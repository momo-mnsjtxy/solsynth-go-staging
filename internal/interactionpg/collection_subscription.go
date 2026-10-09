// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

// WithPostProjection supplies the same complete, visibility-aware association
// chain as post detail. It is used only for reads, never as Apply's successor:
// timeline -> embeds -> media -> this repository's Load is not a recursion.
func (r *Repository) WithPostProjection(p postpg.AssociationStore) *Repository {
	r.projection = p
	return r
}

type collectionScope struct{ ID, Publisher, Owner, Realm, RealmOwner string }

func observeCollection(ctx context.Context, tx *sql.Tx, publisher, slug string) (s collectionScope, e error) {
	e = tx.QueryRowContext(ctx, `SELECT c.id::text,u.id::text,COALESCE(u.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(realm.account_id::text,'') FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id LEFT JOIN owned_realms realm ON realm.id=u.realm_id WHERE c.deleted_at IS NULL AND u.deleted_at IS NULL AND `+accountpg.PublisherNamePredicate("u", "$1")+` AND c.slug=$2`, publisher, taxonomy.NormalizeSlug(slug)).Scan(&s.ID, &s.Publisher, &s.Owner, &s.Realm, &s.RealmOwner)
	if errors.Is(e, sql.ErrNoRows) {
		e = interactions.ErrCollectionNotFound
	}
	return
}
func (r *Repository) visibleCollection(ctx context.Context, tx *sql.Tx, actor, publisher, slug string) (posts.Collection, error) {
	return scanCollection(tx.QueryRowContext(ctx, `SELECT `+collectionColumns+` FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id WHERE c.deleted_at IS NULL AND `+postpg.VisiblePublisherPredicate(r.visibility, "u", "$1", false)+` AND `+accountpg.PublisherNamePredicate("u", "$2")+` AND c.slug=$3`, actor, publisher, taxonomy.NormalizeSlug(slug)))
}
func (r *Repository) collectionAuthority(ctx context.Context, tx *sql.Tx, actor, permission string, preflight bool) error {
	if actor == "" {
		return nil
	}
	if r.guard != nil {
		var e error
		if preflight {
			permissions := []string{}
			if permission != "" {
				permissions = append(permissions, permission)
			}
			_, e = r.guard.Preflight(ctx, r.db, tx, actor, permissions...)
		} else if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if e != nil {
			return e
		}
	}
	var active bool
	if e := tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&active); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return posts.ErrUnauthenticated
		}
		return e
	}
	if !active {
		return posts.ErrUnauthenticated
	}
	return nil
}

// Resolve handles strictly as names, observe before locks, and bind the exact
// incarnation afterward. Sorted accounts -> realms -> publisher -> collection
// ordering composes with canonical moderation and publisher writes. Every query
// uses this transaction; a single-connection pool is sufficient.
func (r *Repository) collectionTransaction(ctx context.Context, actor, publisher, slug, permission string, required bool, fn func(*sql.Tx, posts.Collection) error, prepare ...func(*sql.Tx, string) (*collectionDependencyPlan, error)) error {
	if r == nil || r.db == nil {
		return interactions.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = r.collectionAuthority(ctx, tx, actor, permission, true); e != nil {
		return e
	}
	before, e := observeCollection(ctx, tx, publisher, slug)
	if e != nil {
		return e
	}
	var dependencies *collectionDependencyPlan
	if len(prepare) > 0 {
		dependencies, e = prepare[0](tx, before.ID)
		if e != nil {
			return e
		}
	}
	ids := []string{actor, before.Owner, before.RealmOwner}
	if dependencies != nil {
		ids = append(ids, dependencies.accounts()...)
	}
	for _, id := range sortedCollectionIDs(ids) {
		mode := ` FOR SHARE`
		if id == actor {
			mode = ` FOR UPDATE`
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1`+mode, id).Scan(&held); e != nil {
			return missing(e)
		}
	}
	var scopes map[string]string
	if dependencies == nil {
		scopes, e = postpg.PrelockPublisherRealms(ctx, tx, r.visibility, []string{before.Publisher})
		if e != nil {
			return e
		}
	} else {
		// Deleted nested sources remain lockable and are subsequently redacted.
		// No dependency is authorized by this ancestry-only observation.
		for _, realm := range dependencies.Realms {
			var held string
			if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid FOR SHARE`, realm.ID).Scan(&held); e != nil {
				return e
			}
		}
	}
	publishers := []string{before.Publisher}
	if dependencies != nil {
		for _, pub := range dependencies.Publishers {
			publishers = append(publishers, pub.ID)
		}
	}
	var held string
	for _, id := range sortedCollectionIDs(publishers) {
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1::uuid FOR SHARE`, id).Scan(&held); e != nil {
			return missing(e)
		}
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_post_collections WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, before.ID).Scan(&held); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return interactions.ErrCollectionNotFound
		}
		return e
	}
	if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
		return e
	}
	check := func() (posts.Collection, error) {
		if e := r.collectionAuthority(ctx, tx, actor, permission, false); e != nil {
			return posts.Collection{}, e
		}
		after, e := observeCollection(ctx, tx, publisher, slug)
		if e != nil {
			return posts.Collection{}, e
		}
		if before != after {
			return posts.Collection{}, interactions.ErrCollectionNotFound
		}
		return r.visibleCollection(ctx, tx, actor, publisher, slug)
	}
	c, e := check()
	if e != nil {
		return e
	}
	if e = fn(tx, c); e != nil {
		return e
	}
	if _, e = check(); e != nil {
		return e
	}
	return tx.Commit()
}

const collectionSubscriptionColumns = `id::text,account_id::text,category_id::text,tag_id::text,collection_id::text`

func collectionSubscription(ctx context.Context, tx *sql.Tx, actor, collection string) (v interactions.CollectionSubscription, e error) {
	e = tx.QueryRowContext(ctx, `SELECT `+collectionSubscriptionColumns+` FROM owned_taxonomy.post_category_subscriptions WHERE account_id=$1 AND collection_id=$2 AND deleted_at IS NULL`, actor, collection).Scan(&v.ID, &v.AccountID, &v.CategoryID, &v.TagID, &v.CollectionID)
	if errors.Is(e, sql.ErrNoRows) {
		e = interactions.ErrCollectionSubscriptionNotFound
	}
	return
}
func (r *Repository) SubscribeCollection(ctx context.Context, actor, publisher, slug string) (out interactions.CollectionSubscription, created bool, e error) {
	e = r.collectionTransaction(ctx, actor, publisher, slug, "post.collections.posts.manage", true, func(tx *sql.Tx, c posts.Collection) error {
		v, x := collectionSubscription(ctx, tx, actor, c.ID)
		if x == nil {
			out = v
			return nil
		}
		if !errors.Is(x, interactions.ErrCollectionSubscriptionNotFound) {
			return x
		}
		id, x := newID()
		if x != nil {
			return x
		}
		var now time.Time
		if x = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `INSERT INTO owned_taxonomy.post_category_subscriptions(id,account_id,collection_id,created_at,updated_at)VALUES($1,$2,$3,$4,$4)`, id, actor, c.ID, now); x != nil {
			return x
		}
		out = interactions.CollectionSubscription{ID: id, AccountID: actor, CollectionID: &c.ID}
		created = true
		return audit(ctx, tx, actor, "", c.ID, "collection.subscribe", now)
	})
	if e != nil {
		return interactions.CollectionSubscription{}, false, e
	}
	return
}
func (r *Repository) UnsubscribeCollection(ctx context.Context, actor, publisher, slug string) error {
	return r.collectionTransaction(ctx, actor, publisher, slug, "post.collections.posts.manage", true, func(tx *sql.Tx, c posts.Collection) error {
		var now time.Time
		if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		result, e := tx.ExecContext(ctx, `UPDATE owned_taxonomy.post_category_subscriptions SET deleted_at=$3,updated_at=$3 WHERE account_id=$1 AND collection_id=$2 AND deleted_at IS NULL`, actor, c.ID, now)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		return audit(ctx, tx, actor, "", c.ID, "collection.unsubscribe", now)
	})
}
func (r *Repository) CollectionSubscription(ctx context.Context, actor, publisher, slug string) (out interactions.CollectionSubscription, e error) {
	e = r.collectionTransaction(ctx, actor, publisher, slug, "", true, func(tx *sql.Tx, c posts.Collection) error {
		var x error
		out, x = collectionSubscription(ctx, tx, actor, c.ID)
		return x
	})
	if e != nil {
		return interactions.CollectionSubscription{}, e
	}
	return
}
