// SPDX-License-Identifier: AGPL-3.0-only
package accounthistorypg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"math"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/history"
	"strings"
	"time"
	"unicode/utf8"
)

// Credit is a trusted policy command, never a client-selected reward event.
// Domain owners must supply an immutable canonical provenance key and hold the
// target account lock; explicit administration uses credits.records.manage.
type Credit struct {
	Record     string     `json:"record"`
	ReasonType string     `json:"reason_type"`
	Reason     string     `json:"reason"`
	Delta      float64    `json:"delta"`
	ExpiresAt  *time.Time `json:"expired_at"`
}

func ValidateCredit(v Credit) error {
	if !authority.ValidID(v.Record) || math.IsInf(v.Delta, 0) || math.IsNaN(v.Delta) || v.Delta == 0 || math.Abs(v.Delta) > 1000000 {
		return history.ErrInvalid
	}
	for _, s := range []string{v.ReasonType, v.Reason} {
		if strings.TrimSpace(s) == "" || len([]rune(s)) > 1024 || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
			return history.ErrInvalid
		}
	}
	return nil
}
func GrantCreditInTransaction(ctx context.Context, tx *sql.Tx, account, policy string, v Credit, now time.Time) (out history.CreditRecord, e error) {
	if tx == nil || !authority.ValidID(account) || len(policy) < 1 || len(policy) > 2048 || strings.ContainsRune(policy, 0) {
		return out, history.ErrInvalid
	}
	if e = ValidateCredit(v); e != nil {
		return
	}
	if v.ExpiresAt != nil && (!v.ExpiresAt.After(now) || v.ExpiresAt.Year() > 9999) {
		return out, history.ErrInvalid
	}
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE id=$1::uuid FOR UPDATE`, account).Scan(&active)
	if e != nil {
		return
	}
	if !active {
		return out, authority.ErrDisabled
	}
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		return
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	id := s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_account_credit_records(id,account_id,reason_type,reason,delta,created_at,expired_at,source_kind,source_ref,source_record)VALUES($1::uuid,$2::uuid,$3::text,$4::text,$5::double precision,$6::timestamptz,$7::timestamptz,'owned-policy',$8::text,$9::text)ON CONFLICT(account_id,source_kind,source_ref,source_record)DO NOTHING`, id, account, v.ReasonType, v.Reason, v.Delta, now, v.ExpiresAt, policy, v.Record)
	if e != nil {
		return
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,reason_type,reason,delta,created_at,expired_at FROM owned_account_credit_records WHERE account_id=$1::uuid AND source_kind='owned-policy' AND source_ref=$2::text AND source_record=$3::text`, account, policy, v.Record).Scan(&out.ID, &out.AccountID, &out.ReasonType, &out.Reason, &out.Delta, &out.CreatedAt, &out.ExpiredAt)
	if e != nil {
		return
	}
	out.UpdatedAt = out.CreatedAt
	sameExpiry := out.ExpiredAt == nil && v.ExpiresAt == nil || out.ExpiredAt != nil && v.ExpiresAt != nil && out.ExpiredAt.Equal(*v.ExpiresAt)
	if out.ReasonType != v.ReasonType || out.Reason != v.Reason || out.Delta != v.Delta || !sameExpiry {
		return history.CreditRecord{}, history.ErrInvalid
	}
	return
}
