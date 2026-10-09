// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/signinpg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	v "solsynth.local/go-migration/ports/admin/verification"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/progression"
	"sort"
	"time"
)

func (r *Repository) requireAccountAdmin(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	p, e := r.guard.AuthorizeWithGrant(ctx, r.db, tx, actor, permission, permission)
	if e != nil {
		return e
	}
	return requireAccountAdminRoot(p)
}

// Cross-account security projections and badge writes are a root-session lane.
// Ordinary Type-0 verification alone does not validate a child's parent, and an
// OAuth or other delegated proof must not inherit an administrator's privileges.
func requireAccountAdminRoot(p authority.Principal) error {
	if !p.Account.Superuser || p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
		return c.ErrForbidden
	}
	return nil
}

// The preflight establishes explicit global authority before observing private
// target existence. The original opaque proof survives every lock wait.
func (r *Repository) beginAccountAdmin(ctx context.Context, actor, ref, permission string) (tx *sql.Tx, id string, e error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, "", c.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, "", authority.ErrInvalid
	}
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	ok := false
	defer func() {
		if !ok {
			tx.Rollback()
		}
	}()
	p, e := r.guard.PreflightWithGrant(ctx, r.db, tx, actor, permission, permission)
	if e != nil {
		return tx, "", e
	}
	if e = requireAccountAdminRoot(p); e != nil {
		return tx, "", e
	}
	ids := []string{actor}
	if ref != "" {
		observed, x := observeVerification(ctx, tx, v.Account, ref)
		if x != nil {
			if errors.Is(x, c.ErrNotFound) {
				x = a.ErrAccountNotFound
			}
			return tx, "", x
		}
		id = observed.id
		if id != actor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, key := range ids {
		if e = lockTarget(ctx, tx, "owned_authority_accounts", key); e != nil {
			return tx, "", e
		}
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, permission); e != nil {
		return tx, "", e
	}
	if ref != "" {
		current, x := observeVerification(ctx, tx, v.Account, ref)
		if x != nil {
			if errors.Is(x, c.ErrNotFound) {
				x = a.ErrAccountNotFound
			}
			return tx, "", x
		}
		if current.id != id {
			return tx, "", c.ErrConflict
		}
	}
	ok = true
	return tx, id, nil
}

func (r *Repository) AccountContacts(ctx context.Context, actor, ref string) (out []signin.Contact, e error) {
	if ref == "" {
		return nil, c.ErrInvalid
	}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ViewPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, e = signinpg.AdministrativeContactsInTransaction(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) AccountFactors(ctx context.Context, actor, ref string) (out []signin.FactorSummary, e error) {
	if ref == "" {
		return nil, c.ErrInvalid
	}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ViewPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, e = signinpg.AdministrativeFactorsInTransaction(ctx, tx, id)
	if e != nil {
		return nil, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) AccountBadges(ctx context.Context, actor, ref string) (out []progression.Badge, e error) {
	if ref == "" {
		return nil, c.ErrInvalid
	}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ViewPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, e = progressionpg.AdministrativeBadgeRowsInTransaction(ctx, tx, id, r.now().UTC())
	if e != nil {
		return nil, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}

func (r *Repository) AccountActivityMetrics(ctx context.Context, actor string) (out a.ActivityMetrics, e error) {
	tx, _, e := r.beginAccountAdmin(ctx, actor, "", a.ViewPermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	week, month := day.AddDate(0, 0, -6), day.AddDate(0, 0, -29)
	out.CalculatedAt, out.CurrentDayStartedAt = now, day
	// One snapshot; documented profile population and UTC non-overlapping
	// comparison windows. Future timestamps are not counted as observed activity.
	e = tx.QueryRowContext(ctx, `SELECT count(*),count(*)FILTER(WHERE l.last_seen_at>=$2 AND l.last_seen_at<=$1),count(*)FILTER(WHERE l.last_seen_at>=$3 AND l.last_seen_at<=$1),count(*)FILTER(WHERE l.last_seen_at>=$4 AND l.last_seen_at<=$1),count(*)FILTER(WHERE l.last_seen_at>=$5 AND l.last_seen_at<$2),count(*)FILTER(WHERE l.last_seen_at>=$6 AND l.last_seen_at<$3),count(*)FILTER(WHERE l.last_seen_at>=$7 AND l.last_seen_at<$4),count(*)FILTER(WHERE ac.created_at>=$2 AND ac.created_at<=$1),count(*)FILTER(WHERE ac.created_at>=$3 AND ac.created_at<=$1),count(*)FILTER(WHERE ac.created_at>=$4 AND ac.created_at<=$1) FROM owned_accounts_profiles p JOIN owned_authority_accounts ac ON ac.id=p.account_id LEFT JOIN owned_presence_last_seen l ON l.account_id=ac.id`, now, day, week, month, day.AddDate(0, 0, -1), week.AddDate(0, 0, -7), month.AddDate(0, 0, -30)).Scan(&out.TotalProfiledAccounts, &out.DailyActiveUsers, &out.WeeklyActiveUsers, &out.MonthlyActiveUsers, &out.PreviousDailyActiveUsers, &out.PreviousWeeklyActiveUsers, &out.PreviousMonthlyActiveUsers, &out.NewAccountsToday, &out.NewAccountsThisWeek, &out.NewAccountsThisMonth)
	if e != nil {
		return a.ActivityMetrics{}, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return a.ActivityMetrics{}, e
	}
	return out, tx.Commit()
}

func (r *Repository) badgeTargetEligible(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	var allowed bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=$2)AND(p.expires_at IS NULL OR p.expires_at>$2)) FROM owned_authority_accounts a WHERE a.id=$1`, id, now).Scan(&allowed)
	if e != nil {
		return mapped(e)
	}
	if !allowed {
		return c.ErrConflict
	}
	return nil
}
func (r *Repository) GrantAccountBadge(ctx context.Context, actor, ref string, request progression.AdminBadge) (out progression.Badge, e error) {
	if ref == "" {
		return out, c.ErrInvalid
	}
	if e = a.ValidateBadge(request); e != nil {
		return out, e
	}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.BadgePermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = progressionpg.LockAccountBadgesInTransaction(ctx, tx, id); e != nil {
		return out, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.BadgePermission); e != nil {
		return out, e
	}
	now := r.now().UTC()
	if e = r.badgeTargetEligible(ctx, tx, id, now); e != nil {
		return out, e
	}
	if request.ActivatedAt != nil && (request.ActivatedAt.After(now) || request.ExpiredAt != nil && !now.Before(*request.ExpiredAt)) {
		return out, c.ErrInvalid
	}
	out, e = progressionpg.GrantAdministrativeBadgeInTransaction(ctx, tx, actor, id, request, now)
	if e != nil {
		return progression.Badge{}, mapped(e)
	}
	if e = r.audit(ctx, tx, actor, "account", id, "badge.grant", map[string]any{"badge_id": out.ID, "origin": "admin", "type": out.Type}); e != nil {
		return progression.Badge{}, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.BadgePermission); e != nil {
		return progression.Badge{}, e
	}
	if e = r.badgeTargetEligible(ctx, tx, id, r.now().UTC()); e != nil {
		return progression.Badge{}, e
	}
	if out.ActivatedAt != nil && out.ExpiredAt != nil && !r.now().UTC().Before(*out.ExpiredAt) {
		return progression.Badge{}, c.ErrConflict
	}
	return out, tx.Commit()
}
func (r *Repository) ActivateAccountBadge(ctx context.Context, actor, ref, badge string) error {
	return r.changeAccountBadge(ctx, actor, ref, badge, false)
}
func (r *Repository) RevokeAccountBadge(ctx context.Context, actor, ref, badge string) error {
	return r.changeAccountBadge(ctx, actor, ref, badge, true)
}
func (r *Repository) changeAccountBadge(ctx context.Context, actor, ref, badge string, revoke bool) error {
	if ref == "" || !authority.ValidID(badge) {
		return c.ErrInvalid
	}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.BadgePermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = progressionpg.LockAccountBadgesInTransaction(ctx, tx, id); e != nil {
		return e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.BadgePermission); e != nil {
		return e
	}
	b, e := progressionpg.ReadAdministrativeBadgeInTransaction(ctx, tx, id, badge)
	if e != nil {
		return mapped(e)
	}
	now := r.now().UTC()
	action := "badge.activate"
	if revoke {
		action = "badge.revoke"
		e = progressionpg.RevokeAdministrativeBadgeInTransaction(ctx, tx, id, badge, now)
	} else {
		if b.ExpiredAt != nil && !now.Before(*b.ExpiredAt) {
			return c.ErrConflict
		}
		if e = r.badgeTargetEligible(ctx, tx, id, now); e != nil {
			return e
		}
		e = progressionpg.ActivateAdministrativeBadgeInTransaction(ctx, tx, id, badge, now)
	}
	if e != nil {
		return e
	}
	if e = r.audit(ctx, tx, actor, "account", id, action, map[string]any{"badge_id": badge}); e != nil {
		return e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.BadgePermission); e != nil {
		return e
	}
	if !revoke {
		if e = r.badgeTargetEligible(ctx, tx, id, r.now().UTC()); e != nil {
			return e
		}
		if b.ExpiredAt != nil && !r.now().UTC().Before(*b.ExpiredAt) {
			return c.ErrConflict
		}
	}
	return tx.Commit()
}
