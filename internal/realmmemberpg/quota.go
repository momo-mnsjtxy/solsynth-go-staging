// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"time"
)

// Quota reports the grant used by canonical realm creation, with configured
// account progression and subscription perk projections. It does not infer the
// imported upstream ResourceQuotaCalculator or manufacture creation grants.
func (r *Repository) Quota(ctx context.Context, actor string) (out m.Quota, e error) {
	out.Records = []m.QuotaRecord{}
	defer func() { e = mapped(e) }()
	if r == nil {
		return out, m.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor); e != nil {
		return
	}
	if e = lockAccounts(ctx, tx, []string{actor}); e != nil {
		return
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return
	}
	var grant int64
	var starts time.Time
	var expires *time.Time
	e = tx.QueryRowContext(ctx, `SELECT total,starts_at,expires_at FROM owned_realm_quota WHERE account_id=$1::uuid FOR SHARE NOWAIT`, actor).Scan(&grant, &starts, &expires)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return
	}
	hasGrant := e == nil
	e = nil
	out.Level, e = progressionpg.ConfiguredLevelInTransaction(ctx, tx, actor)
	if errors.Is(e, sql.ErrNoRows) {
		e = m.ErrQuotaPolicy
	}
	if e != nil {
		return
	}
	p, ok := r.boosts.(interface {
		QuotaPerkInTransaction(context.Context, *sql.Tx, string, time.Time) (int, error)
	})
	if !ok {
		return out, m.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,slug,name FROM owned_realms WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY created_at,id LIMIT 10001`, actor)
	if e != nil {
		return
	}
	for rows.Next() {
		var x m.QuotaRecord
		if e = rows.Scan(&x.ID, &x.Slug, &x.Name); e != nil {
			rows.Close()
			return
		}
		out.Records = append(out.Records, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	if len(out.Records) > 10000 {
		return out, m.ErrUnavailable
	}
	out.Used = int64(len(out.Records))
	out.PerkLevel, e = p.QuotaPerkInTransaction(ctx, tx, actor, r.cfg.Now().UTC())
	if e != nil {
		return
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return
	}
	// Use database wall time for the existing grant contract, including expiry
	// during a preceding wait. The read itself never consumes a slot.
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return
	}
	if hasGrant && !starts.After(now) && (expires == nil || expires.After(now)) {
		out.Total = grant
	}
	out.Remaining = max(int64(0), out.Total-out.Used)
	return out, tx.Commit()
}
