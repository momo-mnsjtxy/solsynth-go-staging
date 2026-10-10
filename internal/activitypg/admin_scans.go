// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"sort"
)

type ScanSelection struct{ AccountID, ProviderIdentifier, Stage string }

// ScheduleSteamInTransaction never impersonates a recipient. An administrator
// can advance only an existing, bounded opt-in; the consumer still checks the
// owner's original root session/version/scope both before and after provider I/O.
func (r *Repository) ScheduleSteamInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, q ScanSelection) (int, error) {
	if r == nil || r.providers["steam"] == nil {
		return 0, activities.ErrUnavailable
	}
	if db != r.db || tx == nil || q.AccountID != "" && !authority.ValidID(q.AccountID) {
		return 0, activities.ErrInvalid
	}
	if q.ProviderIdentifier != "" {
		if len(q.ProviderIdentifier) != 17 {
			return 0, activities.ErrInvalid
		}
		for _, c := range q.ProviderIdentifier {
			if c < '0' || c > '9' {
				return 0, activities.ErrInvalid
			}
		}
	}
	if q.Stage != "" && q.Stage != "active" && q.Stage != "cold" && q.Stage != "maybe" {
		return 0, activities.ErrInvalid
	}
	if q.Stage == "maybe" {
		return 0, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT c.account_id::text,c.connection_id::text,c.activity_id::text,c.session_id::text,c.account_version,c.authorization_epoch,c.provided_identifier FROM owned_provider_presence_consents c JOIN owned_account_connections l ON l.id=c.connection_id WHERE c.provider='steam'AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()AND l.revoked_at IS NULL AND l.account_id=c.account_id AND l.provided_identifier=c.provided_identifier AND($1::text=''OR c.account_id=NULLIF($1::text,'')::uuid)AND($2::text=''OR c.provided_identifier=$2::text)ORDER BY c.account_id,c.connection_id LIMIT 1001`, q.AccountID, q.ProviderIdentifier)
	if e != nil {
		return 0, e
	}
	vs := []providerConsent{}
	for rows.Next() {
		var v providerConsent
		if e = rows.Scan(&v.actor, &v.connection, &v.activity, &v.session, &v.version, &v.epoch, &v.identifier); e != nil {
			break
		}
		vs = append(vs, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	if len(vs) > 1000 {
		return 0, activities.ErrUnavailable
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].actor < vs[j].actor })
	g, ok := r.guard.(storedProviderGuard)
	if !ok {
		return 0, authority.ErrUnavailable
	}
	count := 0
	for _, v := range vs {
		var id string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR SHARE NOWAIT`, v.actor).Scan(&id); e != nil {
			return 0, e
		}
		eligible, e := g.StoredSessionEligible(ctx, r.db, tx, v.actor, v.session, v.version, v.epoch, activities.ManagePermission)
		if e != nil {
			return 0, e
		}
		if !eligible {
			continue
		}
		var admitted bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid AND session_id=$3::uuid AND account_version=$4::bigint AND session_epoch=$5::bigint AND ended_at IS NULL AND deleted_at IS NULL)`, v.activity, v.actor, v.session, v.version, v.epoch).Scan(&admitted); e != nil {
			return 0, e
		}
		if !admitted {
			continue
		}
		if q.Stage != "" {
			var online bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_sessions p JOIN owned_authority_sessions s ON s.id=p.session_id AND s.account_id=p.account_id JOIN owned_authority_accounts a ON a.id=p.account_id WHERE p.account_id=$1::uuid AND p.expires_at>clock_timestamp()AND s.expires_at>clock_timestamp()AND s.revoked_at IS NULL AND NOT a.disabled AND p.epoch=s.epoch AND p.account_version=a.version AND s.account_version=a.version)`, v.actor).Scan(&online); e != nil {
				return 0, e
			}
			if (q.Stage == "active") != online {
				continue
			}
		}
		res, e := tx.ExecContext(ctx, `UPDATE owned_provider_presence_consents SET next_at=clock_timestamp()WHERE connection_id=$1::uuid AND revoked_at IS NULL AND expires_at>clock_timestamp()AND(claimed_until IS NULL OR claimed_until<=clock_timestamp())`, v.connection)
		if e != nil {
			return 0, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return 0, e
		}
		count += int(n)
	}
	return count, nil
}
