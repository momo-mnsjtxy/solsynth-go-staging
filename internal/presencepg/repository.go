// SPDX-License-Identifier: AGPL-3.0-only
package presencepg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/presence"
	"time"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db    *sql.DB
	guard TransactionGuard
	ttl   time.Duration
}

// New is deliberately closed to private operations until a guard is supplied.
func New(db *sql.DB) *Repository { return &Repository{db: db, ttl: 90 * time.Second} }
func NewWithAuthority(db *sql.DB, g TransactionGuard) *Repository {
	r := New(db)
	r.guard = g
	return r
}

var _ presence.Repository = (*Repository)(nil)

func (r *Repository) within(ctx context.Context, actor string, permissions []string, fn func(*sql.Tx, authority.Principal, time.Time) error) error {
	if r == nil || r.db == nil {
		return presence.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var p authority.Principal
	if actor != "" {
		if r.guard == nil {
			return authority.ErrUnavailable
		}
		if len(permissions) > 0 {
			p, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...)
		} else {
			p, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		}
		if e != nil {
			return e
		}
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, p, now.UTC()); e != nil {
		return e
	}
	if actor != "" {
		if len(permissions) > 0 {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...)
		} else {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func newID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func prefs(ctx context.Context, tx *sql.Tx, id string) (presence.Preferences, error) {
	v := presence.DefaultPreferences()
	e := tx.QueryRowContext(ctx, `SELECT online_visibility,last_seen_visibility,status_visibility FROM owned_presence_preferences WHERE account_id=$1`, id).Scan(&v.OnlineVisibility, &v.LastSeenVisibility, &v.StatusVisibility)
	if errors.Is(e, sql.ErrNoRows) {
		e = nil
	}
	return v, e
}
func (r *Repository) GetPreferences(ctx context.Context, actor string) (v presence.Preferences, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, _ authority.Principal, _ time.Time) error {
		var e error
		v, e = prefs(ctx, tx, actor)
		return e
	})
	return
}
func (r *Repository) SetPreferences(ctx context.Context, actor string, v presence.Preferences) (presence.Preferences, error) {
	if e := presence.ValidatePreferences(v); e != nil {
		return v, e
	}
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e := r.within(ctx, actor, nil, func(tx *sql.Tx, p authority.Principal, now time.Time) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO owned_presence_preferences(account_id,online_visibility,last_seen_visibility,status_visibility,updated_at)VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id)DO UPDATE SET online_visibility=EXCLUDED.online_visibility,last_seen_visibility=EXCLUDED.last_seen_visibility,status_visibility=EXCLUDED.status_visibility,updated_at=EXCLUDED.updated_at`, actor, v.OnlineVisibility, v.LastSeenVisibility, v.StatusVisibility, now)
		if e != nil {
			return e
		}
		return friendeventpg.FencePresence(ctx, tx, p)
	})
	return v, e
}
func (r *Repository) Heartbeat(ctx context.Context, actor string, idle bool) (v presence.Lease, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, p authority.Principal, now time.Time) error {
		v = presence.Lease{LastSeenAt: now, ExpiresAt: now.Add(r.ttl)}
		if p.Session.ExpiresAt.Before(v.ExpiresAt) {
			v.ExpiresAt = p.Session.ExpiresAt
		}
		if !now.Before(v.ExpiresAt) {
			return authority.ErrExpired
		}
		// There is exactly one bounded lease per canonical session, and no caller-chosen timestamp or session ID.
		_, e := tx.ExecContext(ctx, `INSERT INTO owned_presence_sessions(account_id,session_id,epoch,account_version,last_seen_at,expires_at,idle_since)VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $7 THEN $5::timestamptz END) ON CONFLICT(session_id)DO UPDATE SET epoch=EXCLUDED.epoch,account_version=EXCLUDED.account_version,last_seen_at=EXCLUDED.last_seen_at,expires_at=EXCLUDED.expires_at,idle_since=CASE WHEN $7 THEN CASE WHEN owned_presence_sessions.expires_at>$5 AND owned_presence_sessions.epoch=EXCLUDED.epoch THEN COALESCE(owned_presence_sessions.idle_since,$5) ELSE $5 END END`, actor, p.Session.ID, p.Session.Epoch, p.Account.Version, now, v.ExpiresAt, idle)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_presence_last_seen(account_id,last_seen_at)VALUES($1,$2) ON CONFLICT(account_id)DO UPDATE SET last_seen_at=GREATEST(owned_presence_last_seen.last_seen_at,EXCLUDED.last_seen_at)`, actor, now)
		if e != nil {
			return e
		}
		var tz string
		e = tx.QueryRowContext(ctx, `SELECT time_zone FROM owned_accounts_profiles WHERE account_id=$1`, actor).Scan(&tz)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		loc, x := time.LoadLocation(tz)
		if x != nil || tz == "" {
			loc = time.UTC
		}
		id, x := newID()
		if x != nil {
			return x
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_presence_activity_days(id,account_id,day,occurred_at) VALUES($1,$2,$3,$4) ON CONFLICT(account_id,day)DO NOTHING`, id, actor, now.In(loc).Format("2006-01-02"), now)
		if e != nil {
			return e
		}
		return friendeventpg.CaptureConnection(ctx, tx, p)
	})
	return
}
func (r *Repository) Disconnect(ctx context.Context, actor string) error {
	if actor == "" {
		return authority.ErrInvalid
	}
	return r.within(ctx, actor, nil, func(tx *sql.Tx, p authority.Principal, now time.Time) error {
		_, e := tx.ExecContext(ctx, `DELETE FROM owned_presence_sessions WHERE account_id=$1 AND session_id=$2 AND epoch=$3`, actor, p.Session.ID, p.Session.Epoch)
		if e != nil {
			return e
		}
		return friendeventpg.CaptureConnection(ctx, tx, p)
	})
}

const statusColumns = `id::text,account_id::text,attitude,type,label,symbol,app_identifier,is_automated,meta,cleared_at,created_at,updated_at`

func scanStatus(row *sql.Row) (v presence.Status, e error) {
	var meta []byte
	e = row.Scan(&v.ID, &v.AccountID, &v.Attitude, &v.Type, &v.Label, &v.Symbol, &v.AppIdentifier, &v.IsAutomated, &meta, &v.ClearedAt, &v.CreatedAt, &v.UpdatedAt)
	v.Meta = json.RawMessage(meta)
	v.IsCustomized = e == nil
	if len(v.Meta) == 0 {
		v.Meta = json.RawMessage(`null`)
	}
	return
}
func currentStatus(ctx context.Context, tx *sql.Tx, id string, now time.Time) (presence.Status, error) {
	return scanStatus(tx.QueryRowContext(ctx, `SELECT `+statusColumns+` FROM owned_presence_statuses WHERE account_id=$1 AND(cleared_at IS NULL OR cleared_at>$2)ORDER BY created_at DESC,id LIMIT 1`, id, now))
}
func (r *Repository) GetStatus(ctx context.Context, viewer, reference string) (v presence.Status, e error) {
	if reference == "" || len(reference) > 200 {
		return v, presence.ErrInvalid
	}
	e = r.within(ctx, viewer, nil, func(tx *sql.Tx, _ authority.Principal, now time.Time) error {
		v, e = projectStatus(ctx, tx, viewer, reference, now)
		return e
	})
	return
}

// projectStatus is the canonical projection shared by account and friend reads.
func projectStatus(ctx context.Context, tx *sql.Tx, viewer, reference string, now time.Time) (v presence.Status, e error) {
	column := "name"
	if authority.ValidID(reference) {
		column = "id::text"
	}
	var id string
	var created time.Time
	if e := tx.QueryRowContext(ctx, `SELECT id::text,created_at FROM owned_authority_accounts WHERE `+column+`=$1 AND NOT disabled`, reference).Scan(&id, &created); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return v, presence.ErrNotFound
		}
		return v, e
	}
	var blocked, friend bool
	if viewer != "" && viewer != id {
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1 AND related_id=$2)OR(account_id=$2 AND related_id=$1))AND status=-100),(EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2 AND status>=100) AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$2 AND related_id=$1 AND status>=100))`, id, viewer).Scan(&blocked, &friend); e != nil {
			return v, e
		}
	}
	if blocked {
		return v, presence.ErrNotFound
	}
	pref, e := prefs(ctx, tx, id)
	if e != nil {
		return v, e
	}
	allowed := func(s string) bool { return viewer == id || s == "public" || s == "friends" && friend }
	label := "Offline"
	v = presence.Status{ID: id, AccountID: id, Label: &label, Meta: json.RawMessage(`null`), CreatedAt: created, UpdatedAt: created}
	actual, e := currentStatus(ctx, tx, id, now)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return v, e
	}
	invisible := e == nil && actual.Type == presence.Invisible
	if e == nil && allowed(pref.StatusVisibility) {
		v = actual
	}
	if invisible && viewer != id {
		v.Type = presence.Default
	}
	if allowed(pref.OnlineVisibility) && !invisible {
		var n, active int
		var idle sql.NullTime
		e = tx.QueryRowContext(ctx, `SELECT count(*),count(*)FILTER(WHERE p.idle_since IS NULL),max(p.idle_since) FROM owned_presence_sessions p JOIN owned_authority_sessions s ON s.id=p.session_id AND s.account_id=p.account_id JOIN owned_authority_accounts a ON a.id=p.account_id WHERE p.account_id=$1 AND p.expires_at>$2 AND s.expires_at>$2 AND s.revoked_at IS NULL AND NOT a.disabled AND p.epoch=s.epoch AND p.account_version=a.version AND s.account_version=a.version AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=$2)AND(u.expires_at IS NULL OR u.expires_at>$2))`, id, now).Scan(&n, &active, &idle)
		if e != nil {
			return v, e
		}
		v.IsOnline = n > 0
		v.IsIdle = n > 0 && active == 0
		if v.IsIdle && idle.Valid {
			v.IdleSince = &idle.Time
		}
	}
	if allowed(pref.LastSeenVisibility) && (!invisible || viewer == id) {
		var seen time.Time
		e = tx.QueryRowContext(ctx, `SELECT last_seen_at FROM owned_presence_last_seen WHERE account_id=$1`, id).Scan(&seen)
		if e == nil {
			v.LastSeenAt = &seen
		} else if !errors.Is(e, sql.ErrNoRows) {
			return v, e
		}
	}
	if !v.IsCustomized && v.IsOnline {
		label = "Online"
	}
	return v, nil
}
func (r *Repository) SetStatus(ctx context.Context, actor string, req presence.StatusRequest, update bool) (v presence.Status, e error) {
	if e = presence.ValidateRequest(req); e != nil {
		return
	}
	if actor == "" {
		return v, authority.ErrInvalid
	}
	permission := "accounts.statuses.create"
	if update {
		permission = "accounts.statuses.update"
	}
	e = r.within(ctx, actor, []string{permission}, func(tx *sql.Tx, p authority.Principal, now time.Time) error {
		if req.ClearedAt != nil && !now.Before(*req.ClearedAt) {
			return presence.ErrInvalid
		}
		existing, x := currentStatus(ctx, tx, actor, now)
		if x != nil && !errors.Is(x, sql.ErrNoRows) {
			return x
		}
		if update {
			if errors.Is(x, sql.ErrNoRows) {
				return presence.ErrNotFound
			}
			if req.IsAutomated || existing.IsAutomated && req.AppIdentifier == nil {
				return presence.ErrAutomated
			}
		}
		id := ""
		created := now
		if update {
			id = existing.ID
			created = existing.CreatedAt
		} else if req.IsAutomated && x == nil {
			if !existing.IsAutomated {
				v = existing
				return nil
			}
			if existing.AppIdentifier != nil && req.AppIdentifier != nil && *existing.AppIdentifier == *req.AppIdentifier {
				id = existing.ID
				created = existing.CreatedAt
			}
		}
		if id == "" {
			var e error
			id, e = newID()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_presence_statuses SET cleared_at=$2,updated_at=$2 WHERE account_id=$1 AND(cleared_at IS NULL OR cleared_at>$2)`, actor, now); e != nil {
				return e
			}
		}
		var meta any
		if len(req.Meta) > 0 && string(req.Meta) != "null" {
			meta = []byte(req.Meta)
		}
		var e error
		v, e = scanStatus(tx.QueryRowContext(ctx, `INSERT INTO owned_presence_statuses(id,account_id,attitude,type,label,symbol,app_identifier,is_automated,meta,cleared_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)ON CONFLICT(id)DO UPDATE SET attitude=EXCLUDED.attitude,type=EXCLUDED.type,label=EXCLUDED.label,symbol=EXCLUDED.symbol,app_identifier=EXCLUDED.app_identifier,is_automated=EXCLUDED.is_automated,meta=EXCLUDED.meta,cleared_at=EXCLUDED.cleared_at,updated_at=EXCLUDED.updated_at RETURNING `+statusColumns, id, actor, req.Attitude, req.Type, req.Label, req.Symbol, req.AppIdentifier, req.IsAutomated, meta, req.ClearedAt, created, now))
		if e != nil {
			return e
		}
		return friendeventpg.CaptureStatus(ctx, tx, p)
	})
	return
}
func (r *Repository) ClearStatus(ctx context.Context, actor, app string) error {
	if actor == "" {
		return authority.ErrInvalid
	}
	return r.within(ctx, actor, nil, func(tx *sql.Tx, p authority.Principal, now time.Time) error {
		res, e := tx.ExecContext(ctx, `UPDATE owned_presence_statuses SET cleared_at=$2,updated_at=$2 WHERE id=(SELECT id FROM owned_presence_statuses WHERE account_id=$1 AND(cleared_at IS NULL OR cleared_at>$2)AND($3='' OR(is_automated AND app_identifier=$3))ORDER BY created_at DESC,id LIMIT 1)`, actor, now, app)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return presence.ErrNotFound
		}
		return friendeventpg.CaptureStatus(ctx, tx, p)
	})
}
