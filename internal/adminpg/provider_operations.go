// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/auditcapture"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
)

type PresenceScanner interface {
	ScheduleSteamInTransaction(context.Context, *sql.DB, *sql.Tx, activitypg.ScanSelection) (int, error)
}
type NightOwlSender interface {
	EnqueueNightOwlInTransaction(context.Context, *sql.DB, *sql.Tx, string, string, int) (string, error)
}

func (r *Repository) WithPresenceScanner(s PresenceScanner) *Repository {
	r.presenceScanner = s
	return r
}
func (r *Repository) WithNightOwlSender(s NightOwlSender) *Repository { r.nightOwlSender = s; return r }
func (r *Repository) ScheduleSteamPresence(ctx context.Context, actor string, q a.ProviderScan) (int, error) {
	tx, target, e := r.beginAccountAdmin(ctx, actor, q.AccountRef, a.PresenceScanPermission)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if r.presenceScanner == nil {
		return 0, c.ErrUnavailable
	}
	n, e := r.presenceScanner.ScheduleSteamInTransaction(ctx, r.db, tx, activitypg.ScanSelection{AccountID: target, ProviderIdentifier: q.ProviderIdentifier, Stage: q.Stage})
	if e != nil {
		return 0, e
	}
	auditTarget := target
	if auditTarget == "" {
		auditTarget = actor
	}
	if e = auditcapture.Account(ctx, tx, actor, "accounts.presence.steam.schedule", auditTarget); e != nil {
		return 0, e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.PresenceScanPermission); e != nil {
		return 0, e
	}
	return n, tx.Commit()
}
func (r *Repository) SendNightOwlReminder(ctx context.Context, actor, ref string, hour int) (string, error) {
	if hour < 0 || hour > 2 {
		return "", c.ErrInvalid
	}
	tx, target, e := r.beginAccountAdmin(ctx, actor, ref, a.ManagePermission)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if r.nightOwlSender == nil {
		return "", c.ErrUnavailable
	}
	request, e := newID()
	if e != nil {
		return "", e
	}
	id, e := r.nightOwlSender.EnqueueNightOwlInTransaction(ctx, r.db, tx, target, "night-owl-admin:"+request, hour)
	if e != nil {
		return "", e
	}
	if e = auditcapture.Account(ctx, tx, actor, "accounts.notification.night-owl", target); e != nil {
		return "", e
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ManagePermission); e != nil {
		return "", e
	}
	return id, tx.Commit()
}
