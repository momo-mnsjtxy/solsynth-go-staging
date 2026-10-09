// SPDX-License-Identifier: AGPL-3.0-only
package postmediapg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"sort"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/postmedia"
	"solsynth.local/go-migration/ports/sphere/posts"
)

//go:embed schema.sql
var Schema string

// Repository joins the lifecycle transaction and delegates existing projections
// to next. Compose timeline.WithAssociations(postmedia.New(db, interactions))
// so nested timeline posts receive media and taxonomy/interaction projections.
type Repository struct {
	db         *sql.DB
	next       postpg.AssociationStore
	visibility postpg.VisibilityProvider
}

func New(db *sql.DB, next postpg.AssociationStore) *Repository {
	return &Repository{db: db, next: next, visibility: socialpg.New(db)}
}
func (r *Repository) WithVisibility(v postpg.VisibilityProvider) *Repository {
	r.visibility = v
	return r
}
func (r *Repository) SupportsPostAttachments() bool { return r != nil }
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT post_id,file_id,position,owner_authorized FROM owned_post_media LIMIT 0`)
	return e
}

const fileFields = `f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`

func scanFile(row interface{ Scan(...any) error }) (f media.File, e error) {
	e = row.Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	return
}

func (r *Repository) Apply(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post, v posts.Patch, create bool) error {
	if tx == nil || p == nil || !authority.ValidID(actor) {
		return posts.ErrInvalid
	}
	if r == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if r.next != nil {
		if e := r.next.Apply(ctx, tx, actor, p, v, create); e != nil {
			return e
		}
	} else {
		for _, ids := range []*[]string{v.Tags, v.Categories, v.CollectionIDs} {
			if ids != nil && len(*ids) > 0 {
				return posts.ErrUnsupported
			}
		}
	}
	if p.DeletedAt != nil {
		_, e := tx.ExecContext(ctx, `DELETE FROM owned_post_media WHERE post_id=$1`, p.ID)
		return e
	}
	var ids []string
	if v.Attachments != nil {
		var valid bool
		ids, valid = postmedia.NormalizeIDs(*v.Attachments)
		if !valid {
			return posts.ErrInvalid
		}
	}

	// Actor, publisher, current post, related-post locks precede attachment locks.
	// File locks are sorted; no quota or additional account locks are taken here.
	existing := map[string]bool{}
	rows, e := tx.QueryContext(ctx, `SELECT file_id::text,owner_authorized FROM owned_post_media WHERE post_id=$1 ORDER BY position LIMIT 32`, p.ID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		var authorized bool
		if e = rows.Scan(&id, &authorized); e != nil {
			rows.Close()
			return e
		}
		existing[id] = authorized
		if v.Attachments == nil {
			ids = append(ids, id)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) > 0 {
		realms := []*string{p.RealmID, p.Publisher.RealmID}
		// The canonical timeline prelocker retains reply target realms through
		// this same transaction, including when an existing reply is edited.
		var parentRealm, publisherRealm *string
		e := tx.QueryRowContext(ctx, `SELECT parent.realm_id::text,publisher.realm_id::text FROM owned_post_relations link JOIN owned_text_posts parent ON parent.id=link.replied_post_id JOIN owned_publishers publisher ON publisher.id=parent.publisher_id WHERE link.post_id=$1`, p.ID).Scan(&parentRealm, &publisherRealm)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		realms = append(realms, parentRealm, publisherRealm)
		seen := map[string]bool{}
		for _, realm := range realms {
			if realm != nil && !seen[*realm] {
				seen[*realm] = true
				if e := postpg.RequireRealmAction(ctx, tx, r.visibility, actor, *realm, social.CanUploadMedia); e != nil {
					return e
				}
			}
		}
	}
	orderedLocks := append([]string{}, ids...)
	sort.Strings(orderedLocks)
	grants := make(map[string]bool, len(ids))
	for _, id := range orderedLocks {
		f, e := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileFields+` FROM owned_media_files f WHERE f.id=$1 FOR SHARE OF f`, id))
		if errors.Is(e, sql.ErrNoRows) {
			return posts.ErrNotFound
		}
		if e != nil {
			return e
		}
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, f.AccountID).Scan(&active); e != nil {
			return e
		}
		if v.Attachments == nil {
			continue
		} // Lock retained sources without reviving unavailable metadata.
		if f.State != "ready" || !active {
			return posts.ErrNotFound
		}
		ownerGrant, retained := existing[id]
		// Seeing a private file through another post does not authorize redistributing
		// it to a new audience. An editor may retain an already authorized same-post link.
		if f.AccountID != actor && !f.Public && (!retained || !ownerGrant) {
			return posts.ErrForbidden
		}
		grants[id] = f.AccountID == actor || (retained && ownerGrant)
	}
	if v.Attachments == nil {
		return nil
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_media WHERE post_id=$1`, p.ID); e != nil {
		return e
	}
	for i, id := range ids {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_media(post_id,file_id,position,owner_authorized,created_at)VALUES($1,$2,$3,$4,CURRENT_TIMESTAMP)`, p.ID, id, i, grants[id]); e != nil {
			return e
		}
	}
	return nil
}

// Load hydrates live metadata in the caller's authorized post snapshot. Public
// media remains public; private files require their owner's explicit post link.
func (r *Repository) Load(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	if tx == nil || p == nil {
		return posts.ErrInvalid
	}
	if r == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if r.next != nil {
		if e := r.next.Load(ctx, tx, actor, p); e != nil {
			return e
		}
	} else {
		postpg.EmptyAssociations(p)
	}
	return r.loadVisibleAttachments(ctx, tx, actor, p)
}

func (r *Repository) RecheckProjection(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	if r == nil || r.visibility == nil || tx == nil || p == nil {
		return posts.ErrUnavailable
	}
	if e := postpg.RecheckAssociations(ctx, tx, actor, p, r.next); e != nil {
		return e
	}
	return r.loadVisibleAttachments(ctx, tx, actor, p)
}

func (r *Repository) loadVisibleAttachments(ctx context.Context, tx *sql.Tx, actor string, p *posts.Post) error {
	p.Attachments = []postmedia.Attachment{}
	if p.DeletedAt != nil {
		return nil
	}
	if _, e := postpg.ReadWithVisibility(ctx, tx, actor, p.ID, r.visibility); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fileFields+` FROM owned_post_media l JOIN owned_media_files f ON f.id=l.file_id JOIN owned_authority_accounts a ON a.id=f.account_id WHERE l.post_id=$1 AND f.state='ready' AND NOT a.disabled AND (f.is_public OR f.account_id::text=$2 OR l.owner_authorized) ORDER BY l.position LIMIT 32`, p.ID, actor)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		f, e := scanFile(rows)
		if e != nil {
			return e
		}
		p.Attachments = append(p.Attachments, postmedia.Project(f))
	}
	return rows.Err()
}

// CanReadInTransaction is the optional canonical media read extension. It is
// deliberately read-only: taking post locks after the media row would invert
// attachment mutation ordering. A read linearizes at this fresh SQL snapshot;
// visibility changes committed before it are enforced, including social blocks.
func (r *Repository) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, fileID string) (bool, error) {
	if r == nil || r.visibility == nil || tx == nil {
		return false, media.ErrUnavailable
	}
	if !authority.ValidID(fileID) || actor != "" && !authority.ValidID(actor) {
		return false, nil
	}
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+postpg.ActivePostFrom+` AND `+postpg.DetailPredicate(r.visibility)+` AND EXISTS(SELECT 1 FROM owned_post_media l WHERE l.post_id=p.id AND l.file_id=$2 AND l.owner_authorized))`, actor, fileID).Scan(&allowed)
	return allowed, e
}
