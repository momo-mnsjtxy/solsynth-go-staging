// SPDX-License-Identifier: AGPL-3.0-only
package signinpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

type PaymentPINStore interface {
	FactorPolicyVersion(context.Context, *sql.DB, *sql.Tx, string) (int64, error)
	FactorConfigID(context.Context, *sql.DB, *sql.Tx, string, string) (string, error)
	MutateFactor(context.Context, *sql.DB, *sql.Tx, string, string, string, string, time.Time) error
}

func (r *Repository) WithPaymentPIN(p PaymentPINStore) *Repository { r.pin = p; return r }
func (t *transaction) PaymentPINVersion() (int64, error) {
	if t.repo.pin == nil {
		return 0, signin.ErrUnsupported
	}
	v, e := t.repo.pin.FactorPolicyVersion(t.ctx, t.repo.db, t.tx, t.canonical.Account().ID)
	return v, pinError(e)
}
func pinError(e error) error {
	if errors.Is(e, wallet.ErrPIN) {
		return signin.ErrInvalid
	}
	if errors.Is(e, wallet.ErrUnavailable) {
		return authority.ErrUnavailable
	}
	if errors.Is(e, wallet.ErrConflict) {
		return signin.ErrConflict
	}
	return e
}
func (t *transaction) MutatePaymentPIN(action string, f signin.Factor, pin string, now time.Time) error {
	if t.repo.pin == nil {
		return signin.ErrUnsupported
	}
	if f.Type != 4 || !authority.ValidID(f.ID) {
		return signin.ErrInput
	}
	actor := t.canonical.Account().ID
	if e := t.repo.pin.MutateFactor(t.ctx, t.repo.db, t.tx, actor, f.ID, action, pin, now); e != nil {
		return pinError(e)
	}
	var record int64
	if e := t.tx.QueryRowContext(t.ctx, `INSERT INTO owned_authority_factor_audit(account_id,factor_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::timestamptz)RETURNING id`, actor, f.ID, "pin."+action, now.UTC().Truncate(time.Microsecond)).Scan(&record); e != nil {
		return e
	}
	return auditcapture.Record(t.ctx, t.tx, actor, "owned_authority_factor_audit", record)
}
func (t *transaction) PaymentPINConfig(action string) (string, error) {
	if t.repo.pin == nil {
		return "", signin.ErrUnsupported
	}
	v, e := t.repo.pin.FactorConfigID(t.ctx, t.repo.db, t.tx, t.canonical.Account().ID, action)
	return v, pinError(e)
}
