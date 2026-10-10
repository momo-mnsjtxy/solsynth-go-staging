// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"time"
)

type storedProviderGuard interface {
	StoredSessionEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64, ...string) (bool, error)
}
type providerConsent struct {
	connection, actor, provider, activity, session, identifier, claim string
	version, epoch                                                    int64
	visibility, minutes                                               int
	expires                                                           time.Time
}

func (r *Repository) enrollProvider(ctx context.Context, actor, activity string, q activities.ProviderRequest) error {
	tx, _, p, e := r.begin(ctx, actor, actor, activities.ManagePermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
		return authority.ErrForbidden
	}
	if _, ok := r.guard.(storedProviderGuard); !ok {
		return authority.ErrUnavailable
	}
	var identifier string
	if e = tx.QueryRowContext(ctx, `SELECT provided_identifier FROM owned_account_connections WHERE id=$1::uuid AND account_id=$2::uuid AND provider=$3::text AND revoked_at IS NULL AND verified_at<=clock_timestamp()`, q.ConnectionID, actor, q.Provider).Scan(&identifier); e != nil {
		return e
	}
	v, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid AND ended_at IS NULL AND deleted_at IS NULL FOR UPDATE`, activity, actor))
	if e != nil {
		return e
	}
	if v.session != p.Session.ID || v.version != p.Account.Version || v.epoch != p.Session.AuthorizationEpoch {
		return authority.ErrForbidden
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return e
	}
	expires := now.Add(time.Duration(q.ConsentMinutes) * time.Minute)
	if expires.After(p.Session.ExpiresAt) {
		expires = p.Session.ExpiresAt
	}
	if !expires.After(now) {
		return authority.ErrForbidden
	}
	if v.LeaseExpiresAt.After(expires) {
		v.LeaseExpiresAt = expires
		v.revision++
		if e = save(ctx, tx, v, true); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_provider_presence_consents(connection_id,account_id,provider,activity_id,session_id,account_version,authorization_epoch,provided_identifier,visibility,lease_minutes,created_at,expires_at,next_at)VALUES($1::uuid,$2::uuid,$3::text,$4::uuid,$5::uuid,$6::bigint,$7::bigint,$8::text,$9::integer,$10::integer,$11::timestamptz,$12::timestamptz,$11::timestamptz+interval '1 minute')ON CONFLICT(connection_id)DO UPDATE SET activity_id=EXCLUDED.activity_id,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,authorization_epoch=EXCLUDED.authorization_epoch,provided_identifier=EXCLUDED.provided_identifier,visibility=EXCLUDED.visibility,lease_minutes=EXCLUDED.lease_minutes,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,next_at=EXCLUDED.next_at,revoked_at=NULL,claim_id=NULL,claimed_until=NULL WHERE owned_provider_presence_consents.account_id=EXCLUDED.account_id AND owned_provider_presence_consents.provider=EXCLUDED.provider`, q.ConnectionID, actor, q.Provider, activity, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, identifier, *q.Visibility, q.LeaseMinutes, now, expires)
	if e != nil {
		return e
	}
	if _, e = r.check(ctx, tx, actor, activities.ManagePermission); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) RevokeProviderConsent(ctx context.Context, actor, connection string) error {
	if !authority.ValidID(connection) {
		return activities.ErrInvalid
	}
	tx, _, _, e := r.begin(ctx, actor, actor, activities.ManagePermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var activity string
	e = tx.QueryRowContext(ctx, `UPDATE owned_provider_presence_consents SET revoked_at=clock_timestamp(),claim_id=NULL,claimed_until=NULL WHERE connection_id=$1::uuid AND account_id=$2::uuid RETURNING activity_id::text`, connection, actor).Scan(&activity)
	if errors.Is(e, sql.ErrNoRows) {
		return activities.ErrNotFound
	}
	if e != nil {
		return e
	}
	v, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, activity, actor))
	if e != nil {
		return e
	}
	now, e := clock(ctx, tx)
	if e != nil {
		return e
	}
	v.DeletedAt = &now
	v.UpdatedAt = now
	v.revision++
	if e = save(ctx, tx, v, true); e != nil {
		return e
	}
	if e = r.event(ctx, tx, v, "delete"); e != nil {
		return e
	}
	if _, e = r.check(ctx, tx, actor, activities.ManagePermission); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) claimProvider(ctx context.Context, provider string) (v providerConsent, found bool, e error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	// Across all processes Last.fm observes at most one user each second. The
	// same global limiter bounds Steam calls; row claims prevent overlapping users.
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_provider_presence_rate(provider,next_at)VALUES($1::text,clock_timestamp())ON CONFLICT DO NOTHING`, provider)
	if e != nil {
		return
	}
	var permitted bool
	e = tx.QueryRowContext(ctx, `SELECT next_at<=clock_timestamp() FROM owned_provider_presence_rate WHERE provider=$1::text FOR UPDATE SKIP LOCKED`, provider).Scan(&permitted)
	if errors.Is(e, sql.ErrNoRows) {
		return v, false, nil
	}
	if e != nil || !permitted {
		return v, false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT connection_id::text,account_id::text,provider,activity_id::text,session_id::text,account_version,authorization_epoch,provided_identifier,visibility,lease_minutes,expires_at FROM owned_provider_presence_consents WHERE provider=$1::text AND revoked_at IS NULL AND expires_at>clock_timestamp()AND next_at<=clock_timestamp()AND(claimed_until IS NULL OR claimed_until<=clock_timestamp())ORDER BY next_at,connection_id LIMIT 1 FOR UPDATE SKIP LOCKED`, provider).Scan(&v.connection, &v.actor, &v.provider, &v.activity, &v.session, &v.version, &v.epoch, &v.identifier, &v.visibility, &v.minutes, &v.expires)
	if errors.Is(e, sql.ErrNoRows) {
		return v, false, nil
	}
	if e != nil {
		return
	}
	v.claim, e = uuid()
	if e != nil {
		return
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_provider_presence_consents SET claim_id=$2::uuid,claimed_until=clock_timestamp()+interval '30 seconds'WHERE connection_id=$1::uuid`, v.connection, v.claim)
	if e != nil {
		return
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_provider_presence_rate SET next_at=clock_timestamp()+interval '1 second' WHERE provider=$1::text`, provider)
	if e != nil {
		return
	}
	e = tx.Commit()
	found = e == nil
	return
}
func (r *Repository) consentEligible(ctx context.Context, tx *sql.Tx, v providerConsent) (bool, error) {
	g, ok := r.guard.(storedProviderGuard)
	if !ok {
		return false, authority.ErrUnavailable
	}
	active, e := g.StoredSessionEligible(ctx, r.db, tx, v.actor, v.session, v.version, v.epoch, activities.ManagePermission)
	if e != nil || !active {
		return active, e
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_provider_presence_consents c JOIN owned_account_connections l ON l.id=c.connection_id JOIN owned_authority_sessions s ON s.id=c.session_id WHERE c.connection_id=$1::uuid AND c.claim_id=$2::uuid AND c.claimed_until>clock_timestamp()AND c.revoked_at IS NULL AND c.expires_at>clock_timestamp()AND l.account_id=c.account_id AND l.provider=c.provider AND l.provided_identifier=c.provided_identifier AND l.revoked_at IS NULL AND l.verified_at<=clock_timestamp()AND s.type=0 AND s.parent_id IS NULL AND s.client_id='')`, v.connection, v.claim).Scan(&valid)
	return valid, e
}

// prepareProvider validates the durable original admission before any external
// read. The claim was committed separately; acquire account then consent in the
// same order as revoke/enroll, and never retain database locks during provider I/O.
func (r *Repository) prepareProvider(ctx context.Context, v providerConsent) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var account, claim string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, v.actor).Scan(&account); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(claim_id::text,'')FROM owned_provider_presence_consents WHERE connection_id=$1::uuid FOR UPDATE`, v.connection).Scan(&claim); e != nil {
		return false, e
	}
	if claim != v.claim {
		return false, nil
	}
	valid, e := r.consentEligible(ctx, tx, v)
	if e != nil {
		return false, e
	}
	if valid {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid AND session_id=$3::uuid AND account_version=$4::bigint AND session_epoch=$5::bigint AND ended_at IS NULL AND deleted_at IS NULL)`, v.activity, v.actor, v.session, v.version, v.epoch).Scan(&valid)
		if e != nil {
			return false, e
		}
	}
	if !valid {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_provider_presence_consents SET revoked_at=clock_timestamp(),claim_id=NULL,claimed_until=NULL WHERE connection_id=$1::uuid AND claim_id=$2::uuid`, v.connection, v.claim); e != nil {
			return false, e
		}
	}
	return valid, tx.Commit()
}

func (r *Repository) expireProviderConsents(ctx context.Context, limit int) error {
	// Expiration never extends or mutates an activity; its existing lease is
	// already capped to consent expiry. SKIP LOCKED leaves active writers alone.
	_, e := r.db.ExecContext(ctx, `WITH expired AS(SELECT connection_id FROM owned_provider_presence_consents WHERE revoked_at IS NULL AND expires_at<=clock_timestamp()ORDER BY expires_at,connection_id LIMIT $1 FOR UPDATE SKIP LOCKED)UPDATE owned_provider_presence_consents c SET revoked_at=clock_timestamp(),claim_id=NULL,claimed_until=NULL FROM expired x WHERE c.connection_id=x.connection_id`, limit)
	if e != nil {
		return e
	}
	_, e = r.db.ExecContext(ctx, `WITH old AS(SELECT connection_id FROM owned_provider_presence_consents WHERE revoked_at<clock_timestamp()-interval '30 days'ORDER BY revoked_at,connection_id LIMIT $1 FOR UPDATE SKIP LOCKED)DELETE FROM owned_provider_presence_consents c USING old x WHERE c.connection_id=x.connection_id`, limit)
	return e
}
func (r *Repository) applyProvider(ctx context.Context, v providerConsent, content activities.Content, playing bool) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var account string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, v.actor).Scan(&account); e != nil {
		return false, e
	}
	var claim string
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(claim_id::text,'') FROM owned_provider_presence_consents WHERE connection_id=$1::uuid FOR UPDATE`, v.connection).Scan(&claim)
	if e != nil {
		return false, e
	}
	if claim != v.claim {
		return false, nil
	}
	valid, e := r.consentEligible(ctx, tx, v)
	if e != nil {
		return false, e
	}
	if !valid {
		_, e = tx.ExecContext(ctx, `UPDATE owned_provider_presence_consents SET revoked_at=clock_timestamp(),claim_id=NULL,claimed_until=NULL WHERE connection_id=$1::uuid`, v.connection)
		if e != nil {
			return false, e
		}
		return false, tx.Commit()
	}
	old, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_presence_activities WHERE id=$1::uuid AND account_id=$2::uuid FOR UPDATE`, v.activity, v.actor))
	if e != nil {
		return false, e
	}
	if old.DeletedAt != nil || old.EndedAt != nil || old.session != v.session || old.version != v.version || old.epoch != v.epoch {
		return false, activities.ErrNotFound
	}
	if !playing {
		kind, title := "presence", "Idle"
		content = activities.Content{Type: &kind, Title: &title}
	}
	content.Provider = &v.provider
	q := activities.Request{Content: content, LeaseMinutes: v.minutes, Visibility: v.visibility}
	if e = activities.Normalize(&q, false); e != nil {
		return false, e
	}
	old.Content = q.Content
	old.Visibility = v.visibility
	old.revision++
	now, e := clock(ctx, tx)
	if e != nil {
		return false, e
	}
	old.UpdatedAt = now
	old.LeaseExpiresAt = now.Add(time.Duration(v.minutes) * time.Minute)
	if old.LeaseExpiresAt.After(v.expires) {
		old.LeaseExpiresAt = v.expires
	}
	if e = artwork(ctx, tx, v.actor, &old.Content, true); e != nil {
		return false, e
	}
	if e = save(ctx, tx, old, true); e != nil {
		return false, e
	}
	if e = r.event(ctx, tx, old, "update"); e != nil {
		return false, e
	}
	valid, e = r.consentEligible(ctx, tx, v)
	if e != nil {
		return false, e
	}
	fresh, e := clock(ctx, tx)
	if e != nil {
		return false, e
	}
	if !valid || !fresh.Before(old.LeaseExpiresAt) {
		return false, authority.ErrForbidden
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_provider_presence_consents SET next_at=clock_timestamp()+interval '1 minute',claim_id=NULL,claimed_until=NULL WHERE connection_id=$1::uuid AND claim_id=$2::uuid`, v.connection, v.claim); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// ProcessProviders claims durable consent before I/O and reconciles its original
// authority after I/O and every local wait. It updates only an admitted activity.
func (r *Repository) ProcessProviders(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	if limit < 1 || limit > 1000 {
		return 0, activities.ErrInvalid
	}
	if e := r.expireProviderConsents(ctx, limit); e != nil {
		return 0, e
	}
	count := 0
	for _, provider := range []string{"steam", "lastfm"} {
		observer := r.providers[provider]
		if observer == nil {
			continue
		}
		for n := 0; n < limit; n++ {
			v, found, e := r.claimProvider(ctx, provider)
			if e != nil {
				return count, e
			}
			if !found {
				break
			}
			valid, e := r.prepareProvider(ctx, v)
			if e != nil {
				return count, e
			}
			if !valid {
				continue
			}
			bounded, cancel := context.WithDeadline(ctx, v.expires)
			content, playing, e := observer.Observe(bounded, v.identifier)
			cancel()
			if e != nil {
				return count, e
			}
			applied, e := r.applyProvider(ctx, v, content, playing)
			if e != nil {
				return count, e
			}
			if applied {
				count++
			}
		}
	}
	return count, nil
}
