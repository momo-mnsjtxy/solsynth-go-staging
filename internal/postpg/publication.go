// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

// PublicationObserver joins the canonical publication transaction. It may only
// persist local intent, never contact providers or lock recipient accounts after
// domain locks. Failure rolls back the publication and its audit/schedule.
type PublicationObserver interface {
	RecordPublication(context.Context, *sql.DB, *sql.Tx, posts.Post) error
}

func (r *Repository) WithPublicationObserver(p PublicationObserver) *Repository {
	r.publications = p
	return r
}
func isPublished(p posts.Post, now time.Time) bool {
	return p.DeletedAt == nil && p.DraftedAt == nil && p.PublishedAt != nil && !p.PublishedAt.After(now)
}
func (r *Repository) recordPublication(ctx context.Context, tx *sql.Tx, p posts.Post, wasPublished bool, now time.Time) error {
	if r.publications == nil || wasPublished || !isPublished(p, now) {
		return nil
	}
	return r.publications.RecordPublication(ctx, r.db, tx, p)
}
