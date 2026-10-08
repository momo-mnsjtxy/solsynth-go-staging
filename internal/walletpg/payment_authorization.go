// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

type PaymentAuthorization interface {
	LockPayment(context.Context, *sql.DB, *sql.Tx, string, string, string) error
	CheckPayment(context.Context, *sql.DB, *sql.Tx, string, string, string) error
}

func (r *Repository) WithPaymentAuthorization(p PaymentAuthorization) *Repository {
	r.payments = p
	return r
}

type paymentProofKey struct{}
type paymentProof struct {
	provider *PINVerifier
	token    [32]byte
}

func paymentOperation(purpose, binding string) bool {
	if purpose != accounts.PaymentTransfer && purpose != accounts.PaymentOrder && purpose != accounts.PaymentFundCreate && purpose != accounts.PaymentFundContribute {
		return false
	}
	b, e := hex.DecodeString(binding)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == binding
}
func paymentPermission(purpose string) string {
	if purpose == accounts.PaymentFundCreate || purpose == accounts.PaymentFundContribute {
		return accounts.PermissionFunds
	}
	if purpose == accounts.PaymentOrder {
		return "orders.pay"
	}
	return accounts.PermissionTransactions
}

// The canonical host always installs this explicit atomic composition. The
// standalone PINVerifier retains its original error-only legacy interface.
type AtomicPaymentVerifier struct{ *PINVerifier }

func (p *PINVerifier) Atomic() *AtomicPaymentVerifier { return &AtomicPaymentVerifier{p} }
func (a *AtomicPaymentVerifier) PreparePayment(ctx context.Context, principal accounts.Principal, pin, purpose, binding string) (context.Context, error) {
	if a == nil {
		return ctx, accounts.ErrUnavailable
	}
	p := a.PINVerifier
	if p == nil || p.db == nil || p.guard == nil {
		return ctx, accounts.ErrUnavailable
	}
	if !paymentOperation(purpose, binding) || len(pin) > 256 {
		return ctx, accounts.ErrPIN
	}
	tx, e := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return ctx, e
	}
	defer tx.Rollback()
	fresh, e := p.guard.Authorize(ctx, p.db, tx, principal.AccountID, paymentPermission(purpose))
	if e != nil {
		return ctx, e
	}
	if fresh.Session.ID != principal.SessionID {
		return ctx, accounts.ErrUnauthorized
	}
	result := p.verifyPolicy(ctx, tx, principal.AccountID, pin)
	if result != nil {
		if errors.Is(result, accounts.ErrPIN) {
			if e = tx.Commit(); e != nil {
				return ctx, e
			}
		}
		return ctx, result
	}
	var version int64
	var linked sql.NullString
	if e = tx.QueryRowContext(ctx, `SELECT version,factor_id::text FROM wallet_v1.payment_policies WHERE account_id=$1::uuid`, principal.AccountID).Scan(&version, &linked); e != nil {
		return ctx, e
	}
	if linked.Valid {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid AND type=4 AND enabled_at IS NOT NULL)`, linked.String, principal.AccountID).Scan(&active); e != nil {
			return ctx, e
		}
		if !active {
			return ctx, accounts.ErrPIN
		}
	}
	now := p.now().UTC().Truncate(time.Microsecond)
	if _, e = tx.ExecContext(ctx, `DELETE FROM wallet_v1.payment_authorizations WHERE hash IN(SELECT hash FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND(expires_at<=$2::timestamptz OR consumed_at IS NOT NULL)ORDER BY created_at LIMIT 100)`, principal.AccountID, now); e != nil {
		return ctx, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid`, principal.AccountID).Scan(&count); e != nil {
		return ctx, e
	}
	if count >= 32 {
		return ctx, accounts.ErrPIN
	}
	var raw [32]byte
	if _, e = rand.Read(raw[:]); e != nil {
		return ctx, e
	}
	hash := sha256.Sum256(raw[:])
	if _, e = tx.ExecContext(ctx, `INSERT INTO wallet_v1.payment_authorizations(hash,account_id,session_id,session_epoch,account_version,policy_version,purpose,binding,created_at,expires_at)VALUES($1::bytea,$2::uuid,$3::uuid,$4::bigint,$5::bigint,$6::bigint,$7::text,$8::text,$9::timestamptz,$10::timestamptz)`, hash[:], principal.AccountID, fresh.Session.ID, fresh.Session.Epoch, fresh.Account.Version, version, purpose, binding, now, now.Add(2*time.Minute)); e != nil {
		return ctx, e
	}
	if _, e = p.guard.Authorize(ctx, p.db, tx, principal.AccountID, paymentPermission(purpose)); e != nil {
		return ctx, e
	}
	if e = tx.Commit(); e != nil {
		return ctx, e
	}
	return context.WithValue(ctx, paymentProofKey{}, paymentProof{p, raw}), nil
}
func (p *PINVerifier) CheckPayment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purpose, binding string) error {
	return p.checkPayment(ctx, db, tx, actor, purpose, binding, true)
}
func (p *PINVerifier) LockPayment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purpose, binding string) error {
	return p.checkPayment(ctx, db, tx, actor, purpose, binding, false)
}
func (p *PINVerifier) checkPayment(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purpose, binding string, consume bool) error {
	if p == nil || p.db == nil || p.db != db || tx == nil || p.guard == nil {
		return accounts.ErrUnavailable
	}
	proof, ok := ctx.Value(paymentProofKey{}).(paymentProof)
	if !ok || proof.provider != p || !paymentOperation(purpose, binding) {
		return accounts.ErrPIN
	}
	fresh, e := p.guard.Authorize(ctx, db, tx, actor, paymentPermission(purpose))
	if e != nil {
		return e
	}
	var version int64
	var disabled bool
	var locked sql.NullTime
	var linked sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT version,disabled,locked_until,factor_id::text FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&version, &disabled, &locked, &linked)
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.ErrPIN
	}
	if e != nil {
		return e
	}
	now := p.now().UTC().Truncate(time.Microsecond)
	if disabled || locked.Valid && now.Before(locked.Time) {
		return accounts.ErrPIN
	}
	if linked.Valid {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid AND type=4 AND enabled_at IS NOT NULL)`, linked.String, actor).Scan(&active); e != nil {
			return e
		}
		if !active {
			return accounts.ErrPIN
		}
	}
	hash := sha256.Sum256(proof.token[:])
	if !consume {
		var found bool
		e = tx.QueryRowContext(ctx, `SELECT true FROM wallet_v1.payment_authorizations WHERE hash=$1::bytea AND account_id=$2::uuid AND session_id=$3::uuid AND session_epoch=$4::bigint AND account_version=$5::bigint AND policy_version=$6::bigint AND purpose=$7::text AND binding=$8::text AND consumed_at IS NULL AND created_at<=$9::timestamptz AND expires_at>$9::timestamptz FOR UPDATE`, hash[:], actor, fresh.Session.ID, fresh.Session.Epoch, fresh.Account.Version, version, purpose, binding, now).Scan(&found)
		if errors.Is(e, sql.ErrNoRows) {
			return accounts.ErrPIN
		}
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE wallet_v1.payment_authorizations SET consumed_at=$9::timestamptz WHERE hash=$1::bytea AND account_id=$2::uuid AND session_id=$3::uuid AND session_epoch=$4::bigint AND account_version=$5::bigint AND policy_version=$6::bigint AND purpose=$7::text AND binding=$8::text AND consumed_at IS NULL AND created_at<=$9::timestamptz AND expires_at>$9::timestamptz`, hash[:], actor, fresh.Session.ID, fresh.Session.Epoch, fresh.Account.Version, version, purpose, binding, now)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return accounts.ErrPIN
	}
	return nil
}
