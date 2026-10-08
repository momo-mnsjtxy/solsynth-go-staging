// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"time"
)

// VisibilityProvider is a trusted, compile-time storage policy supplied by the
// canonical social repository. SQL methods must return fixed expressions using
// p (post), u (publisher), and actor $1; never accept or interpolate user input.
// The transaction methods read the same snapshot and acquire no additional locks.
type VisibilityProvider interface {
	IsBlockedInTransaction(context.Context, *sql.Tx, string, string) (bool, error)
	CanReadFriendsInTransaction(context.Context, *sql.Tx, string, string, bool) (bool, error)
	PostVisibilitySQL() string
	PostBlockedSQL() string
}

func (r *Repository) WithVisibility(v VisibilityProvider) *Repository { r.visibility = v; return r }
func (r *Repository) validatePatch(v posts.Patch, create bool, now time.Time) error {
	if v.Visibility != nil && (*v.Visibility == posts.Friends || *v.Visibility == posts.CloseFriends) {
		if r.visibility == nil {
			return posts.ErrUnsupported
		}
		copy := v
		copy.Visibility = nil
		return posts.ValidatePatch(copy, create, now)
	}
	return posts.ValidatePatch(v, create, now)
}
func DetailPredicate(v VisibilityProvider) string {
	visible := `p.visibility IN(0,2)`
	blocked := ""
	if v != nil {
		visible += ` OR (` + v.PostVisibilitySQL() + `)`
		blocked = ` AND NOT (` + v.PostBlockedSQL() + `)`
	}
	return `u.type=0 AND ((p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND (` + visible + `)) OR ` + editor + `)` + blocked
}
func ListingPredicate(v VisibilityProvider) string {
	visible := `p.visibility=0 OR ` + editor
	blocked := ""
	if v != nil {
		visible += ` OR (` + v.PostVisibilitySQL() + `)`
		blocked = ` AND NOT (` + v.PostBlockedSQL() + `)`
	}
	return `u.type=0 AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND (` + visible + `)` + blocked
}
func ReadWithVisibility(ctx context.Context, tx *sql.Tx, actor, id string, v VisibilityProvider) (posts.Post, error) {
	if tx == nil {
		return posts.Post{}, posts.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return posts.Post{}, posts.ErrUnauthenticated
	}
	if !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+base+` AND `+DetailPredicate(v)+` AND p.id=$2`, actor, id))
	EmptyAssociations(&p)
	return p, e
}
