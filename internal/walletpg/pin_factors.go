// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

func (p *PINVerifier) factorBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if p == nil || p.db == nil || p.db != db || tx == nil || p.guard == nil {
		return accounts.ErrUnavailable
	}
	_, e := p.guard.Authenticate(ctx, db, tx, actor)
	return e
}
func (p *PINVerifier) FactorPolicyVersion(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) (int64, error) {
	if e := p.factorBoundary(ctx, db, tx, actor); e != nil {
		return 0, e
	}
	var version int64
	e := tx.QueryRowContext(ctx, `SELECT version FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&version)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, nil
	}
	return version, e
}
func (p *PINVerifier) FactorConfigID(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, action string) (string, error) {
	if e := p.factorBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if action == "create" {
		if _, ok := p.peppers[p.enrollmentPepperID]; !ok {
			return "", accounts.ErrUnavailable
		}
		return p.enrollmentPepperID, nil
	}
	var id sql.NullString
	e := tx.QueryRowContext(ctx, `SELECT pepper_id FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&id)
	if e != nil {
		return "", e
	}
	if action == "enable" {
		if _, ok := p.peppers[id.String]; !ok {
			return "", accounts.ErrUnavailable
		}
	}
	return id.String, nil
}
func (p *PINVerifier) MutateFactor(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, fid, action, pin string, now time.Time) error {
	if e := p.factorBoundary(ctx, db, tx, actor); e != nil {
		return e
	}
	var typ int
	var enabled sql.NullTime
	if e := tx.QueryRowContext(ctx, `SELECT type,enabled_at FROM owned_authority_factors WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, fid, actor).Scan(&typ, &enabled); e != nil {
		return e
	}
	if typ != 4 {
		return accounts.ErrInvalid
	}
	now = now.UTC().Truncate(time.Microsecond)
	var linked sql.NullString
	var salt, expected []byte
	var pepperID sql.NullString
	var iterations sql.NullInt64
	var locked sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT factor_id::text,salt,pin_mac,pepper_id,iterations,locked_until FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&linked, &salt, &expected, &pepperID, &iterations, &locked)
	missing := errors.Is(e, sql.ErrNoRows)
	if e != nil && !missing {
		return e
	}
	if action == "create" {
		if linked.Valid || !enabled.Valid || len(pin) != 6 || !pinSyntax(pin) {
			return accounts.ErrConflict
		}
		pepper, ok := p.peppers[p.enrollmentPepperID]
		if !ok {
			return accounts.ErrUnavailable
		}
		salt = make([]byte, 32)
		if _, e = rand.Read(salt); e != nil {
			return e
		}
		mac, e := DerivePINMAC(pin, salt, pepper)
		if e != nil {
			return e
		}
		defer clear(mac)
		result, e := tx.ExecContext(ctx, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,salt,pin_mac,iterations,pepper_id,disabled,updated_at,factor_id)VALUES($1::uuid,true,$2::bytea,$3::bytea,$4::integer,$5::text,false,$6::timestamptz,$7::uuid)ON CONFLICT(account_id)DO UPDATE SET pin_required=true,salt=EXCLUDED.salt,pin_mac=EXCLUDED.pin_mac,iterations=EXCLUDED.iterations,pepper_id=EXCLUDED.pepper_id,disabled=false,failed_attempts=0,locked_until=NULL,updated_at=EXCLUDED.updated_at,factor_id=EXCLUDED.factor_id,version=wallet_v1.payment_policies.version+1 WHERE wallet_v1.payment_policies.factor_id IS NULL`, actor, salt, mac, PINIterations, p.enrollmentPepperID, now, fid)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return accounts.ErrConflict
		}
	} else {
		if missing || !linked.Valid || linked.String != fid {
			return accounts.ErrConflict
		}
		switch action {
		case "enable":
			if !enabled.Valid || len(pin) != 6 || !pinSyntax(pin) || locked.Valid && now.Before(locked.Time) {
				return accounts.ErrPIN
			}
			pepper, ok := p.peppers[pepperID.String]
			if !ok || iterations.Int64 != PINIterations || len(salt) != 32 || len(expected) != 32 {
				return accounts.ErrUnavailable
			}
			candidate, deriveErr := DerivePINMAC(pin, salt, pepper)
			if deriveErr != nil {
				return deriveErr
			}
			valid := subtle.ConstantTimeCompare(candidate, expected) == 1
			clear(candidate)
			if !valid {
				return accounts.ErrPIN
			}
			_, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_policies SET disabled=false,failed_attempts=0,locked_until=NULL,version=version+1,updated_at=$2::timestamptz WHERE account_id=$1::uuid`, actor, now)
		case "disable":
			if enabled.Valid {
				return accounts.ErrConflict
			}
			_, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_policies SET disabled=true,version=version+1,updated_at=$2::timestamptz WHERE account_id=$1::uuid`, actor, now)
		case "delete":
			_, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_policies SET disabled=true,pin_required=false,salt=NULL,pin_mac=NULL,iterations=NULL,pepper_id=NULL,factor_id=NULL,failed_attempts=0,locked_until=NULL,version=version+1,updated_at=$2::timestamptz WHERE account_id=$1::uuid`, actor, now)
		default:
			return accounts.ErrInvalid
		}
		if e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_authorizations SET consumed_at=$2::timestamptz WHERE account_id=$1::uuid AND consumed_at IS NULL`, actor, now)
	return e
}
