// SPDX-License-Identifier: AGPL-3.0-only
package accountsettingspg

import (
	"context"
	"database/sql"
	_ "embed"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accountsettings"
	"time"
)

//go:embed schema.sql
var Schema string

type Repository struct {
	db    *sql.DB
	guard *authoritypg.TransactionGuard
	app   string
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, app string) (*Repository, error) {
	if db == nil || g == nil || !notifications.ValidApp(app) {
		return nil, accountsettings.ErrInvalid
	}
	return &Repository{db, g, app}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	_, e := r.db.ExecContext(ctx, `SELECT account_id,app_id,topic,preference FROM owned_account_notification_preferences LIMIT 0;SELECT account_id,topic,action FROM owned_account_settings_audit LIMIT 0`)
	return e
}
func (r *Repository) within(ctx context.Context, actor, app string, write bool, fn func(*sql.Tx, time.Time) error) error {
	if !authority.ValidID(actor) || app != r.app {
		return accountsettings.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if write {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, "notifications.preferences.manage")
	} else {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	if e != nil {
		return e
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) List(ctx context.Context, actor, app string) (out []accountsettings.Preference, e error) {
	out = []accountsettings.Preference{}
	e = r.within(ctx, actor, app, false, func(tx *sql.Tx, _ time.Time) error {
		rows, e := tx.QueryContext(ctx, `SELECT topic,preference,description,updated_at FROM owned_account_notification_preferences WHERE account_id=$1::uuid AND app_id=$2::text ORDER BY topic LIMIT 201`, actor, app)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v accountsettings.Preference
			if e = rows.Scan(&v.Topic, &v.Preference, &v.Description, &v.UpdatedAt); e != nil {
				return e
			}
			out = append(out, v)
			if len(out) > 200 {
				return accountsettings.ErrLimit
			}
		}
		return rows.Err()
	})
	return
}
func (r *Repository) Set(ctx context.Context, actor, app, topic string, v accountsettings.PreferenceInput) (out accountsettings.Preference, e error) {
	if !accountsettings.ValidTopic(topic) || !accountsettings.ValidPreference(v) {
		return out, accountsettings.ErrInvalid
	}
	e = r.within(ctx, actor, app, true, func(tx *sql.Tx, now time.Time) error {
		var count int
		var exists bool
		if e := tx.QueryRowContext(ctx, `SELECT count(*),EXISTS(SELECT 1 FROM owned_account_notification_preferences WHERE account_id=$1::uuid AND app_id=$2::text AND topic=$3::text)FROM owned_account_notification_preferences WHERE account_id=$1::uuid AND app_id=$2::text`, actor, app, topic).Scan(&count, &exists); e != nil {
			return e
		}
		if count >= 200 && !exists {
			return accountsettings.ErrLimit
		}
		if e := tx.QueryRowContext(ctx, `INSERT INTO owned_account_notification_preferences(account_id,app_id,topic,preference,description,updated_at)VALUES($1::uuid,$2::text,$3::text,$4::text,$5::text,$6::timestamptz)ON CONFLICT(account_id,app_id,topic)DO UPDATE SET preference=EXCLUDED.preference,description=EXCLUDED.description,updated_at=EXCLUDED.updated_at RETURNING topic,preference,description,updated_at`, actor, app, topic, v.Preference, v.Description, now).Scan(&out.Topic, &out.Preference, &out.Description, &out.UpdatedAt); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `INSERT INTO owned_account_settings_audit(account_id,app_id,topic,action,occurred_at)VALUES($1::uuid,$2::text,$3::text,'preference.set',$4::timestamptz)`, actor, app, topic, now)
		return e
	})
	return
}
func (r *Repository) Reset(ctx context.Context, actor, app, topic string) error {
	if !accountsettings.ValidTopic(topic) {
		return accountsettings.ErrInvalid
	}
	return r.within(ctx, actor, app, true, func(tx *sql.Tx, now time.Time) error {
		res, e := tx.ExecContext(ctx, `DELETE FROM owned_account_notification_preferences WHERE account_id=$1::uuid AND app_id=$2::text AND topic=$3::text`, actor, app, topic)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_account_settings_audit(account_id,app_id,topic,action,occurred_at)VALUES($1::uuid,$2::text,$3::text,'preference.reset',$4::timestamptz)`, actor, app, topic, now)
		return e
	})
}

// Preference is a read-only worker seam. Caller already locks the recipient's
// canonical account and owns this transaction; no new transaction or I/O occurs.
func (r *Repository) Preference(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, app, topic string) (string, error) {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(actor) || app != r.app {
		return "", accountsettings.ErrInvalid
	}
	var level string
	e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT preference FROM owned_account_notification_preferences WHERE account_id=$1::uuid AND app_id=$2::text AND topic=$3::text),'normal')`, actor, app, topic).Scan(&level)
	return level, e
}
