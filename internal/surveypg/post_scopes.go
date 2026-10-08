// SPDX-License-Identifier: AGPL-3.0-only
package surveypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/surveys"
)

func (r *Repository) lockPostScopes(ctx context.Context, tx *sql.Tx, publisher string, edit bool, links ...*string) (map[string]postpg.MutationScope, error) {
	ids := []string{}
	for _, id := range links {
		if id != nil && *id != "" {
			ids = append(ids, *id)
		}
	}
	observed, e := postpg.ObservePostScopes(ctx, tx, ids)
	if e != nil {
		if errors.Is(e, posts.ErrNotFound) {
			return nil, surveys.ErrNotFound
		}
		return nil, e
	}
	for _, s := range observed {
		if s.PublisherID != publisher {
			return nil, surveys.ErrInvalid
		}
	}
	scopes, e := postpg.PrelockPublishingScopes(ctx, tx, r.visibility, []string{publisher}, postpg.ObservedRealmIDs(observed), false)
	if e != nil {
		if errors.Is(e, posts.ErrNotFound) {
			return nil, surveys.ErrNotFound
		}
		return nil, e
	}
	mode := ` FOR SHARE`
	if edit {
		mode = ` FOR UPDATE`
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL`+mode, publisher).Scan(&held); e != nil {
		return nil, mapped(e)
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return nil, e
	}
	if e = postpg.VerifyPostScopes(ctx, tx, observed); e != nil {
		if errors.Is(e, posts.ErrNotFound) {
			return nil, surveys.ErrNotFound
		}
		return nil, e
	}
	return observed, nil
}
