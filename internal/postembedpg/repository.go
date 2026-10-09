// SPDX-License-Identifier: AGPL-3.0-only
package postembedpg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"sort"
)

//go:embed schema.sql
var Schema string

// Repository participates in the existing post transaction. Its supported
// ownership scope introduces no new account/publisher/realm locks: account-owned
// references must belong to the actor and publisher-owned references to the
// source publisher. Target rows are ordered before media; projections are live.
type Repository struct {
	db         *sql.DB
	next       postpg.AssociationStore
	visibility postpg.VisibilityProvider
}

func New(db *sql.DB, next postpg.AssociationStore, v postpg.VisibilityProvider) *Repository {
	return &Repository{db, next, v}
}
func (r *Repository) SupportsPostComposition() bool { return r != nil && r.visibility != nil }
func (r *Repository) SupportsPostAttachments() bool {
	v, ok := r.next.(interface{ SupportsPostAttachments() bool })
	return ok && v.SupportsPostAttachments()
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT post_id,kind,target_id,location,position FROM owned_post_embeds LIMIT 0;SELECT post_id,file_id FROM owned_post_thumbnails LIMIT 0`)
	return e
}
func stored(ctx context.Context, tx *sql.Tx, id string) ([]posts.Embed, error) {
	rows, e := tx.QueryContext(ctx, `SELECT kind,COALESCE(target_id::text,''),location FROM owned_post_embeds WHERE post_id=$1 ORDER BY position`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []posts.Embed{}
	for rows.Next() {
		var v posts.Embed
		var raw []byte
		if e = rows.Scan(&v.Type, &v.ID, &raw); e != nil {
			return nil, e
		}
		if v.Type == "location" {
			if json.Unmarshal(raw, &v) != nil || v.Type != "location" {
				return nil, posts.ErrUnavailable
			}
		}
		if e = v.Validate(); e != nil {
			return nil, posts.ErrUnavailable
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func ordered(v []posts.Embed) []posts.Embed {
	out := append([]posts.Embed{}, v...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type == out[j].Type {
			return out[i].ID < out[j].ID
		}
		return out[i].Type < out[j].Type
	})
	return out
}
func (r *Repository) check(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v []posts.Embed, lock bool) error {
	// Observe the complete scope first. A different owner or publisher cannot be
	// added after the lifecycle has already taken its canonical actor lock.
	for _, x := range ordered(v) {
		if x.Type == "location" || x.Type == "notable_day" {
			continue
		}
		scope, e := r.scope(ctx, tx, x, false)
		if e != nil {
			return e
		}
		if !owns(actor, p, x, scope) {
			return posts.ErrForbidden
		}
	}
	for _, x := range ordered(v) {
		if x.Type == "location" {
			continue
		}
		scope, e := r.scope(ctx, tx, x, lock)
		if e != nil {
			return e
		}
		if x.Type != "notable_day" && !owns(actor, p, x, scope) {
			return posts.ErrForbidden
		}
		ok, e := r.visible(ctx, tx, actor, p, x)
		if e != nil {
			return e
		}
		if !ok {
			return posts.ErrNotFound
		}
	}
	return nil
}
func owns(actor string, p *posts.Post, x posts.Embed, scope string) bool {
	if x.Type == "survey" || x.Type == "live_stream" {
		return scope == p.PublisherID
	}
	return scope == actor
}
func (r *Repository) scope(ctx context.Context, tx *sql.Tx, x posts.Embed, lock bool) (string, error) {
	table, column := "", ""
	switch x.Type {
	case "survey":
		table, column = "owned_surveys", "publisher_id"
	case "live_stream":
		table, column = "owned_livestreams", "publisher_id"
	case "fund":
		table, column = "wallet_v1.funds", "creator_account_id"
	case "meet":
		table, column = "owned_meets", "host_id"
	case "calendar_event":
		table, column = "owned_calendar_events", "account_id"
	case "notable_day":
		table, column = "owned_notable_days", "id"
	default:
		return "", posts.ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var owner string
	e := tx.QueryRowContext(ctx, `SELECT `+column+`::text FROM `+table+` WHERE id=$1`+suffix, x.ID).Scan(&owner)
	if errors.Is(e, sql.ErrNoRows) {
		return "", posts.ErrNotFound
	}
	return owner, e
}
func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, create bool) error {
	if r == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if tx == nil || p == nil || !authority.ValidID(actor) {
		return posts.ErrInvalid
	}
	if p.DeletedAt != nil {
		for _, table := range []string{"owned_post_embeds", "owned_post_thumbnails"} {
			if _, e := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE post_id=$1`, p.ID); e != nil {
				return e
			}
		}
		if r.next != nil {
			return r.next.Apply(ctx, tx, actor, p, v, create)
		}
		return nil
	}
	old, e := stored(ctx, tx, p.ID)
	if e != nil {
		return e
	}
	links, e := posts.ResolveEmbeds(old, v)
	if e != nil {
		return e
	}
	if p.Type == posts.Blog && (len(links) > 0 || v.ThumbnailID != nil) {
		return posts.ErrUnsupported
	}
	if e = r.check(ctx, tx, actor, p, links, true); e != nil {
		return e
	}
	if r.next != nil {
		if e = r.next.Apply(ctx, tx, actor, p, v, create); e != nil {
			return e
		}
	}
	// Media may have waited. Expiry and current target policy must be checked
	// again using the database clock, while all planned rows remain locked.
	if e = r.check(ctx, tx, actor, p, links, false); e != nil {
		return e
	}
	if v.Embeds != nil || len(v.EmbedUpdates) > 0 {
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_embeds WHERE post_id=$1`, p.ID); e != nil {
			return e
		}
		for i, x := range links {
			var target, location any
			if x.Type == "location" {
				b, _ := json.Marshal(x)
				location = b
			} else {
				target = x.ID
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_embeds(post_id,position,kind,target_id,location)VALUES($1,$2,$3,$4,$5::jsonb)`, p.ID, i, x.Type, target, location); e != nil {
				return e
			}
		}
	}
	var thumbnail *string
	e = tx.QueryRowContext(ctx, `SELECT file_id::text FROM owned_post_thumbnails WHERE post_id=$1`, p.ID).Scan(&thumbnail)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	e = nil
	if v.ThumbnailSet {
		thumbnail = v.ThumbnailID
	}
	if thumbnail != nil {
		if p.Type != posts.Article {
			return &posts.Failure{Code: "POST_THUMBNAIL_NOT_SUPPORTED", Message: "Thumbnail only supported in article.", Status: 400}
		}
		var linked bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_post_media WHERE post_id=$1 AND file_id=$2)`, p.ID, *thumbnail).Scan(&linked)
		if e != nil {
			return e
		}
		if !linked {
			return &posts.Failure{Code: "POST_THUMBNAIL_NOT_IN_ATTACHMENTS", Message: "Thumbnail must be present in attachment list.", Status: 400}
		}
	}
	if v.ThumbnailSet {
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_thumbnails WHERE post_id=$1`, p.ID); e != nil {
			return e
		}
		if thumbnail != nil {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_thumbnails(post_id,file_id)VALUES($1,$2)`, p.ID, *thumbnail)
		}
	}
	return e
}
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	if r == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if tx == nil || p == nil {
		return posts.ErrInvalid
	}
	if r.next != nil {
		if e := r.next.Load(ctx, tx, actor, p); e != nil {
			return e
		}
	} else {
		postpg.EmptyAssociations(p)
	}
	if p.Meta != nil {
		p.Meta.Composed = nil
		p.Meta.Thumbnail = nil
	}
	if p.DeletedAt != nil {
		return nil
	}
	if _, e := postpg.ReadWithVisibility(ctx, tx, actor, p.ID, r.visibility); e != nil {
		return e
	}
	all, e := stored(ctx, tx, p.ID)
	if e != nil {
		return e
	}
	for _, x := range all {
		ok, e := r.visible(ctx, tx, actor, p, x)
		if e != nil {
			return e
		}
		if !ok {
			continue
		}
		if p.Meta == nil {
			p.Meta = &posts.Metadata{}
		}
		p.Meta.Composed = append(p.Meta.Composed, x)
	}
	var thumbnail string
	e = tx.QueryRowContext(ctx, `SELECT file_id::text FROM owned_post_thumbnails WHERE post_id=$1`, p.ID).Scan(&thumbnail)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	// The media association already independently rechecked owner authorization,
	// availability and source audience. Hidden metadata also hides the thumbnail ID.
	if p.Type == posts.Article && thumbnail != "" {
		for _, a := range p.Attachments {
			if a["id"] == thumbnail {
				if p.Meta == nil {
					p.Meta = &posts.Metadata{}
				}
				p.Meta.Thumbnail = &thumbnail
				break
			}
		}
	}
	return nil
}
func (r *Repository) CheckPostPublication(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, lock bool) error {
	v, e := stored(ctx, tx, p.ID)
	if e != nil {
		return e
	}
	return r.check(ctx, tx, actor, p, v, lock)
}
