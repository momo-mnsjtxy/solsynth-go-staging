// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

// StoredSessionEligible is a read-only reconciliation observation in the same
// caller transaction. It cannot authenticate a request or authorize a new grant.
// Callers retain their canonical account/domain locks when acting on its result.
func (g *TransactionGuard) StoredSessionEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, session string, version, epoch int64, permissions ...string) (bool, error) {
	return g.storedSessionEligible(ctx, db, tx, actor, session, version, epoch, false, permissions)
}

// StoredAuthenticationEligible only reconciles existing authentication-based
// admissions; the caller must also enforce its canonical domain membership.
func (g *TransactionGuard) StoredAuthenticationEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, session string, version, epoch int64) (bool, error) {
	return g.storedSessionEligible(ctx, db, tx, actor, session, version, epoch, true, nil)
}

// StoredConnectionEligible reconciles transport leases through the same OAuth
// app/consent/parent binding used by the original credential verifier.
func (g *TransactionGuard) StoredConnectionEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, session string, version, epoch int64) (bool, error) {
	return g.storedSessionEligible(ctx, db, tx, actor, session, version, epoch, true, nil, true)
}

func (g *TransactionGuard) storedSessionEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, session string, version, epoch int64, authenticationOnly bool, permissions []string, connection ...bool) (bool, error) {
	if g == nil || g.db == nil || g.db != db || g.verifier == nil || tx == nil {
		return false, authority.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return false, nil
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil || isolation != "read committed" {
		return false, authority.ErrUnavailable
	}
	var a authority.Account
	e := tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var at time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&at); e != nil {
		return false, e
	}
	if len(connection) == 1 && connection[0] {
		return g.verifier.RecordedConnectionEligible(&transaction{tx: tx, ctx: ctx, account: a, readOnly: true}, session, version, epoch, at.UTC())
	}
	if authenticationOnly {
		return g.verifier.RecordedAuthenticationEligible(&transaction{tx: tx, ctx: ctx, account: a, readOnly: true}, session, version, epoch, at.UTC())
	}
	return g.verifier.RecordedSessionEligible(&transaction{tx: tx, ctx: ctx, account: a, readOnly: true}, session, version, epoch, at.UTC(), permissions...)
}
