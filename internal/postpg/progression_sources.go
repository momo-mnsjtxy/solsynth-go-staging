// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"time"

	"solsynth.local/go-migration/ports/sphere/posts"
)

// recordPostProgressionSource persists the complete server-established fact in
// the same transaction as the post operation. The immutable receipt remains
// valid after an unboost, post edit, or taxonomy soft deletion.
func (r *Repository) recordPostProgressionSource(ctx context.Context, tx *sql.Tx, kind, source, actor, sourcePublisher string, p posts.Post, at time.Time) error {
	if !isPublished(p, at) || p.DeletedAt != nil {
		return nil
	}
	var actionPublisher any
	if sourcePublisher != "" {
		actionPublisher = sourcePublisher
	}
	var tag, category any
	if kind == "post.create_topical" {
		if r == nil || r.associations == nil {
			return nil
		}
		var tagID, categoryID sql.NullString
		if e := tx.QueryRowContext(ctx, `SELECT
		 (SELECT t.id::text FROM owned_taxonomy.post_tag_links l JOIN owned_taxonomy.post_tags t ON t.id=l.tags_id WHERE l.posts_id=$1 AND t.deleted_at IS NULL ORDER BY t.id LIMIT 1),
		 (SELECT c.id::text FROM owned_taxonomy.post_category_links l JOIN owned_taxonomy.post_categories c ON c.id=l.categories_id WHERE l.posts_id=$1 AND c.deleted_at IS NULL ORDER BY c.id LIMIT 1)`, p.ID).Scan(&tagID, &categoryID); e != nil {
			return e
		}
		if !tagID.Valid || !categoryID.Valid {
			return nil
		}
		tag, category = tagID.String, categoryID.String
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_progression_sources(id,kind,post_id,target_publisher_id,action_publisher_id,account_id,published_at,tag_id,category_id,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, source, kind, p.ID, p.PublisherID, actionPublisher, actor, *p.PublishedAt, tag, category, at)
	return e
}
