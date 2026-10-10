// SPDX-License-Identifier: AGPL-3.0-only
package realmboostpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"time"
)

// LabelPolicyInTransaction exposes the same copied cap policy and live paid
// entitlement as Status. It cannot alter or seed the frozen boost ledger.
func (r *Repository) LabelPolicyInTransaction(ctx context.Context, tx *sql.Tx, realm string) (level, cap int, e error) {
	_, level, e = r.BoostStateInTransaction(ctx, tx, realm)
	if e != nil {
		return
	}
	if level < 0 || level >= len(r.cfg.Policy.LabelCaps) {
		return 0, 0, b.ErrUnavailable
	}
	return level, r.cfg.Policy.LabelCaps[level], nil
}

// MemberPerkInTransaction reuses the explicitly configured canonical perk
// selection. The caller supplies its final clock after account/realm locks.
func (r *Repository) MemberPerkInTransaction(ctx context.Context, tx *sql.Tx, actor, realm, account string, at time.Time) (int, error) {
	if r == nil || tx == nil || !authority.ValidID(account) {
		return 0, b.ErrUnavailable
	}
	provider, ok := r.commerce.(interface {
		RealmMemberPerkLevel(context.Context, *sql.DB, *sql.Tx, string, string, string, map[string]int, time.Time) (int, error)
	})
	if !ok {
		return 0, b.ErrUnavailable
	}
	return provider.RealmMemberPerkLevel(ctx, r.db, tx, actor, realm, account, r.cfg.Policy.PerkLevels, at)
}

// ProfilePerkInTransaction uses the same explicit subscription map as boosts,
// with an actor-only profile scope rather than payer or realm-owner authority.
func (r *Repository) ProfilePerkInTransaction(ctx context.Context, tx *sql.Tx, actor string, at time.Time) (int, error) {
	if r == nil || tx == nil || !authority.ValidID(actor) {
		return 0, b.ErrUnavailable
	}
	provider, ok := r.commerce.(interface {
		RealmProfilePerkLevel(context.Context, *sql.DB, *sql.Tx, string, map[string]int, time.Time) (int, error)
	})
	if !ok {
		return 0, b.ErrUnavailable
	}
	return provider.RealmProfilePerkLevel(ctx, r.db, tx, actor, r.cfg.Policy.PerkLevels, at)
}

func (r *Repository) QuotaPerkInTransaction(ctx context.Context, tx *sql.Tx, actor string, at time.Time) (int, error) {
	if r == nil || tx == nil || !authority.ValidID(actor) {
		return 0, b.ErrUnavailable
	}
	provider, ok := r.commerce.(interface {
		RealmQuotaPerkLevel(context.Context, *sql.DB, *sql.Tx, string, map[string]int, time.Time) (int, error)
	})
	if !ok {
		return 0, b.ErrUnavailable
	}
	return provider.RealmQuotaPerkLevel(ctx, r.db, tx, actor, r.cfg.Policy.PerkLevels, at)
}

// MaintenancePerkInTransaction cannot impersonate an owner's session. It is
// limited to the trusted canonical-source award and daily-tenure workers.
func (r *Repository) MaintenancePerkInTransaction(ctx context.Context, tx *sql.Tx, realm, account string, at time.Time) (int, error) {
	provider, ok := r.commerce.(interface {
		RealmMaintenancePerkLevel(context.Context, *sql.DB, *sql.Tx, string, string, map[string]int, time.Time) (int, error)
	})
	if !ok {
		return 0, b.ErrUnavailable
	}
	return provider.RealmMaintenancePerkLevel(ctx, r.db, tx, realm, account, r.cfg.Policy.PerkLevels, at)
}
