// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"time"
)

//go:embed wellbeing_schema.sql
var WellbeingSchema string

func (r *Repository) WithNightOwlReminders(enabled bool) *Repository { r.nightOwls = enabled; return r }
func (r *Repository) NightOwlReady(ctx context.Context) error {
	if !r.nightOwls {
		return nil
	}
	_, e := r.db.ExecContext(ctx, `SELECT account_id,local_day,local_hour,outbox_id FROM notification_v1.night_owl_receipts LIMIT 0`)
	return e
}
func wellbeingActive(ctx context.Context, tx *sql.Tx, account string) (bool, error) {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE id=$1::uuid AND NOT disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account'AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())))`, account).Scan(&active)
	return active, e
}

// EnqueueNightOwlInTransaction is a trusted, same-pool product capability. The
// caller holds target account UPDATE and, for admin calls, the original root proof.
func (r *Repository) EnqueueNightOwlInTransaction(ctx context.Context, db *sql.DB, tx *sql.Tx, account, key string, hour int) (string, error) {
	if r == nil || !r.nightOwls {
		return "", notifications.ErrUnavailable
	}
	if db != r.db || tx == nil || hour < 0 || hour > 2 {
		return "", notifications.ErrInvalid
	}
	active, e := wellbeingActive(ctx, tx, account)
	if e != nil {
		return "", e
	}
	if !active {
		return "", authority.ErrDisabled
	}
	bodies := []string{"It's getting late. Consider winding down and getting some rest.", "A little rest can help tomorrow feel better. Take a break when you can.", "It's late in your time zone. Consider putting your screen away and resting."}
	id, e := r.EnqueueInTransaction(ctx, db, tx, notifications.Command{Key: key, AccountID: account, AppID: r.app, Topic: "accounts.wellbeing.sleep", Title: "Time for some rest", Content: bodies[hour], Meta: []byte(`{"action_uri":"/","wellbeing_reminder":true}`), External: true})
	if e != nil {
		return "", e
	}
	active, e = wellbeingActive(ctx, tx, account)
	if e != nil {
		return "", e
	}
	if !active {
		return "", authority.ErrDisabled
	}
	return id, nil
}

func (r *Repository) recentNightOwl(ctx context.Context, tx *sql.Tx, account string, now time.Time) (bool, error) {
	var recent bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_presence_last_seen WHERE account_id=$1::uuid AND last_seen_at<=$2::timestamptz AND last_seen_at>=$2::timestamptz-interval '15 minutes')`, account, now).Scan(&recent); e != nil || recent {
		return recent, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT session_id::text,account_version,session_epoch FROM owned_presence_activities WHERE account_id=$1::uuid AND ended_at IS NULL AND deleted_at IS NULL AND lease_expires_at>$2::timestamptz ORDER BY id LIMIT 1001`, account, now)
	if e != nil {
		return false, e
	}
	type admission struct {
		s        string
		v, epoch int64
	}
	vs := []admission{}
	for rows.Next() {
		var v admission
		if e = rows.Scan(&v.s, &v.v, &v.epoch); e != nil {
			break
		}
		vs = append(vs, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return false, e
	}
	if len(vs) > 1000 {
		return false, notifications.ErrUnavailable
	}
	for _, v := range vs {
		ok, e := r.guard.StoredAuthenticationEligible(ctx, r.db, tx, account, v.s, v.v, v.epoch)
		if e != nil {
			return false, e
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// ProcessNightOwls sends each active account at most once per local hour 0..2.
// A canonical receipt and notification command commit together, including across
// independent processes and restarts. Invalid time zones are not guessed.
func (r *Repository) ProcessNightOwls(ctx context.Context, limit int) (int, error) {
	if r == nil || !r.nightOwls {
		return 0, nil
	}
	if r.guard == nil || limit < 1 || limit > 1000 {
		return 0, notifications.ErrUnavailable
	}
	rows, e := r.db.QueryContext(ctx, `SELECT a.id::text FROM owned_authority_accounts a JOIN owned_accounts_profiles p ON p.account_id=a.id JOIN pg_timezone_names z ON z.name=p.time_zone WHERE NOT a.disabled AND extract(hour FROM clock_timestamp()AT TIME ZONE z.name)<3 AND NOT EXISTS(SELECT 1 FROM notification_v1.night_owl_receipts n WHERE n.account_id=a.id AND n.local_day=(clock_timestamp()AT TIME ZONE z.name)::date AND n.local_hour=extract(hour FROM clock_timestamp()AT TIME ZONE z.name)::integer)AND(EXISTS(SELECT 1 FROM owned_presence_last_seen s WHERE s.account_id=a.id AND s.last_seen_at>=clock_timestamp()-interval '15 minutes')OR EXISTS(SELECT 1 FROM owned_presence_activities s WHERE s.account_id=a.id AND s.deleted_at IS NULL AND s.ended_at IS NULL AND s.lease_expires_at>clock_timestamp()))ORDER BY a.id LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, account := range ids {
		tx, e := r.begin(ctx)
		if e != nil {
			return count, e
		}
		applied, err := func() (bool, error) {
			defer tx.Rollback()
			active, e := lockAccount(ctx, tx, account)
			if e != nil || !active {
				return false, e
			}
			var zone string
			var now time.Time
			if e = tx.QueryRowContext(ctx, `SELECT p.time_zone,clock_timestamp()FROM owned_accounts_profiles p WHERE account_id=$1::uuid`, account).Scan(&zone, &now); e != nil {
				return false, e
			}
			loc, e := time.LoadLocation(zone)
			if e != nil {
				return false, nil
			}
			local := now.In(loc)
			hour := local.Hour()
			if hour >= 3 {
				return false, nil
			}
			recent, e := r.recentNightOwl(ctx, tx, account, now)
			if e != nil || !recent {
				return false, e
			}
			key := fmt.Sprintf("night-owl:%s:%s:%d", account, local.Format("2006-01-02"), hour)
			var exists bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_v1.night_owl_receipts WHERE account_id=$1::uuid AND local_day=$2::date AND local_hour=$3::integer)`, account, local.Format("2006-01-02"), hour).Scan(&exists); e != nil || exists {
				return false, e
			}
			id, e := r.EnqueueNightOwlInTransaction(ctx, r.db, tx, account, key, hour)
			if e != nil {
				return false, e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.night_owl_receipts(account_id,local_day,local_hour,outbox_id,created_at)VALUES($1::uuid,$2::date,$3::integer,$4::uuid,clock_timestamp())`, account, local.Format("2006-01-02"), hour, id); e != nil {
				return false, e
			}
			var fresh time.Time
			if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&fresh); e != nil {
				return false, e
			}
			if fresh.In(loc).Format("2006-01-02") != local.Format("2006-01-02") || fresh.In(loc).Hour() != hour {
				return false, notifications.ErrConflict
			}
			active, e = wellbeingActive(ctx, tx, account)
			if e != nil {
				return false, e
			}
			if !active {
				return false, authority.ErrDisabled
			}
			recent, e = r.recentNightOwl(ctx, tx, account, fresh)
			if e != nil || !recent {
				return false, e
			}
			return true, tx.Commit()
		}()
		if err != nil {
			if errors.Is(err, authority.ErrDisabled) || errors.Is(err, notifications.ErrConflict) {
				continue
			}
			return count, err
		}
		if applied {
			count++
		}
	}
	return count, nil
}
