// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
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

func (r *Repository) WithVisibility(v VisibilityProvider) *Repository {
	r.visibility = v
	realms, _ := v.(accountpg.RealmProvider)
	r.publishers.WithRealms(realms)
	return r
}

// PublisherPredicate is also used by publisher metadata, follows and survey
// adapters. The default excludes organizations unless canonical realms exist.
func PublisherPredicate(v VisibilityProvider) string {
	if policy, ok := v.(accountpg.RealmProvider); ok {
		return policy.PublisherVisibilitySQL("u", "$1")
	}
	return `(u.type=0 AND ` + accountpg.PublisherModerationPredicate("u", "$1", `u.type=0`) + `)`
}
func PublisherMemberPredicate(v VisibilityProvider) string {
	if policy, ok := v.(accountpg.RealmProvider); ok {
		return policy.PublisherMembershipSQL("u", "$1")
	}
	return `u.type=0`
}
func EditorPredicateWithVisibility(v VisibilityProvider) string {
	return `(` + editor + ` AND ` + PublisherMemberPredicate(v) + ` AND ` + RealmPredicate(v, true) + `)`
}

// PrelockPublisherRealms runs before any publisher lock. It returns observed
// immutable ancestry for verification after the ordered publisher lock phase.
func PrelockPublisherRealms(ctx context.Context, tx *sql.Tx, v VisibilityProvider, publishers []string) (map[string]string, error) {
	if policy, ok := v.(accountpg.RealmProvider); ok {
		scopes, e := policy.PrelockPublisherRealms(ctx, tx, publishers)
		if errors.Is(e, accounts.ErrNotFound) {
			e = posts.ErrNotFound
		}
		return scopes, e
	}
	return nil, nil
}

func CanViewPublisher(ctx context.Context, tx *sql.Tx, actor, publisher string, v VisibilityProvider) (bool, error) {
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers u WHERE u.id=$2 AND $1::text IS NOT NULL AND u.deleted_at IS NULL AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=u.account_id AND NOT owner.disabled)) AND `+PublisherPredicate(v)+`)`, actor, publisher).Scan(&allowed)
	return allowed, e
}
func (r *Repository) validatePatch(v posts.Patch, create bool, now time.Time) error {
	if v.Type != nil && *v.Type == posts.Blog && r.blogs != nil {
		v.Type = nil
	}
	if r.scheduling && v.PublishedAt != nil && v.PublishedAt.After(now) && v.DraftedAt == nil {
		v.PublishedAt = nil
	}
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
	return PublisherPredicate(v) + ` AND ` + RealmPredicate(v, false) + ` AND ` + ModerationPredicate(v) + ` AND ((p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND (` + visible + `)) OR ` + EditorPredicateWithVisibility(v) + `)` + blocked
}
func ListingPredicate(v VisibilityProvider) string {
	visible := `p.visibility=0 OR ` + EditorPredicateWithVisibility(v)
	blocked := ""
	if v != nil {
		visible += ` OR (` + v.PostVisibilitySQL() + `)`
		blocked = ` AND NOT (` + v.PostBlockedSQL() + `)`
	}
	return PublisherPredicate(v) + ` AND ` + RealmPredicate(v, false) + ` AND ` + ModerationPredicate(v) + ` AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND (` + visible + `)` + blocked
}

// Global administrative inspection is a separate guarded repository; ordinary
// reads never gain a bypass from an account's global-administration flag.
func ModerationPredicate(v VisibilityProvider) string {
	return `(p.shadowbanned_at IS NULL OR (` + EditorPredicateWithVisibility(v) + `))`
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
