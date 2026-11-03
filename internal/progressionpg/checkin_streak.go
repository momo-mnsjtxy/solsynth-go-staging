// SPDX-License-Identifier: AGPL-3.0-only
// Daily check-ins earn UTC streak tiers; the existing grant ledger records them
// once and the funded wallet worker pays them independently of check-in success.
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"solsynth.local/go-migration/ports/passport/progression"
)

const checkInStreakKind = "check-in-streak"

func checkInStreakGoldReward(streak int) int64 {
	if streak <= 0 {
		return 0
	}
	var gold int64
	if streak%7 == 0 {
		gold++
	}
	if streak%30 == 0 {
		gold += 5
	}
	return gold
}

// NewFundedCheckInStreakGold retains the canonical wallet transfer and budget
// checks. Gold has its own explicit treasury policy and cannot mint balances.
func NewFundedCheckInStreakGold(c SourcePointConfig) (*FundedSourcePoints, error) {
	if c.Currency != "golds" || c.MaxPerGrant < 6 {
		return nil, progression.ErrInvalid
	}
	return NewFundedSourcePoints(c)
}

func (r *Repository) WithCheckInStreakGoldPayout(p SourcePointPayout) error {
	if p == nil || p.SourcePointCurrency() != "golds" {
		return progression.ErrInvalid
	}
	r.checkinGold = p
	return nil
}

func recordCheckInStreakGold(ctx context.Context, tx *sql.Tx, actor, checkin string, now time.Time) error {
	day := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	// Walk the existing day index until the first gap. A fixed lookback would
	// turn every longer streak into the same repeated tier and overpay gold.
	rows, e := tx.QueryContext(ctx, `SELECT day::text FROM owned_progression_checkins WHERE account_id=$1::uuid AND day<=$2::date ORDER BY day DESC`, actor, day.Format("2006-01-02"))
	if e != nil {
		return e
	}
	streak := 0
	for rows.Next() {
		var stored string
		if e = rows.Scan(&stored); e != nil {
			break
		}
		if stored != day.AddDate(0, 0, -streak).Format("2006-01-02") {
			break
		}
		streak++
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	gold := checkInStreakGoldReward(streak)
	if gold == 0 {
		return nil
	}
	id, e := newID()
	if e != nil {
		return e
	}
	currency := "golds"
	reward, e := json.Marshal(progression.Reward{SourcePoints: gold, SourcePointsCurrency: &currency})
	if e != nil {
		return e
	}
	key := day.Format("2006-01-02")
	title := fmt.Sprintf("Check-in streak reward (%d days) on %s", streak, day.Format("2006/01/02"))
	// The same actor authority lock serializes daily and backdated check-ins.
	// The unique token also protects replays across processes and restarts.
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_grants(id,account_id,kind,identifier,title,reward_token,source_event_id,period_key,reward,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::text,'golds',$4::text,$5::text,$6::text,$7::text,$8::jsonb,$9::timestamptz,$9::timestamptz)ON CONFLICT(reward_token)DO NOTHING`, id, actor, checkInStreakKind, title, "check-in-streak:"+actor+":"+key, checkin, key, reward, now)
	return e
}

// ProcessCheckInStreakGold retries only earned gold tiers. A wallet failure
// leaves the grant pending; it never rolls back a successful daily check-in.
func (r *Repository) ProcessCheckInStreakGold(ctx context.Context, limit int) (int, error) {
	if r == nil {
		return 0, nil
	}
	return r.processPointGrants(ctx, limit, r.checkinGold, true)
}
