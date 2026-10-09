// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
)

// RealmProvider is a trusted canonical storage adapter. SQL arguments are fixed
// aliases/parameter names supplied by Go code, never request values. Realm locks
// precede publisher locks and remain held through the caller's transaction.
type RealmProvider interface {
	PublisherVisibilitySQL(string, string) string
	PublisherMembershipSQL(string, string) string
	PrelockPublisherRealms(context.Context, *sql.Tx, []string) (map[string]string, error)
	ResolveRealmForPublisher(context.Context, *sql.Tx, string, string) (string, error)
}

func (r *Repository) WithRealms(v RealmProvider) *Repository { r.realms = v; return r }

func (r *Repository) publisherVisible(alias, actor string) string {
	if r.realms == nil {
		return `(` + alias + `.type=0 AND ` + PublisherModerationPredicate(alias, actor, alias+`.type=0`) + `)`
	}
	return r.realms.PublisherVisibilitySQL(alias, actor)
}
func (r *Repository) publisherMember(alias, actor string) string {
	if r.realms == nil {
		return alias + `.type=0`
	}
	return r.realms.PublisherMembershipSQL(alias, actor)
}

// VerifyPublisherRealms checks the observed ancestry after publisher locks have
// been acquired. An unexpected reparenting never causes a late realm lock.
func VerifyPublisherRealms(ctx context.Context, tx *sql.Tx, expected map[string]string) error {
	for id, realm := range expected {
		var actual string
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(realm_id::text,'') FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&actual); e != nil {
			return mapped(e)
		}
		if actual != realm {
			return accounts.ErrConflict
		}
	}
	return nil
}

func (r *Repository) lockPublisher(ctx context.Context, tx *sql.Tx, actor, ref string) (accounts.Publisher, error) {
	before, e := lookupPublisher(ctx, tx, ref, false)
	if e != nil {
		return before, e
	}
	var scope map[string]string
	if r.realms != nil {
		scope, e = r.realms.PrelockPublisherRealms(ctx, tx, []string{before.ID})
		if e != nil {
			return accounts.Publisher{}, e
		}
	} else if before.Type != 0 {
		return accounts.Publisher{}, accounts.ErrUnsupported
	}
	p, e := lookupPublisher(ctx, tx, before.ID, true)
	if e != nil {
		return p, e
	}
	if e = VerifyPublisherRealms(ctx, tx, scope); e != nil {
		return accounts.Publisher{}, e
	}
	// Re-resolve name selectors under the locked snapshot without acquiring a
	// different publisher. Names and realm ancestry are immutable in owned APIs.
	actual, e := lookupPublisher(ctx, tx, ref, false)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if actual.ID != p.ID {
		return accounts.Publisher{}, accounts.ErrConflict
	}
	return p, nil
}

func (r *Repository) require(ctx context.Context, q querier, publisher, actor string, role accounts.Role) error {
	if e := require(ctx, q, publisher, actor, role); e != nil {
		return e
	}
	var allowed bool
	e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND $2::text IS NOT NULL AND `+r.publisherMember("p", "$2")+`)`, publisher, actor).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return accounts.ErrForbidden
	}
	return nil
}

func (r *Repository) publisherRead(ctx context.Context, viewer string, fn func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return accounts.ErrUnavailable
	}
	if viewer != "" && !authority.ValidID(viewer) {
		return accounts.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if viewer != "" {
		if _, e = lookupAccount(ctx, tx, viewer); e != nil {
			return e
		}
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) visiblePublisher(ctx context.Context, q querier, viewer, ref string) (accounts.Publisher, error) {
	p, e := lookupPublisher(ctx, q, ref, false)
	if e != nil {
		return p, e
	}
	var allowed bool
	e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND $2::text IS NOT NULL AND `+r.publisherVisible("p", "$2")+`)`, p.ID, viewer).Scan(&allowed)
	if e != nil {
		return accounts.Publisher{}, e
	}
	if !allowed {
		return accounts.Publisher{}, accounts.ErrNotFound
	}
	e = hydrate(ctx, q, &p)
	return p, e
}

func (r *Repository) LookupPublisherForViewer(ctx context.Context, viewer, ref string) (out accounts.Publisher, err error) {
	err = r.publisherRead(ctx, viewer, func(tx *sql.Tx) error {
		var e error
		out, e = r.visiblePublisher(ctx, tx, viewer, ref)
		return e
	})
	return
}
