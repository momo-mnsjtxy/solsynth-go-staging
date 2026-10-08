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
	return g.check(ctx, db, tx, actor, false, nil)
}

func (g *TransactionGuard) Authorize(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, permissions ...string) (authority.Principal, error) {
	return g.check(ctx, db, tx, actor, true, permissions)
}

func (g *TransactionGuard) check(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, authorize bool, permissions []string) (authority.Principal, error) {
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
	err := tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,is_superuser,disabled,version,created_at FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, envelope.proof.Account.ID).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.Superuser, &a.Disabled, &a.Version, &a.CreatedAt)
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
		return g.verifier.AuthorizeTransaction(ctx, envelope.proof, snapshot, permissions...)
	}
	return g.verifier.AuthenticateTransaction(ctx, envelope.proof, snapshot)
}
