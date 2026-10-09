// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"solsynth.local/go-migration/ports/sphere/posts"
)

type batchPrelocker interface {
	BatchPrelock(context.Context, *sql.Tx, string, []MutationScope) error
}

// Batch runs the canonical post lifecycle in one bounded transaction. The
// complete relation/realm/publisher lock union precedes every per-post write;
// audit, media and durable publication intent roll back together on any failure.
func (r *Repository) Batch(ctx context.Context, actor string, b posts.BatchRequest, deleting bool) error {
	ids, e := b.Validate(deleting)
	if e != nil {
		return e
	}
	locker, ok := r.associations.(batchPrelocker)
	if !ok {
		return posts.ErrUnsupported
	}
	return r.write(ctx, actor, posts.BatchPermission, func(tx *sql.Tx, now time.Time) error {
		scopes := make([]MutationScope, 0, len(ids))
		for _, id := range ids {
			scope := MutationScope{PostID: id, Deleting: deleting, Publishing: !deleting}
			if e = tx.QueryRowContext(ctx, `SELECT publisher_id::text,realm_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&scope.PublisherID, &scope.RealmID); e != nil {
				if errors.Is(e, sql.ErrNoRows) {
					return &posts.Failure{Code: "POST_BATCH_NOT_FOUND", Message: "Posts not found: " + id, Status: 400}
				}
				return e
			}
			scopes = append(scopes, scope)
		}
		if e = locker.BatchPrelock(ctx, tx, actor, scopes); e != nil {
			return e
		}
		// Sample wall time after the full scope wait; past-publication checks
		// and scheduling must not use the start time of a blocked transaction.
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		v := posts.Patch{Visibility: b.Visibility, DraftedAt: b.DraftedAt, PublishedAt: b.PublishedAt}
		if !deleting {
			if e = r.validatePatch(v, false, now); e != nil {
				return e
			}
		}
		out := make([]posts.Post, 0, len(ids))
		for _, id := range ids {
			p, e := r.locked(ctx, tx, actor, id, v, deleting, !deleting)
			if e != nil {
				return e
			}
			if !deleting && b.PublishedAt != nil && b.PublishedAt.Before(now) && p.DraftedAt == nil {
				return &posts.Failure{Code: "POST_PUBLISH_PAST_NOT_ALLOWED", Message: fmt.Sprintf("Cannot set publishedAt to the past for post %s.", id), Status: 400}
			}
			if !deleting && b.PublishedAt != nil && r.authority != nil {
				if _, e = r.authority.Authorize(ctx, r.db, tx, actor, "posts.create"); e != nil {
					return e
				}
			}
			if p.Type == posts.Blog && !deleting {
				if e = r.authorizeBlog(ctx, tx, actor, p); e != nil {
					return e
				}
			}
			out = append(out, p)
		}
		action := "update"
		if deleting {
			action = "delete"
		}
		for i := range out {
			p := &out[i]
			if deleting {
				p.DeletedAt = &now
			} else {
				apply(p, v)
			}
			if !deleting && p.Publisher.Type == 1 && (p.Visibility == posts.Friends || p.Visibility == posts.CloseFriends) {
				return posts.ErrUnsupported
			}
			p.UpdatedAt = now
			if !deleting {
				if e = r.prepareSchedule(ctx, tx, actor, p, v, now); e != nil {
					return e
				}
			}
			if e = save(ctx, tx, *p); e != nil {
				return e
			}
			if e = r.applyAssociations(ctx, tx, actor, p, v, false); e != nil {
				return e
			}
			if e = r.evaluateAutomod(ctx, tx, actor, p, action); e != nil {
				return e
			}
			if e = audit(ctx, tx, actor, p.ID, action, now); e != nil {
				return e
			}
			if e = r.syncSchedule(ctx, tx, actor, p, v, action, now); e != nil {
				return e
			}
			if !deleting {
				if e = r.checkComposedPublication(ctx, tx, actor, p, true); e != nil {
					return e
				}
				if e = r.lockPublicationMedia(ctx, tx, p.ID); e != nil {
					return e
				}
			}
		}
		// Later members of the batch may wait on media, automod or schedules.
		// Recheck every source's live rights and publication gates after all waits.
		for i := range out {
			p := &out[i]
			if deleting {
				if _, e = r.publishers.RequirePostEditor(ctx, tx, actor, p.PublisherID); e != nil {
					return e
				}
				continue
			}
			if _, e = r.locked(ctx, tx, actor, p.ID, posts.Patch{}, false, true); e != nil {
				return e
			}
			if p.Type == posts.Blog {
				if e = r.authorizeBlog(ctx, tx, actor, *p); e != nil {
					return e
				}
			}
			if e = r.finishComposition(ctx, tx, actor, p); e != nil {
				return e
			}
			if p.DraftedAt == nil && p.PublishedAt != nil {
				reason, e := r.checkPublication(ctx, tx, actor, p)
				if e != nil {
					return e
				}
				if reason != "" {
					return posts.ErrForbidden
				}
			}
		}
		if !deleting && b.PublishedAt != nil && r.authority != nil {
			_, e = r.authority.Authorize(ctx, r.db, tx, actor, "posts.create")
			return e
		}
		return nil
	})
}
