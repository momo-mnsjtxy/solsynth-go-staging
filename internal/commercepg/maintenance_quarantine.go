// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

//go:embed maintenance_schema.sql
var MaintenanceSchema string

var errMaintenanceAdvanced = errors.New("maintenance candidate advanced")

func skipMaintenance(tx *sql.Tx) (bool, error) {
	if e := tx.Commit(); e != nil {
		return false, e
	}
	return false, errMaintenanceAdvanced
}

type subscriptionProjection struct {
	ID, AccountID, Identifier string
	Status                    c.SubscriptionStatus
	BegunAt, EndedAt          time.Time
	RenewalAt                 *time.Time
	Payer                     sql.NullString
	Renew                     bool
	Days                      int
	Document                  []byte
}

func lockedSubscriptionProjection(ctx context.Context, tx *sql.Tx, id string) (v subscriptionProjection, e error) {
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,identifier,status,begun_at,ended_at,renewal_at,payer_wallet_id::text,auto_renew,cycle_days,document FROM commerce_v1.subscriptions WHERE id=$1::uuid FOR UPDATE`, id).Scan(&v.ID, &v.AccountID, &v.Identifier, &v.Status, &v.BegunAt, &v.EndedAt, &v.RenewalAt, &v.Payer, &v.Renew, &v.Days, &v.Document)
	return v, notFound(e)
}
func samePGTime(a, b time.Time) bool {
	return a.UTC().Truncate(time.Microsecond).Equal(b.UTC().Truncate(time.Microsecond))
}
func samePGOptionalTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && samePGTime(*a, *b)
}
func (v subscriptionProjection) validate() (s c.Subscription, reason string) {
	if e := json.Unmarshal(v.Document, &s); e != nil {
		return s, "invalid_document"
	}
	payer := ""
	if s.PaymentDetails.PayerWalletID != nil {
		payer = *s.PaymentDetails.PayerWalletID
	}
	if s.ID != v.ID || s.AccountID != v.AccountID || s.Identifier != v.Identifier || s.Status != v.Status || !samePGTime(s.BegunAt, v.BegunAt) || !samePGTime(s.EndedAt, v.EndedAt) || !samePGOptionalTime(s.RenewalAt, v.RenewalAt) || (s.PaymentDetails.PayerWalletID != nil) != v.Payer.Valid || payer != v.Payer.String {
		return s, "projection_mismatch"
	}
	return s, ""
}
func quarantineSubscription(ctx context.Context, tx *sql.Tx, v subscriptionProjection, reason string, now time.Time) error {
	hash := sha256.Sum256(v.Document)
	_, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.subscription_maintenance_quarantine(subscription_id,reason,document_sha256,first_seen_at,last_seen_at)VALUES($1::uuid,$2::text,$3::text,$4::timestamptz,$4::timestamptz)ON CONFLICT(subscription_id)DO UPDATE SET reason=EXCLUDED.reason,document_sha256=EXCLUDED.document_sha256,last_seen_at=GREATEST(commerce_v1.subscription_maintenance_quarantine.last_seen_at,EXCLUDED.last_seen_at),resolved_at=NULL,observations=commerce_v1.subscription_maintenance_quarantine.observations+1`, v.ID, reason, hex.EncodeToString(hash[:]), now.UTC().Truncate(time.Microsecond))
	return e
}

// MaintenanceQuarantine contains diagnostic metadata, never copied payment
// credentials, full documents, prices, account profile data or provider tokens.
type MaintenanceQuarantine struct {
	SubscriptionID, Reason, DocumentSHA256 string
	FirstSeenAt, LastSeenAt                time.Time
	ResolvedAt                             *time.Time
	Observations                           int64
}

// SubscriptionMaintenanceFailures is an explicit trusted operator inspection
// seam, not an unauthenticated HTTP route. after is an optional UUID cursor.
func (r *Repository) SubscriptionMaintenanceFailures(ctx context.Context, after string, limit int) ([]MaintenanceQuarantine, error) {
	if r == nil || r.db == nil {
		return nil, c.ErrUnavailable
	}
	if limit < 1 || limit > 1000 || after != "" && !wallet.ValidID(after) {
		return nil, c.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT subscription_id::text,reason,document_sha256,first_seen_at,last_seen_at,resolved_at,observations FROM commerce_v1.subscription_maintenance_quarantine WHERE resolved_at IS NULL AND ($1::text='' OR subscription_id>NULLIF($1::text,'')::uuid) ORDER BY subscription_id LIMIT $2::integer`, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []MaintenanceQuarantine{}
	for rows.Next() {
		var v MaintenanceQuarantine
		if e = rows.Scan(&v.SubscriptionID, &v.Reason, &v.DocumentSHA256, &v.FirstSeenAt, &v.LastSeenAt, &v.ResolvedAt, &v.Observations); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RetrySubscriptionMaintenance clears only a coherent, manually repaired
// subscription. It neither repairs fields nor charges/activates anything. The
// next bounded ProcessDue rechecks the complete standing-renewal policy.
func (r *Repository) RetrySubscriptionMaintenance(ctx context.Context, id string) error {
	if !wallet.ValidID(id) {
		return c.ErrInvalid
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var actor string
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM commerce_v1.subscriptions WHERE id=$1::uuid`, id).Scan(&actor); e != nil {
		return notFound(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor); e != nil {
		return e
	}
	v, e := lockedSubscriptionProjection(ctx, tx, id)
	if e != nil {
		return e
	}
	if v.AccountID != actor {
		return c.ErrRetry
	}
	if _, reason := v.validate(); reason != "" {
		return c.ErrConflict
	}
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.subscription_maintenance_quarantine SET resolved_at=GREATEST(last_seen_at,$2::timestamptz) WHERE subscription_id=$1::uuid AND resolved_at IS NULL`, id, r.now().UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
