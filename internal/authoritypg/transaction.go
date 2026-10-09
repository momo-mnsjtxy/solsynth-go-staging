// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"database/sql"
	"errors"

	"solsynth.local/go-migration/ports/authority"
)

// TransactionGuard joins fresh authority checks to an existing domain SQL
// transaction. It accepts only its configured DB pool; matching DSN strings are
// deliberately insufficient evidence of the same transaction boundary.
type TransactionGuard struct {
	db       *sql.DB
	verifier *authority.Verifier
}

func NewTransactionGuard(store *Repository, verifier *authority.Verifier) *TransactionGuard {
	g := &TransactionGuard{verifier: verifier}
	if store != nil {
		g.db = store.db
	}
	return g
}

type transactionProofKey struct{}
type transactionProof struct {
	guard *TransactionGuard
	proof authority.Principal
}

// Bind retains only the original opaque proof, whose service identity and
// projections are revalidated at the transaction boundary. It grants no access.
func (g *TransactionGuard) Bind(ctx context.Context, p authority.Principal) context.Context {
	return context.WithValue(ctx, transactionProofKey{}, transactionProof{g, p})
}

func (g *TransactionGuard) Authenticate(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) (authority.Principal, error) {
	return g.check(ctx, db, tx, actor, false, nil, true)
}

func (g *TransactionGuard) Authorize(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, permissions ...string) (authority.Principal, error) {
	return g.check(ctx, db, tx, actor, true, permissions, true)
}

// AuthorizeWithGrant requires a current grant for one sensitive permission,
// preserving the same original-proof, pool, isolation and account-lock checks.
func (g *TransactionGuard) AuthorizeWithGrant(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, requiredGrant string, permissions ...string) (authority.Principal, error) {
	if requiredGrant == "" {
		return authority.Principal{}, authority.ErrForbidden
	}
	return g.check(ctx, db, tx, actor, true, permissions, true, requiredGrant)
}

// Preflight checks only canonical authority state in the caller's transaction,
// before domain observations or a sorted multi-account lock set. It acquires no
// account lock and cannot authorize a mutation on its own: the caller MUST run
// Authenticate/Authorize again after its planned locks and before committing.
// The original proof, verifier ownership, pool and isolation checks are shared.
func (g *TransactionGuard) Preflight(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, permissions ...string) (authority.Principal, error) {
	return g.check(ctx, db, tx, actor, len(permissions) > 0, permissions, false)
}

// PreflightWithGrant is the observation-only counterpart of AuthorizeWithGrant.
// Sensitive cross-account reads must establish an explicit current grant before
// even resolving a target. It acquires no account lock; callers must repeat the
// guarded authorization after their sorted account/domain locks.
func (g *TransactionGuard) PreflightWithGrant(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, requiredGrant string, permissions ...string) (authority.Principal, error) {
	if requiredGrant == "" {
		return authority.Principal{}, authority.ErrForbidden
	}
	return g.check(ctx, db, tx, actor, true, permissions, false, requiredGrant)
}

func (g *TransactionGuard) check(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, authorize bool, permissions []string, lock bool, requiredGrant ...string) (authority.Principal, error) {
	if g == nil || g.db == nil || g.db != db || g.verifier == nil || tx == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	envelope, ok := ctx.Value(transactionProofKey{}).(transactionProof)
	if !ok || envelope.guard != g || !authority.ValidID(envelope.proof.Account.ID) || actor != "" && actor != envelope.proof.Account.ID {
		return authority.Principal{}, authority.ErrInvalid
	}
	var isolation string
	if err := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil || isolation != "read committed" {
		return authority.Principal{}, authority.ErrUnavailable
	}
	var a authority.Account
	query := `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, query, envelope.proof.Account.ID).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return authority.Principal{}, authority.ErrInvalid
	}
	if err != nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	// ReadCommitted is the domain adapter contract: a waiter must observe the
	// revocation/policy update that committed before it acquired the account lock.
	// Authority mutators use this same account row lock. The read-only flag denies
	// credential writes through the snapshot, not locks in the enclosing SQL tx.
	snapshot := &transaction{tx: tx, ctx: ctx, account: a, readOnly: true}
	if authorize {
		if len(requiredGrant) == 1 {
			return g.verifier.AuthorizeTransactionWithGrant(ctx, envelope.proof, snapshot, requiredGrant[0], permissions...)
		}
		return g.verifier.AuthorizeTransaction(ctx, envelope.proof, snapshot, permissions...)
	}
	return g.verifier.AuthenticateTransaction(ctx, envelope.proof, snapshot)
}
