// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"database/sql"
	"strings"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
)

func (r *Repository) DeveloperStatistics(ctx context.Context, name string) (out apps.DeveloperStats, e error) {
	if r == nil || r.db == nil {
		return out, apps.ErrUnavailable
	}
	if strings.TrimSpace(name) == "" || len(name) > 256 {
		return out, apps.ErrInvalid
	}
	// One statement snapshot binds count, canonical ancestry and owner status.
	// As in the authored controller, the public aggregate includes unpublished
	// apps; soft-deleted apps and projects do not contribute.
	e = r.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM owned_custom_apps app JOIN owned_dev_projects project ON project.id=app.project_id WHERE project.developer_id=d.id AND project.deleted_at IS NULL AND app.deleted_at IS NULL) FROM owned_developers d JOIN owned_publishers p ON p.id=d.publisher_id JOIN owned_authority_accounts owner ON owner.id=p.account_id WHERE `+accountpg.PublisherNamePredicate("p", "$1")+` AND p.type=0 AND p.realm_id IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=owner.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=statement_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>statement_timestamp()))`, name).Scan(&out.TotalCustomApps)
	return out, mapped(e)
}

// RequireCustomOrderManager keeps the source's editor role and apps.update
// permission, using the original account proof, never an app-secret identity.
// Call before merchant/idempotency locks and again immediately before commit.
func (r *Repository) RequireCustomOrderManager(ctx context.Context, tx *sql.Tx, s apps.Scope) (out apps.Client, e error) {
	if r == nil || r.db == nil || r.guard == nil || tx == nil {
		return out, apps.ErrUnavailable
	}
	if !authority.ValidID(s.Actor) || !authority.ValidID(s.App) || !authority.ValidID(s.Project) || strings.TrimSpace(s.Developer) == "" || len(s.Developer) > 256 {
		return out, apps.ErrInvalid
	}
	var isolation string
	if e = tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil || isolation != "read committed" {
		return out, apps.ErrUnavailable
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, "developers.apps.update"); e != nil {
		return out, e
	}
	d, e := scope(ctx, tx, s, 50, true)
	if e != nil {
		return out, e
	}
	// scope() may wait for a management transaction beyond the proof deadline.
	if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, "developers.apps.update"); e != nil {
		return out, e
	}
	// Do not introduce an owner account lock after the actor/ancestry locks:
	// reciprocal managers can otherwise deadlock. Fresh observations after all
	// waits provide the same current-owner boundary as secret issuance/checkout.
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+appJoins+` JOIN owned_authority_accounts owner ON owner.id=pub.account_id JOIN owned_publisher_members member ON member.publisher_id=pub.id WHERE a.id=$1 AND p.id=$2 AND d.id=$3 AND pub.id=$4 AND a.status=1 AND pub.realm_id IS NULL AND `+activeApp+` AND member.account_id=$5 AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND member.role>=50 AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=owner.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())))`, s.App, s.Project, d.ID, d.PublisherID, s.Actor).Scan(&active)
	if e != nil {
		return out, e
	}
	if !active {
		return out, apps.ErrForbidden
	}
	return apps.Client{AppID: s.App, PublisherID: d.PublisherID}, nil
}

var _ apps.StatisticsRepository = (*Repository)(nil)
