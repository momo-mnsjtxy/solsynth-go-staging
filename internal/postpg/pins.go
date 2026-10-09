// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type PinRealmProvider interface {
	RequirePostRealmModerator(context.Context, *sql.Tx, string, string) error
}

var _ posts.PinRepository = (*Repository)(nil)

func (r *Repository) Pin(ctx context.Context, actor, id string, mode int) (posts.Post, error) {
	if mode != posts.PublisherPin && mode != posts.RealmPin && mode != posts.ReplyPin {
		return posts.Post{}, posts.ErrUnsupported
	}
	return r.changePin(ctx, actor, id, &mode)
}
func (r *Repository) Unpin(ctx context.Context, actor, id string) (posts.Post, error) {
	return r.changePin(ctx, actor, id, nil)
}
func (r *Repository) changePin(ctx context.Context, actor, id string, mode *int) (posts.Post, error) {
	if !authority.ValidID(id) {
		return posts.Post{}, posts.ErrNotFound
	}
	var out posts.Post
	e := r.write(ctx, actor, "posts.pin", func(tx *sql.Tx, now time.Time) error {
		var observedParent *string
		if e := tx.QueryRowContext(ctx, `SELECT replied_post_id::text FROM owned_post_relations WHERE post_id=$1`, id).Scan(&observedParent); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		ids := []string{id}
		if observedParent != nil {
			ids = append(ids, *observedParent)
		}
		observed, e := ObservePostScopes(ctx, tx, ids)
		if e != nil {
			return e
		}
		scope := observed[id]
		publishers := []string{scope.PublisherID}
		if observedParent != nil {
			publishers = append(publishers, observed[*observedParent].PublisherID)
		}
		sort.Strings(publishers)
		// Plan all realm locks before the publisher/post locks. Observations never
		// grant access, and ancestry is compared again after blocking acquisition.
		realms, e := PrelockPublishingScopes(ctx, tx, r.visibility, publishers, ObservedRealmIDs(observed), false)
		if e != nil {
			return e
		}
		last := ""
		for _, publisher := range publishers {
			if publisher == last {
				continue
			}
			last = publisher
			var held string
			if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, publisher).Scan(&held); e != nil {
				return e
			}
		}
		publisherMember := `p.type=0`
		if membership, ok := r.visibility.(accountpg.RealmProvider); ok {
			publisherMember = membership.PublisherMembershipSQL("p", "$2")
		}
		requireEditor := func(publisher string) error {
			var allowed bool
			e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members m JOIN owned_publishers p ON p.id=m.publisher_id LEFT JOIN owned_realms realm ON realm.id=p.realm_id LEFT JOIN owned_authority_accounts owner ON owner.id=COALESCE(p.account_id,realm.account_id) WHERE m.publisher_id=$1 AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=50 AND p.deleted_at IS NULL AND owner.id IS NOT NULL AND NOT owner.disabled AND `+publisherMember+`)`, publisher, actor).Scan(&allowed)
			if e != nil {
				return e
			}
			if !allowed {
				return posts.ErrForbidden
			}
			return nil
		}
		authorityPublisher := scope.PublisherID
		if observedParent != nil {
			authorityPublisher = observed[*observedParent].PublisherID
		}
		if e = requireEditor(authorityPublisher); e != nil {
			return e
		}
		if observedParent != nil {
			if _, e = ReadWithVisibility(ctx, tx, actor, *observedParent, r.visibility); e != nil {
				return e
			}
		}
		if e = VerifyPostScopes(ctx, tx, observed); e != nil {
			return e
		}
		if e = accountpg.VerifyPublisherRealms(ctx, tx, realms); e != nil {
			return e
		}
		sort.Strings(ids)
		for _, postID := range ids {
			var held string
			if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, postID).Scan(&held); e != nil {
				return e
			}
		}
		if observedParent != nil {
			if _, e = ReadWithVisibility(ctx, tx, actor, *observedParent, r.visibility); e != nil {
				return e
			}
		}
		if e = VerifyPostScopes(ctx, tx, observed); e != nil {
			return e
		}
		p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+base+` AND p.id=$1 FOR UPDATE OF p`, id))
		if e != nil {
			return e
		}
		if p.PublisherID != scope.PublisherID || !SameOptionalID(p.RealmID, scope.RealmID) {
			return posts.ErrConflict
		}
		// This query reads only authored, owned relation IDs and executes while the
		// source publisher and post are locked against relation mutation.
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT replied_post_id::text FROM owned_post_relations WHERE post_id=$1),(SELECT chained_post_id::text FROM owned_post_relations WHERE post_id=$1)`, id).Scan(&p.RepliedPostID, &p.ChainedPostID); e != nil {
			return e
		}
		required := mode
		if observedParent == nil && p.RepliedPostID != nil || observedParent != nil && (p.RepliedPostID == nil || *p.RepliedPostID != *observedParent) {
			return posts.ErrConflict
		}
		if mode == nil {
			required = p.PinMode
			if p.RepliedPostID != nil && (required == nil || *required != posts.ReplyPin) || p.RepliedPostID == nil && required != nil && *required != posts.PublisherPin && *required != posts.RealmPin {
				return posts.ErrUnpinFailed
			}
		} else if e = posts.ValidatePin(p, *mode); e != nil {
			return e
		}
		if e = r.requirePinRealm(ctx, tx, actor, p, required); e != nil {
			return e
		}
		var priorAudit int64
		if r.scheduling {
			if priorAudit, e = postAudit(ctx, tx, id); e != nil {
				return e
			}
		}
		// No ordinal or reorder contract exists. The single mode transition and its
		// audit entry commit together, including idempotent repeated pin/unpin calls.
		if _, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET pin_mode=$2,updated_at=$3 WHERE id=$1`, id, mode, now); e != nil {
			return e
		}
		action := "pin"
		if mode == nil {
			action = "unpin"
		}
		if e = audit(ctx, tx, actor, id, action, now); e != nil {
			return e
		}
		if e = r.syncPinSchedule(ctx, tx, actor, id, priorAudit, now); e != nil {
			return e
		}
		out, e = ReadWithVisibility(ctx, tx, actor, id, r.visibility)
		if e != nil {
			return e
		}
		if r.associations != nil {
			if e = r.associations.Load(ctx, tx, actor, &out); e != nil {
				return e
			}
		}
		if e = r.loadAutomod(ctx, tx, actor, &out); e != nil {
			return e
		}
		if e = r.loadSchedule(ctx, tx, actor, &out); e != nil {
			return e
		}
		if e = RecheckAssociations(ctx, tx, actor, &out, r.associations); e != nil {
			return e
		}
		// A realm owner's account can be disabled while its realm row stays locked.
		// Recheck the entire current source audience after hydration and schedule
		// waits, before write() revalidates the original signed authority proof.
		if e = requireEditor(authorityPublisher); e != nil {
			return e
		}
		if e = r.requirePinRealm(ctx, tx, actor, p, required); e != nil {
			return e
		}
		_, e = ReadWithVisibility(ctx, tx, actor, id, r.visibility)
		return e
	})
	if e != nil {
		return posts.Post{}, e
	}
	posts.RedactAutomodForEditor(&out)
	return out, nil
}

func (r *Repository) requirePinRealm(ctx context.Context, tx *sql.Tx, actor string, p posts.Post, required *int) error {
	if p.RealmID == nil {
		return nil
	}
	realms, ok := r.visibility.(RealmAssociationProvider)
	if !ok {
		return posts.ErrUnsupported
	}
	if e := realms.RequirePostRealmMember(ctx, tx, actor, *p.RealmID); e != nil {
		return e
	}
	if required != nil && *required == posts.RealmPin {
		moderators, ok := r.visibility.(PinRealmProvider)
		if !ok {
			return posts.ErrUnsupported
		}
		return moderators.RequirePostRealmModerator(ctx, tx, actor, *p.RealmID)
	}
	return nil
}

// Pin metadata does not replace publication intent. Advance only a still-valid
// pending audit cursor, keeping its original actor, proof, policy and due time.
// An already-stale cursor, blocked intent or other intervening change is never
// repaired by a caller who has only posts.pin authority.
func (r *Repository) syncPinSchedule(ctx context.Context, tx *sql.Tx, actor, id string, priorAudit int64, now time.Time) error {
	if !r.scheduling {
		return nil
	}
	currentAudit, e := postAudit(ctx, tx, id)
	if e != nil {
		return e
	}
	var schedule, revision int64
	e = tx.QueryRowContext(ctx, `UPDATE owned_post_schedules SET revision=revision+1,post_audit_id=$3,updated_at=$4 WHERE post_id=$1 AND state='pending' AND post_audit_id=$2 RETURNING id,revision`, id, priorAudit, currentAudit, now).Scan(&schedule, &revision)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	return scheduleAudit(ctx, tx, schedule, revision, id, actor, "edited", now)
}

func pinFilter(o posts.ListOptions) string {
	if o.Pinned == nil {
		return ""
	}
	if !*o.Pinned {
		return ` AND p.pin_mode IS NULL`
	}
	if o.Realm != "" {
		return ` AND p.pin_mode=1`
	}
	return ` AND p.pin_mode=0`
}
func (r *Repository) readList(ctx context.Context, actor string, pinned bool) (*sql.Tx, error) {
	if !pinned {
		return r.read(ctx, actor)
	}
	if r == nil || r.db == nil {
		return nil, posts.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		if r.authority != nil {
			if _, e = r.authority.Authenticate(ctx, r.db, tx, actor); e != nil {
				tx.Rollback()
				return nil, e
			}
		}
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&enabled)
		if e != nil || !enabled {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, posts.ErrUnauthenticated
		}
	}
	return tx, nil
}
func (r *Repository) lockPinnedPage(ctx context.Context, tx *sql.Tx, actor string, page []posts.Post, where string, args []any) ([]posts.Post, error) {
	ids := []string{}
	publishers := []string{}
	explicit := []string{}
	seen := map[string]bool{}
	for _, p := range page {
		ids = append(ids, p.ID)
		explicit = append(explicit, RealmIDs(p.RealmID)...)
		if !seen[p.PublisherID] {
			publishers = append(publishers, p.PublisherID)
			seen[p.PublisherID] = true
		}
	}
	scopes, e := PrelockPublishingScopes(ctx, tx, r.visibility, publishers, explicit, false)
	if e != nil {
		return nil, e
	}
	sort.Strings(publishers)
	for _, id := range publishers {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&held); e != nil {
			return nil, mapped(e)
		}
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return nil, e
	}
	// Lock the observed post ancestry in a stable order, then obtain fresh rows.
	sort.Strings(ids)
	for _, id := range ids {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 FOR SHARE`, id).Scan(&held); e != nil {
			return nil, mapped(e)
		}
	}
	out := []posts.Post{}
	for _, prior := range page {
		p, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+where+` AND p.id=$5`, append(args, prior.ID)...))
		if errors.Is(e, posts.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if p.PublisherID != prior.PublisherID || !SameOptionalID(p.RealmID, prior.RealmID) {
			return nil, posts.ErrConflict
		}
		out = append(out, p)
	}
	return out, nil
}

// Hydration may wait on another provider. Recheck every root's current audience
// and pin state, retaining only the already hydrated rows still in this scope.
func recheckPinnedPage(ctx context.Context, tx *sql.Tx, page []posts.Post, where string, args []any) ([]posts.Post, error) {
	out := make([]posts.Post, 0, len(page))
	for _, p := range page {
		var visible bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+where+` AND p.id=$5)`, append(args, p.ID)...).Scan(&visible); e != nil {
			return nil, e
		}
		if visible {
			out = append(out, p)
		}
	}
	return out, nil
}
