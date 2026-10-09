// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/passport/progression"
	"time"
)

type CheckInPerks interface {
	CheckInPerkInTransaction(context.Context, *sql.Tx, string, []string, time.Time) (bool, error)
}

func (r *Repository) WithCheckInPerks(p CheckInPerks) *Repository { r.checkinPerks = p; return r }

type CheckInPolicy struct {
	BackdateWindowDays int
	PerkProducts       []string
	BirthdayExperience int64
}

func ValidateCheckInPolicy(p CheckInPolicy) error {
	if p.BackdateWindowDays < 0 || p.BackdateWindowDays > 366 || p.BirthdayExperience < 0 || p.BirthdayExperience > 1000000 || len(p.PerkProducts) > 32 || p.BackdateWindowDays > 0 && len(p.PerkProducts) == 0 {
		return progression.ErrInvalid
	}
	seen := map[string]bool{}
	for _, v := range p.PerkProducts {
		if !identifier.MatchString(v) || seen[v] {
			return progression.ErrInvalid
		}
		seen[v] = true
	}
	return nil
}
func (r *Repository) WithCheckInPolicy(p CheckInPolicy) error {
	if e := ValidateCheckInPolicy(p); e != nil {
		return e
	}
	p.PerkProducts = append([]string{}, p.PerkProducts...)
	r.checkinPolicy = &p
	return nil
}
func (r *Repository) BackdatedCheckIn(ctx context.Context, actor string, at time.Time) (progression.CheckIn, error) {
	return r.checkInAt(ctx, actor, &at)
}
func (r *Repository) eligibleBackdate(ctx context.Context, tx *sql.Tx, actor string, at, now time.Time) error {
	if r.checkinPolicy == nil || r.checkinPolicy.BackdateWindowDays == 0 {
		return progression.ErrUnavailable
	}
	day := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !day.Before(today) || day.Before(today.AddDate(0, 0, -r.checkinPolicy.BackdateWindowDays)) {
		return progression.ErrInvalid
	}
	var created time.Time
	if e := tx.QueryRowContext(ctx, `SELECT created_at FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&created); e != nil {
		return e
	}
	if day.Before(time.Date(created.UTC().Year(), created.UTC().Month(), created.UTC().Day(), 0, 0, 0, 0, time.UTC)) {
		return progression.ErrInvalid
	}
	if r.checkinPerks == nil {
		return progression.ErrUnavailable
	}
	entitled, e := r.checkinPerks.CheckInPerkInTransaction(ctx, tx, actor, r.checkinPolicy.PerkProducts, now)
	if e != nil {
		return e
	}
	if !entitled {
		return progression.ErrForbidden
	}
	return nil
}
func (r *Repository) birthdayReward(ctx context.Context, tx *sql.Tx, actor string, now time.Time) error {
	if r.checkinPolicy == nil || r.checkinPolicy.BirthdayExperience == 0 {
		return nil
	}
	var birthday *time.Time
	if e := tx.QueryRowContext(ctx, `SELECT birthday FROM owned_accounts_profiles WHERE account_id=$1::uuid`, actor).Scan(&birthday); e == sql.ErrNoRows {
		return nil
	} else if e != nil {
		return e
	}
	if birthday == nil || !birthday.Before(now) {
		return nil
	}
	loc, e := zone(ctx, tx, actor)
	if e != nil {
		return e
	}
	local := now.In(loc)
	born := birthday.UTC()
	if local.Month() != born.Month() || local.Day() != born.Day() {
		return nil
	}
	return insertXP(ctx, tx, actor, "birthday", "Birthday reward", "birthday:"+actor+":"+local.Format("2006"), r.checkinPolicy.BirthdayExperience, now)
}
func (r *Repository) SyncSeeds(ctx context.Context, actor string) (count int, e error) {
	seeds, e := progression.Seeds()
	if e != nil {
		return 0, e
	}
	e = r.within(ctx, actor, []string{"progression.admin", "progression.sync"}, func(tx *sql.Tx, now time.Time) error {
		for _, s := range seeds {
			if e := ValidateDefinition(s.Kind, s.Definition); e != nil {
				return e
			}
			raw, e := json.Marshal(s.Definition)
			if e != nil {
				return e
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO owned_progression_definitions(kind,identifier,definition,updated_at)VALUES($1::text,$2::text,$3::jsonb,$4::timestamptz)ON CONFLICT(kind,identifier)DO UPDATE SET definition=EXCLUDED.definition,updated_at=EXCLUDED.updated_at WHERE COALESCE((owned_progression_definitions.definition->>'is_seed_managed')::boolean,false)`, s.Kind, s.Definition.Identifier, raw, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			count += int(n)
		}
		return audit(ctx, tx, actor, "seeds.sync", "global", now)
	})
	return
}
