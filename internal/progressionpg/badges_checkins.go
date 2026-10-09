// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/badgediscovery"
	"solsynth.local/go-migration/ports/passport/progression"
	"time"
)

const badgeColumns = `id::text,type,label,caption,meta,expired_at,account_id::text,created_at,updated_at,activated_at,deleted_at`

type scanner interface{ Scan(...any) error }

func scanBadge(row scanner) (v progression.Badge, e error) {
	e = row.Scan(&v.ID, &v.Type, &v.Label, &v.Caption, &v.Meta, &v.ExpiredAt, &v.AccountID, &v.CreatedAt, &v.UpdatedAt, &v.ActivatedAt, &v.DeletedAt)
	return
}
func badgeKnown(kind string) bool {
	for _, b := range badgediscovery.Catalog(false).Badges {
		if b.Identifier == kind {
			return true
		}
	}
	return false
}
func grantBadge(ctx context.Context, tx *sql.Tx, actor string, reward progression.BadgeReward, now time.Time) error {
	if !badgeKnown(reward.Type) {
		return progression.ErrInvalid
	}
	id, e := newID()
	if e != nil {
		return e
	}
	meta := reward.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	raw, e := json.Marshal(meta)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_badges(id,account_id,type,label,caption,meta,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7)ON CONFLICT(account_id,type)DO NOTHING`, id, actor, reward.Type, reward.Label, reward.Caption, raw, now)
	return e
}
func (r *Repository) Badges(ctx context.Context, viewer, reference string) (out []progression.Badge, e error) {
	out = []progression.Badge{}
	if reference == "" || len(reference) > 200 {
		return out, progression.ErrInvalid
	}
	e = r.within(ctx, viewer, nil, func(tx *sql.Tx, now time.Time) error {
		column := "name"
		if authority.ValidID(reference) {
			column = "id::text"
		}
		var id string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE `+column+`=$1 AND NOT disabled`, reference).Scan(&id); e != nil {
			return e
		}
		if viewer != "" && viewer != id {
			var blocked bool
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1 AND related_id=$2)OR(account_id=$2 AND related_id=$1))AND status=-100)`, viewer, id).Scan(&blocked); e != nil {
				return e
			}
			if blocked {
				return progression.ErrNotFound
			}
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+badgeColumns+` FROM owned_progression_badges WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC,id`, id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanBadge(rows)
			if e != nil {
				return e
			}
			if v.ExpiredAt != nil && !now.Before(*v.ExpiredAt) {
				v.ActivatedAt = nil
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
func (r *Repository) ActivateBadge(ctx context.Context, actor, id string) error {
	if actor == "" || !authority.ValidID(id) {
		return authority.ErrInvalid
	}
	return r.within(ctx, actor, []string{"progression.badges.manage"}, func(tx *sql.Tx, now time.Time) error {
		var target string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_progression_badges WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL AND(expired_at IS NULL OR expired_at>$3)FOR UPDATE`, id, actor, now).Scan(&target); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=NULL,updated_at=$2 WHERE account_id=$1 AND activated_at IS NOT NULL`, actor, now); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=$2,updated_at=$2 WHERE id=$1`, id, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "badge.activate", id, now)
	})
}

const checkinColumns = `id::text,account_id::text,level,reward_experience,created_at,created_at`

func scanCheckin(row scanner) (v progression.CheckIn, e error) {
	v.Tips = []any{}
	e = row.Scan(&v.ID, &v.AccountID, &v.Level, &v.RewardExperience, &v.CreatedAt, &v.UpdatedAt)
	return
}
func (r *Repository) Today(ctx context.Context, actor string) (v progression.CheckIn, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error {
		var e error
		v, e = scanCheckin(tx.QueryRowContext(ctx, `SELECT `+checkinColumns+` FROM owned_progression_checkins WHERE account_id=$1 AND day=$2`, actor, now.Format("2006-01-02")))
		if e == nil && r.reports != nil {
			e = r.reports.HydrateCheckIn(ctx, tx, &v)
		}
		return e
	})
	return
}
func (r *Repository) CheckIn(ctx context.Context, actor string) (v progression.CheckIn, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error {
		day := now.Format("2006-01-02")
		var e error
		v, e = scanCheckin(tx.QueryRowContext(ctx, `SELECT `+checkinColumns+` FROM owned_progression_checkins WHERE account_id=$1 AND day=$2`, actor, day))
		if e == nil {
			if r.reports != nil {
				if e = r.reports.EnqueueCheckIn(ctx, tx, v, now); e != nil {
					return e
				}
				return r.reports.HydrateCheckIn(ctx, tx, &v)
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		id, e := newID()
		if e != nil {
			return e
		}
		n, e := rand.Int(rand.Reader, big.NewInt(100))
		if e != nil {
			return e
		}
		level := 4
		switch x := n.Int64(); {
		case x < 10:
			level = 0
		case x < 30:
			level = 1
		case x < 70:
			level = 2
		case x < 90:
			level = 3
		}
		v, e = scanCheckin(tx.QueryRowContext(ctx, `INSERT INTO owned_progression_checkins(id,account_id,day,level,reward_experience,created_at)VALUES($1,$2,$3,$4,100,$5)RETURNING `+checkinColumns, id, actor, day, level, now))
		if e != nil {
			return e
		}
		if e = insertXP(ctx, tx, actor, "check-in", "Check-in reward on "+now.Format("2006/01/02"), "check-in:"+actor+":"+day, 100, now); e != nil {
			return e
		}
		if r.reports != nil {
			if e = r.reports.EnqueueCheckIn(ctx, tx, v, now); e != nil {
				return e
			}
			if e = r.reports.HydrateCheckIn(ctx, tx, &v); e != nil {
				return e
			}
		}
		return audit(ctx, tx, actor, "check-in", id, now)
	})
	return
}
func (r *Repository) CheckInHistory(ctx context.Context, actor string, offset, take int) (out []progression.CheckIn, total int64, e error) {
	out = []progression.CheckIn{}
	if actor == "" {
		return out, 0, authority.ErrInvalid
	}
	if !progression.ValidPage(offset, take) {
		return out, 0, progression.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, _ time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_progression_checkins WHERE account_id=$1`, actor).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+checkinColumns+` FROM owned_progression_checkins WHERE account_id=$1 ORDER BY day DESC,id OFFSET $2 LIMIT $3`, actor, offset, take)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanCheckin(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		rows.Close()
		if r.reports != nil {
			for i := range out {
				if e = r.reports.HydrateCheckIn(ctx, tx, &out[i]); e != nil {
					return e
				}
			}
		}
		return nil
	})
	return
}
