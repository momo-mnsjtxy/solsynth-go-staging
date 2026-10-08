// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

const PINIterations = 600000

// Construction performs no provisioning or authority grant. Canonical factor
// methods require the exact guarded transaction. Missing policy denies.
type PINVerifier struct {
	db                 *sql.DB
	peppers            map[string][]byte
	now                func() time.Time
	guard              *authoritypg.TransactionGuard
	enrollmentPepperID string
}

func NewPINVerifier(db *sql.DB, peppers map[string][]byte) *PINVerifier {
	p := &PINVerifier{db: db, peppers: map[string][]byte{}, now: time.Now}
	for id, key := range peppers {
		if len(id) > 0 && len(id) <= 64 && len(key) >= 32 {
			p.peppers[id] = append([]byte(nil), key...)
		}
	}
	return p
}
func (p *PINVerifier) WithAuthority(g *authoritypg.TransactionGuard) *PINVerifier {
	p.guard = g
	return p
}
func (p *PINVerifier) WithClock(now func() time.Time) *PINVerifier {
	if now != nil {
		p.now = now
	}
	return p
}
func (p *PINVerifier) WithEnrollmentPepper(id string) *PINVerifier {
	p.enrollmentPepperID = id
	return p
}
func DerivePINMAC(pin string, salt, pepper []byte) ([]byte, error) {
	if !pinSyntax(pin) || len(salt) != 32 || len(pepper) < 32 {
		return nil, accounts.ErrPIN
	}
	derived, e := pbkdf2.Key(sha256.New, pin, salt, PINIterations, 32)
	if e != nil {
		return nil, e
	}
	defer clear(derived)
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte("wallet-pin-v1\x00"))
	_, _ = mac.Write(salt)
	_, _ = mac.Write(derived)
	return mac.Sum(nil), nil
}
func pinSyntax(pin string) bool {
	if len(pin) < 6 || len(pin) > 12 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func (p *PINVerifier) Status(ctx context.Context, principal accounts.Principal) (accounts.PINStatus, error) {
	if p == nil || p.db == nil {
		return accounts.PINStatus{}, accounts.ErrUnavailable
	}
	if !accounts.ValidID(principal.AccountID) || principal.SessionID == "" {
		return accounts.PINStatus{}, accounts.ErrUnauthorized
	}
	var required, disabled bool
	e := p.db.QueryRowContext(ctx, `SELECT pin_required,disabled FROM wallet_v1.payment_policies WHERE account_id=$1`, principal.AccountID).Scan(&required, &disabled)
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.PINStatus{}, accounts.ErrUnavailable
	}
	if e != nil {
		return accounts.PINStatus{}, e
	}
	if disabled {
		return accounts.PINStatus{HasPIN: false, ValidationRequired: true}, nil
	}
	return accounts.PINStatus{HasPIN: required, ValidationRequired: required}, nil
}
func (p *PINVerifier) VerifyPayment(ctx context.Context, principal accounts.Principal, pin string) error {
	if p == nil || p.db == nil {
		return accounts.ErrUnavailable
	}
	if !accounts.ValidID(principal.AccountID) || principal.SessionID == "" {
		return accounts.ErrUnauthorized
	}
	tx, e := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	result := p.verifyPolicy(ctx, tx, principal.AccountID, pin)
	if result != nil && !errors.Is(result, accounts.ErrPIN) {
		return result
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return result
}

// Callers deliberately commit failed verification so later rollback cannot
// erase the online attempt budget.
func (p *PINVerifier) verifyPolicy(ctx context.Context, tx *sql.Tx, actor, pin string) error {
	var required, disabled bool
	var salt, expected []byte
	var iterations sql.NullInt64
	var pepperID sql.NullString
	var failures int
	var locked sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT pin_required,salt,pin_mac,iterations,pepper_id,failed_attempts,locked_until,disabled FROM wallet_v1.payment_policies WHERE account_id=$1::uuid FOR UPDATE`, actor).Scan(&required, &salt, &expected, &iterations, &pepperID, &failures, &locked, &disabled)
	if errors.Is(e, sql.ErrNoRows) || disabled {
		return accounts.ErrUnavailable
	}
	if e != nil {
		return e
	}
	if !required {
		return nil
	}
	now := p.now().UTC()
	if locked.Valid && now.Before(locked.Time) {
		return accounts.ErrPIN
	}
	if locked.Valid {
		failures = 0
	}
	pepper, ok := p.peppers[pepperID.String]
	if !ok || iterations.Int64 != PINIterations || len(salt) != 32 || len(expected) != 32 {
		return accounts.ErrUnavailable
	}
	valid := false
	if pinSyntax(pin) {
		candidate, x := DerivePINMAC(pin, salt, pepper)
		if x != nil {
			return x
		}
		valid = subtle.ConstantTimeCompare(candidate, expected) == 1
		clear(candidate)
	}
	if valid {
		_, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_policies SET failed_attempts=0,locked_until=NULL,updated_at=$2::timestamptz WHERE account_id=$1::uuid`, actor, now)
		return e
	}
	failures++
	if failures > 5 {
		failures = 5
	}
	var until any
	if failures == 5 {
		until = now.Add(15 * time.Minute)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE wallet_v1.payment_policies SET failed_attempts=$2::integer,locked_until=$3::timestamptz,updated_at=$4::timestamptz WHERE account_id=$1::uuid`, actor, failures, until, now); e != nil {
		return e
	}
	return accounts.ErrPIN
}

// Trusted standalone provisioning only. Canonical user lifecycle instead uses
// guarded MutateFactor, and this hook cannot overwrite a linked factor.
func (p *PINVerifier) ProvisionPIN(ctx context.Context, accountID, pin, pepperID string) error {
	if p == nil || p.db == nil {
		return accounts.ErrUnavailable
	}
	if !accounts.ValidID(accountID) {
		return accounts.ErrInvalid
	}
	pepper, ok := p.peppers[pepperID]
	if !ok {
		return accounts.ErrUnavailable
	}
	salt := make([]byte, 32)
	if _, e := rand.Read(salt); e != nil {
		return e
	}
	mac, e := DerivePINMAC(pin, salt, pepper)
	if e != nil {
		return e
	}
	result, e := p.db.ExecContext(ctx, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,salt,pin_mac,iterations,pepper_id,updated_at) VALUES($1::uuid,true,$2::bytea,$3::bytea,$4::integer,$5::text,$6::timestamptz) ON CONFLICT(account_id) DO UPDATE SET pin_required=true,salt=EXCLUDED.salt,pin_mac=EXCLUDED.pin_mac,iterations=EXCLUDED.iterations,pepper_id=EXCLUDED.pepper_id,failed_attempts=0,locked_until=NULL,disabled=false,updated_at=EXCLUDED.updated_at,version=wallet_v1.payment_policies.version+1 WHERE wallet_v1.payment_policies.factor_id IS NULL`, accountID, salt, mac, PINIterations, pepperID, p.now().UTC())
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
	return nil
}
