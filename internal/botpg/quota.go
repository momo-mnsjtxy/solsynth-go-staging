// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
)

// DeveloperQuota is an explicit owned account-to-publisher policy projection.
// It never guesses a publisher from memberships, sums unrelated capacities, or
// invents a bonus/subscription formula. Install the binding separately; neither
// this read nor schema installation creates policy or increases capacity.
func (r *Repository) DeveloperQuota(ctx context.Context, actor string) (out apps.DeveloperQuota, e error) {
	if r == nil || r.db == nil || r.guard == nil {
		return out, apps.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return out, apps.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return out, e
	}
	var publisher string
	e = tx.QueryRowContext(ctx, `SELECT binding.publisher_id::text FROM owned_developer_quota_accounts binding WHERE binding.account_id=$1 FOR SHARE`, actor).Scan(&publisher)
	if errors.Is(e, sql.ErrNoRows) {
		return out, apps.ErrUnavailable
	}
	if e != nil {
		return out, e
	}
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT pub.account_id::text FROM owned_publishers pub JOIN owned_developers developer ON developer.publisher_id=pub.id WHERE pub.id=$1 AND pub.account_id=$2 AND pub.type=0 AND pub.realm_id IS NULL AND pub.deleted_at IS NULL FOR SHARE OF pub`, publisher, actor).Scan(&owner)
	if errors.Is(e, sql.ErrNoRows) {
		return out, apps.ErrForbidden
	}
	if e != nil {
		return out, e
	}
	// Lock the exact admission policy, then observe its wall-clock validity after
	// the wait. Used counts the same retained live bot reservations as capacity().
	e = tx.QueryRowContext(ctx, `SELECT bots_total FROM owned_bot_quotas WHERE publisher_id=$1 FOR SHARE`, publisher).Scan(&out.Total)
	if errors.Is(e, sql.ErrNoRows) {
		return out, apps.ErrUnavailable
	}
	if e != nil {
		return out, e
	}
	var current bool
	e = tx.QueryRowContext(ctx, `SELECT starts_at<=clock_timestamp() AND(expires_at IS NULL OR expires_at>clock_timestamp()) FROM owned_bot_quotas WHERE publisher_id=$1`, publisher).Scan(&current)
	if e != nil {
		return out, e
	}
	if !current {
		return out, apps.ErrUnavailable
	}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_bots bot JOIN owned_custom_apps app ON app.id=bot.app_id JOIN owned_dev_projects project ON project.id=app.project_id JOIN owned_developers developer ON developer.id=project.developer_id WHERE developer.publisher_id=$1 AND bot.deleted_at IS NULL`, publisher).Scan(&out.Used)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return out, e
	}
	// Account proof revalidation may wait on session/policy rows.
	e = tx.QueryRowContext(ctx, `SELECT starts_at<=clock_timestamp() AND(expires_at IS NULL OR expires_at>clock_timestamp()) FROM owned_bot_quotas WHERE publisher_id=$1`, publisher).Scan(&current)
	if e != nil {
		return out, e
	}
	if !current {
		return out, apps.ErrUnavailable
	}
	e = tx.Commit()
	return
}

var _ apps.QuotaStore = (*Repository)(nil)

func currentBotQuota(ctx context.Context, tx *sql.Tx, publisher string) error {
	var current bool
	e := tx.QueryRowContext(ctx, `SELECT starts_at<=clock_timestamp() AND(expires_at IS NULL OR expires_at>clock_timestamp()) FROM owned_bot_quotas WHERE publisher_id=$1`, publisher).Scan(&current)
	if errors.Is(e, sql.ErrNoRows) {
		return bots.ErrQuota
	}
	if e != nil {
		return e
	}
	if !current {
		return bots.ErrQuota
	}
	return nil
}
