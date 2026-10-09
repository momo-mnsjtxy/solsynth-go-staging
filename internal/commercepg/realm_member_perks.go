// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// RealmMemberPerkLevel is a separate owned maintenance boundary. Boost checkout
// keeps its existing actor-only boundary unchanged. The requesting owner must
// currently manage members, and the target must currently belong to this realm.
// Secondary locks are NOWAIT: a caller retries the whole transaction with fresh
// time rather than awarding against an entitlement observed before a wait.
func (r *Repository) RealmMemberPerkLevel(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, realm, account string, levels map[string]int, at time.Time) (int, error) {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil || levels == nil || !authority.ValidID(realm) || !authority.ValidID(account) {
		return 0, c.ErrUnavailable
	}
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "realms.members.manage"); e != nil {
		return 0, e
	}
	var active bool
	if e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid FOR UPDATE OF a NOWAIT`, account).Scan(&active); e != nil {
		return 0, e
	}
	if !active {
		return 0, c.ErrForbidden
	}
	var id string
	e := tx.QueryRowContext(ctx, `SELECT r.id::text FROM owned_realms r WHERE r.id=$1::uuid AND r.account_id=$2::uuid AND r.deleted_at IS NULL AND EXISTS(SELECT 1 FROM owned_realm_members m WHERE m.realm_id=r.id AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.role=100) AND EXISTS(SELECT 1 FROM owned_realm_members m WHERE m.realm_id=r.id AND m.account_id=$3::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL) FOR SHARE OF r NOWAIT`, realm, actor, account).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, c.ErrForbidden
	}
	if e != nil {
		return 0, e
	}
	return r.realmAccountPerk(ctx, tx, account, levels, at)
}

// RealmProfilePerkLevel authenticates the actor-only profile boundary. It does
// not require ownership of the realm or grant member-management rights.
func (r *Repository) RealmProfilePerkLevel(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, levels map[string]int, at time.Time) (int, error) {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil || levels == nil {
		return 0, c.ErrUnavailable
	}
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "realms.members.manage"); e != nil {
		return 0, e
	}
	return r.realmAccountPerk(ctx, tx, actor, levels, at)
}
func (r *Repository) realmAccountPerk(ctx context.Context, tx *sql.Tx, account string, levels map[string]int, at time.Time) (int, error) {
	if len(levels) == 0 {
		return 0, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text,identifier,status,begun_at,ended_at,renewal_at,payer_wallet_id::text,auto_renew,cycle_days,document FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND status=1 AND ended_at>$2::timestamptz ORDER BY id LIMIT 1001 FOR UPDATE NOWAIT`, account, at)
	if e != nil {
		return 0, e
	}
	subs := []subscriptionProjection{}
	for rows.Next() {
		var p subscriptionProjection
		if e = rows.Scan(&p.ID, &p.AccountID, &p.Identifier, &p.Status, &p.BegunAt, &p.EndedAt, &p.RenewalAt, &p.Payer, &p.Renew, &p.Days, &p.Document); e != nil {
			rows.Close()
			return 0, e
		}
		subs = append(subs, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if len(subs) > 1000 {
		return 0, c.ErrUnavailable
	}
	type eligible struct {
		level      int
		begin, end time.Time
	}
	candidates := []eligible{}
	for _, p := range subs {
		s, reason := p.validate()
		if reason != "" {
			return 0, c.ErrConflict
		}
		level := levels[s.Identifier]
		if level < 1 || level > 3 || s.AccountID != account || s.AppIdentifier != nil || !s.IsActive || s.IsTesting || s.Status != c.SubscriptionActive || !s.EndedAt.After(s.BegunAt) {
			continue
		}
		var raw []byte
		e = tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.definitions WHERE identifier=$1::text AND enabled FOR SHARE NOWAIT`, s.Identifier).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return 0, e
		}
		var d c.Definition
		if json.Unmarshal(raw, &d) != nil || d.Identifier != s.Identifier || d.BasePrice.Sign() <= 0 || d.CycleDurationDays < 1 || d.CycleDurationDays > 366 || d.Currency == "" {
			return 0, c.ErrUnavailable
		}
		if d.AppIdentifier != nil || d.ProductID != "" {
			continue
		}
		// Keep queued paid cycles until the final observation: a subscription or
		// definition table wait may cross their start as well as their expiry.
		candidates = append(candidates, eligible{level, s.BegunAt, s.EndedAt})
	}
	// Even NOWAIT row locks can encounter unrelated table/trigger delays. Finish
	// at a fresh database wall clock after every subscription/definition query.
	// The supplied policy clock may advance synthetic/maintenance time, but may
	// never move entitlement time behind the current database observation.
	var fresh time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&fresh); e != nil {
		return 0, e
	}
	if at.After(fresh) {
		fresh = at
	}
	if current := r.now().UTC(); current.After(fresh) {
		fresh = current
	}
	result := 0
	for _, candidate := range candidates {
		if !candidate.begin.After(fresh) && candidate.end.After(fresh) && candidate.level > result {
			result = candidate.level
		}
	}
	return result, nil
}

// RealmQuotaPerkLevel is an authenticated self-read. Read-only quota projection
// must not acquire the mutation scope required by the separate profile boundary.
func (r *Repository) RealmQuotaPerkLevel(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, levels map[string]int, at time.Time) (int, error) {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil || levels == nil {
		return 0, c.ErrUnavailable
	}
	if _, e := r.guard.Authenticate(ctx, db, tx, actor); e != nil {
		return 0, e
	}
	return r.realmAccountPerk(ctx, tx, actor, levels, at)
}
