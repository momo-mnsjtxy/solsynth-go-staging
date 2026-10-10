// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/auditcapture"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	points        SourcePointPayout
	checkinPolicy *CheckInPolicy
	checkinPerks  CheckInPerks
	db            *sql.DB
	guard         TransactionGuard
	reports       CheckInReports
}

// CheckInReports shares the canonical transaction. Implementations must not
// create another check-in/XP authority or contact a provider inside these hooks.
type CheckInReports interface {
	EnqueueCheckIn(context.Context, *sql.Tx, progression.CheckIn, time.Time) error
	HydrateCheckIn(context.Context, *sql.Tx, *progression.CheckIn) error
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewWithAuthority(db *sql.DB, g TransactionGuard) *Repository {
	return &Repository{db: db, guard: g}
}

func NewWithCheckInReports(db *sql.DB, g TransactionGuard, reports CheckInReports) *Repository {
	return &Repository{db: db, guard: g, reports: reports}
}

var _ progression.Repository = (*Repository)(nil)

func (r *Repository) within(ctx context.Context, actor string, permissions []string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return progression.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor != "" {
		if r.guard == nil {
			return authority.ErrUnavailable
		}
		if len(permissions) == 0 {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...)
		}
		if e != nil {
			return e
		}
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return mapped(e)
	}
	if actor != "" {
		if len(permissions) == 0 {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...)
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return progression.ErrNotFound
	}
	return e
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
func audit(ctx context.Context, tx *sql.Tx, actor, action, target string, now time.Time) error {
	var id int64
	if e := tx.QueryRowContext(ctx, `INSERT INTO owned_progression_audit(actor_id,action,target,occurred_at)VALUES($1,$2,$3,$4)RETURNING id`, actor, action, target, now).Scan(&id); e != nil {
		return e
	}
	return auditcapture.Record(ctx, tx, actor, "owned_progression_audit", id)
}
func zone(ctx context.Context, tx *sql.Tx, actor string) (*time.Location, error) {
	var name string
	e := tx.QueryRowContext(ctx, `SELECT time_zone FROM owned_accounts_profiles WHERE account_id=$1`, actor).Scan(&name)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	loc, e := time.LoadLocation(name)
	if e != nil || name == "" {
		loc = time.UTC
	}
	return loc, nil
}
func definitions(ctx context.Context, tx *sql.Tx, kind string) ([]progression.Definition, error) {
	rows, e := tx.QueryContext(ctx, `SELECT definition FROM owned_progression_definitions WHERE kind=$1 ORDER BY identifier`, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []progression.Definition{}
	for rows.Next() {
		var b []byte
		var d progression.Definition
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].Title < out[j].Title
	})
	return out, rows.Err()
}
func (r *Repository) Definitions(ctx context.Context, actor, kind string) (out []progression.Definition, e error) {
	if actor == "" {
		return nil, authority.ErrInvalid
	}
	if kind != "achievement" && kind != "quest" {
		return nil, progression.ErrInvalid
	}
	e = r.within(ctx, actor, []string{"progression.admin"}, func(tx *sql.Tx, _ time.Time) error { var e error; out, e = definitions(ctx, tx, kind); return e })
	return
}
func insertXP(ctx context.Context, tx *sql.Tx, actor, reasonType, reason, token string, delta int64, now time.Time) error {
	if delta <= 0 {
		return nil
	}
	id, e := newID()
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_experience(id,account_id,reason_type,reason,delta,reward_token,created_at)VALUES($1,$2,$3,$4,$5,$6,$7)ON CONFLICT(reward_token)DO NOTHING`, id, actor, reasonType, reason, delta, token, now)
	return e
}
func (r *Repository) Experience(ctx context.Context, actor string, offset, take int) (out []progression.ExperienceRecord, total int64, e error) {
	out = []progression.ExperienceRecord{}
	if actor == "" {
		return out, 0, authority.ErrInvalid
	}
	if !progression.ValidPage(offset, take) {
		return out, 0, progression.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, _ time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_progression_experience WHERE account_id=$1`, actor).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text,reason_type,reason,delta,bonus_multiplier,created_at,created_at FROM owned_progression_experience WHERE account_id=$1 ORDER BY created_at DESC,id OFFSET $2 LIMIT $3`, actor, offset, take)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v progression.ExperienceRecord
			if e = rows.Scan(&v.ID, &v.AccountID, &v.ReasonType, &v.Reason, &v.Delta, &v.BonusMultiplier, &v.CreatedAt, &v.UpdatedAt); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
func (r *Repository) Summary(ctx context.Context, actor string) (v progression.Summary, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(delta),0) FROM owned_progression_experience WHERE account_id=$1`, actor).Scan(&v.Experience); e != nil {
			return e
		}
		var raw []byte
		e := tx.QueryRowContext(ctx, `SELECT thresholds FROM owned_progression_level_policy WHERE id=1`).Scan(&raw)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil {
			var thresholds []int64
			if e = json.Unmarshal(raw, &thresholds); e != nil {
				return e
			}
			if e = validateThresholds(thresholds); e != nil {
				return e
			}
			n := sort.Search(len(thresholds), func(i int) bool { return thresholds[i] > v.Experience })
			level := n
			fraction := 1.0
			if n < len(thresholds) {
				fraction = float64(v.Experience-thresholds[n-1]) / float64(thresholds[n]-thresholds[n-1])
			}
			v.Level = &level
			v.LevelingProgress = &fraction
			v.LevelPolicyConfigured = true
		}
		b, e := scanBadge(tx.QueryRowContext(ctx, `SELECT `+badgeColumns+` FROM owned_progression_badges WHERE account_id=$1 AND activated_at IS NOT NULL AND deleted_at IS NULL AND(expired_at IS NULL OR expired_at>$2)`, actor, now))
		if e == nil {
			v.ActiveBadge = &b
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return nil
	})
	return
}
func validateThresholds(v []int64) error {
	if len(v) < 1 || len(v) > 10000 || v[0] != 0 {
		return progression.ErrInvalid
	}
	for i, n := range v {
		if n < 0 || n > 1e15 || i > 0 && n <= v[i-1] {
			return progression.ErrInvalid
		}
	}
	return nil
}
func (r *Repository) SetLevelPolicy(ctx context.Context, actor string, v []int64) error {
	if actor == "" {
		return authority.ErrInvalid
	}
	if e := validateThresholds(v); e != nil {
		return e
	}
	raw, _ := json.Marshal(v)
	return r.within(ctx, actor, []string{"progression.admin", "progression.levels.manage"}, func(tx *sql.Tx, now time.Time) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO owned_progression_level_policy(id,thresholds,updated_at)VALUES(1,$1,$2)ON CONFLICT(id)DO UPDATE SET thresholds=EXCLUDED.thresholds,updated_at=EXCLUDED.updated_at`, raw, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "levels.policy", "global", now)
	})
}
