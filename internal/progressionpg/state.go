// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"sort"
	"strings"
	"time"
)

func available(d progression.Definition, at time.Time) bool {
	return d.IsEnabled && d.IsProgressEnabled && (d.AvailableFrom == nil || !at.Before(*d.AvailableFrom)) && (d.AvailableUntil == nil || !at.After(*d.AvailableUntil))
}

// Period uses local calendar arithmetic, including DST transitions and ISO week-years.
func Period(schedule string, at time.Time, loc *time.Location) (string, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	t := at.In(loc)
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	switch schedule {
	case "weekly":
		day := (int(t.Weekday()) + 6) % 7
		start = start.AddDate(0, 0, -day)
		year, week := start.ISOWeek()
		return fmtWeek(year, week), start.AddDate(0, 0, 7).UTC()
	case "monthly":
		start = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		return start.Format("2006-01"), start.AddDate(0, 1, 0).UTC()
	default:
		return start.Format("2006-01-02"), start.AddDate(0, 0, 1).UTC()
	}
}
func fmtWeek(year, week int) string {
	const digits = "0123456789"
	return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + "-W" + string([]byte{digits[week/10], digits[week%10]})
}

type progress struct {
	count, streak, best int
	last, completed     *time.Time
}

func stateFor(d progression.Definition, p progress, now time.Time) progression.State {
	st := progression.State{Identifier: d.Identifier, Title: d.Title, Summary: d.Summary, Icon: d.Icon, SeriesIdentifier: d.SeriesIdentifier, SeriesTitle: d.SeriesTitle, SeriesOrder: d.SeriesOrder, SortOrder: d.SortOrder, Hidden: d.Hidden, IsEnabled: d.IsEnabled, IsProgressEnabled: d.IsProgressEnabled, IsCurrentlyAvailable: available(d, now), AvailableFrom: d.AvailableFrom, AvailableUntil: d.AvailableUntil, TargetCount: d.TargetCount, ProgressCount: p.count, CurrentStreak: &p.streak, BestStreak: &p.best, SeriesTotalSteps: 1, SeriesStages: []progression.SeriesStage{}, IsCompleted: p.completed != nil, CompletedAt: p.completed, Reward: d.Reward}
	if st.IsCompleted {
		st.SeriesCompletedSteps = 1
	}
	return st
}
func states(ctx context.Context, tx *sql.Tx, actor, kind string, now time.Time) (out []progression.State, e error) {
	ds, e := definitions(ctx, tx, kind)
	if e != nil {
		return nil, e
	}
	loc, e := zone(ctx, tx, actor)
	if e != nil {
		return nil, e
	}
	out = []progression.State{}
	for _, d := range ds {
		if !d.IsEnabled {
			continue
		}
		key := ""
		var reset time.Time
		if kind == "quest" {
			schedule := "daily"
			if d.Schedule != nil {
				schedule = d.Schedule.Repeatability
			}
			key, reset = Period(schedule, now, loc)
		}
		var p progress
		e = tx.QueryRowContext(ctx, `SELECT progress_count,current_streak,best_streak,last_progress_at,completed_at FROM owned_progression_progress WHERE account_id=$1 AND kind=$2 AND identifier=$3 AND period_key=$4`, actor, kind, d.Identifier, key).Scan(&p.count, &p.streak, &p.best, &p.last, &p.completed)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		v := stateFor(d, p, now)
		if kind == "quest" {
			v.CurrentStreak = nil
			v.BestStreak = nil
			v.PeriodKey = &key
			v.NextResetAt = &reset
			v.Schedule = d.Schedule
		}
		out = append(out, v)
	}
	return out, nil
}
func merge(in []progression.State) []progression.State {
	groups := map[string][]progression.State{}
	keys := []string{}
	for _, v := range in {
		key := v.Identifier
		if v.SeriesIdentifier != nil && strings.TrimSpace(*v.SeriesIdentifier) != "" {
			key = *v.SeriesIdentifier
		}
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], v)
	}
	out := []progression.State{}
	for _, key := range keys {
		g := groups[key]
		sort.SliceStable(g, func(i, j int) bool {
			a, b := math.MaxInt, math.MaxInt
			if g[i].SeriesOrder != nil {
				a = *g[i].SeriesOrder
			}
			if g[j].SeriesOrder != nil {
				b = *g[j].SeriesOrder
			}
			if a != b {
				return a < b
			}
			if g[i].SortOrder != g[j].SortOrder {
				return g[i].SortOrder < g[j].SortOrder
			}
			return g[i].Title < g[j].Title
		})
		rep := len(g) - 1
		for i := range g {
			if !g[i].IsCompleted {
				rep = i
				break
			}
		}
		v := g[rep]
		v.SeriesTotalSteps = len(g)
		v.SeriesCompletedSteps = 0
		v.SeriesStages = []progression.SeriesStage{}
		for _, stage := range g {
			if stage.IsCompleted {
				v.SeriesCompletedSteps++
			}
			v.SeriesStages = append(v.SeriesStages, progression.SeriesStage{Identifier: stage.Identifier, Title: stage.Title, SeriesOrder: stage.SeriesOrder, TargetCount: stage.TargetCount, IsCompleted: stage.IsCompleted, CompletedAt: stage.CompletedAt})
		}
		if v.SeriesIdentifier == nil {
			v.SeriesIdentifier = &v.Identifier
		}
		if v.SeriesTitle == nil {
			v.SeriesTitle = &v.Title
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.IsCompleted != b.IsCompleted {
			return !a.IsCompleted
		}
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return a.Title < b.Title
	})
	return out
}
func (r *Repository) States(ctx context.Context, actor, kind, query string) (out []progression.State, e error) {
	if actor == "" {
		return nil, authority.ErrInvalid
	}
	if (kind != "achievement" && kind != "quest") || len(query) > 256 {
		return nil, progression.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = states(ctx, tx, actor, kind, now)
		if e != nil {
			return e
		}
		out = merge(out)
		query = strings.ToLower(strings.TrimSpace(query))
		if query != "" {
			filtered := []progression.State{}
			for _, v := range out {
				hay := v.Identifier + " " + v.Title + " " + v.Summary
				if v.SeriesIdentifier != nil {
					hay += " " + *v.SeriesIdentifier
				}
				if v.SeriesTitle != nil {
					hay += " " + *v.SeriesTitle
				}
				if strings.Contains(strings.ToLower(hay), query) {
					filtered = append(filtered, v)
				}
			}
			out = filtered
		}
		return nil
	})
	return
}
func (r *Repository) AchievementStats(ctx context.Context, actor string) (v progression.Stats, e error) {
	if actor == "" {
		return v, authority.ErrInvalid
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error {
		all, e := states(ctx, tx, actor, "achievement", now)
		if e != nil {
			return e
		}
		for _, s := range all {
			v.TotalCount++
			if s.IsCompleted {
				v.CompletedCount++
			}
			if s.Hidden {
				v.HiddenTotalCount++
				if s.IsCompleted {
					v.HiddenCompletedCount++
				}
			}
		}
		if v.TotalCount > 0 {
			v.CompletionPercentage = math.Round(float64(v.CompletedCount)/float64(v.TotalCount)*10000) / 100
		}
		return nil
	})
	return
}
func (r *Repository) Grants(ctx context.Context, actor string, take int) (out []progression.Grant, e error) {
	out = []progression.Grant{}
	if actor == "" {
		return out, authority.ErrInvalid
	}
	if take < 1 {
		take = 1
	}
	if take > 100 {
		take = 100
	}
	e = r.within(ctx, actor, nil, func(tx *sql.Tx, _ time.Time) error {
		rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text,kind,identifier,title,reward_token,source_event_id,period_key,reward,badge_granted_at,experience_granted_at,source_points_granted_at,notification_sent_at,created_at,updated_at FROM owned_progression_grants WHERE account_id=$1 ORDER BY created_at DESC,id LIMIT $2`, actor, take)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v progression.Grant
			var raw []byte
			if e = rows.Scan(&v.ID, &v.AccountID, &v.DefinitionType, &v.DefinitionIdentifier, &v.DefinitionTitle, &v.RewardToken, &v.SourceEventID, &v.PeriodKey, &raw, &v.BadgeGrantedAt, &v.ExperienceGrantedAt, &v.SourcePointsGrantedAt, &v.NotificationSentAt, &v.CreatedAt, &v.UpdatedAt); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &v.Reward); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
