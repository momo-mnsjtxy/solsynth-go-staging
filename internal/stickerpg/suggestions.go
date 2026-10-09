// SPDX-License-Identifier: AGPL-3.0-only
package stickerpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"sort"
	"strings"
)

// SuggestionsInTransaction bounds candidate work and uses the existing pack,
// sticker and public-media projections. It plans all realm/publisher locks before
// hydrating any pack, preserving canonical ordering across a suggestion batch.
func (r *Repository) SuggestionsInTransaction(ctx context.Context, tx *sql.Tx, actor, query string, limit int) ([]stickers.Sticker, error) {
	if r == nil || r.db == nil || r.media == nil {
		return nil, stickers.ErrUnavailable
	}
	if tx == nil || !authority.ValidID(actor) || limit < 1 || limit > 10 || len(query) > 257 {
		return nil, stickers.ErrInvalid
	}
	query = strings.ToLower(query)
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text,p.id::text,p.publisher_id::text FROM owned_stickers s JOIN owned_sticker_packs p ON p.id=s.pack_id JOIN owned_publishers u ON u.id=p.publisher_id JOIN owned_media_files f ON f.id=s.image_id JOIN owned_authority_accounts media_owner ON media_owner.id=f.account_id WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=u.account_id AND NOT a.disabled)) AND f.state='ready' AND f.is_public AND NOT media_owner.disabled AND f.mime_type IN('image/png','image/jpeg','image/gif','image/webp','image/avif') AND left(lower(p.prefix||'+'||s.slug),length($2))=$2 AND `+postpg.PublisherPredicate(r.visibility)+` AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE b.status=-100 AND ((b.account_id=$1::uuid AND b.related_id=u.account_id) OR(b.related_id=$1::uuid AND b.account_id=u.account_id))) AND NOT EXISTS(SELECT 1 FROM owned_timeline_preferences x WHERE x.account_id=$1::uuid AND ((x.kind='account' AND x.reference_id=u.account_id) OR(x.kind='publisher' AND x.reference_id=u.id) OR(x.kind='realm' AND x.reference_id=u.realm_id))) ORDER BY lower(p.prefix||'+'||s.slug),s.id LIMIT $3`, actor, query, limit)
	if e != nil {
		return nil, e
	}
	type candidate struct{ id, pack, pub string }
	items := []candidate{}
	pubSet, packSet := map[string]bool{}, map[string]string{}
	for rows.Next() {
		var v candidate
		if e = rows.Scan(&v.id, &v.pack, &v.pub); e != nil {
			rows.Close()
			return nil, e
		}
		items = append(items, v)
		pubSet[v.pub] = true
		packSet[v.pack] = v.pub
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	pubs := []string{}
	for id := range pubSet {
		pubs = append(pubs, id)
	}
	sort.Strings(pubs)
	scopes, e := postpg.PrelockPublisherRealms(ctx, tx, r.visibility, pubs)
	if e != nil {
		return nil, e
	}
	for _, id := range pubs {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&held); e != nil {
			if e == sql.ErrNoRows {
				return nil, stickers.ErrNotFound
			}
			return nil, e
		}
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return nil, e
	}
	packs := []string{}
	for id := range packSet {
		packs = append(packs, id)
	}
	sort.Strings(packs)
	for _, id := range packs {
		var pub string
		if e = tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_sticker_packs WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&pub); e != nil {
			if e == sql.ErrNoRows {
				return nil, stickers.ErrNotFound
			}
			return nil, e
		}
		if pub != packSet[id] {
			return nil, stickers.ErrConflict
		}
	}
	out := []stickers.Sticker{}
	for _, item := range items {
		x, e := r.pack(ctx, tx, actor, item.pack, false)
		if errors.Is(e, stickers.ErrNotFound) || errors.Is(e, posts.ErrNotFound) || errors.Is(e, accounts.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		v, e := r.sticker(ctx, tx, actor, x.p, item.id)
		if errors.Is(e, stickers.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if strings.HasPrefix(strings.ToLower(v.Identifier), query) {
			out = append(out, v)
		}
	}
	return out, nil
}
