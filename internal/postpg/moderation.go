// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// ReadForModerationInTransaction projects only the canonical root post. The
// caller must have freshly checked global administration (or scoped read and
// moderation authority); this helper is never an ordinary visibility bypass.
// Nested objects and attachment bytes retain their separate current read gates.
func ReadForModerationInTransaction(ctx context.Context, tx *sql.Tx, id string) (posts.Post, error) {
	if tx == nil || !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=$1 AND p.deleted_at IS NULL AND u.deleted_at IS NULL`, id))
	if e != nil {
		return posts.Post{}, e
	}
	EmptyAssociations(&p)
	return p, nil
}
