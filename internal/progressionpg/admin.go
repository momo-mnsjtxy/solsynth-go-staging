// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"time"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func ValidateDefinition(kind string, d progression.Definition) error {
	if (kind != "achievement" && kind != "quest") || !identifier.MatchString(d.Identifier) || len(strings.TrimSpace(d.Title)) == 0 || len(d.Title) > 1024 || len(d.Summary) > 8192 || d.TargetCount < 1 || d.TargetCount > 1000000 || d.Reward.Experience < 0 || d.Reward.Experience > 1000000000 || d.Reward.SourcePoints < 0 || d.Reward.SourcePoints > 1000000000 {
		return progression.ErrInvalid
	}
	if d.AvailableFrom != nil && d.AvailableUntil != nil && d.AvailableUntil.Before(*d.AvailableFrom) {
		return progression.ErrInvalid
	}
	if len(d.Trigger.Actions) < 1 || len(d.Trigger.Actions) > 32 {
		return progression.ErrInvalid
	}
	for _, a := range d.Trigger.Actions {
		if !identifier.MatchString(a) {
			return progression.ErrInvalid
		}
	}
	if d.Trigger.Mode != "count" && d.Trigger.Mode != "streak" && d.Trigger.Mode != "daily_count" {
		return progression.ErrInvalid
	}
	if kind == "quest" && (d.Schedule == nil || (d.Schedule.Repeatability != "daily" && d.Schedule.Repeatability != "weekly" && d.Schedule.Repeatability != "monthly")) {
		return progression.ErrInvalid
	}
	if d.Reward.Badge != nil && !badgeKnown(d.Reward.Badge.Type) {
		return progression.ErrInvalid
	}
	raw, e := json.Marshal(d)
	if e != nil || len(raw) > 32768 {
		return progression.ErrInvalid
	}
	return nil
}
func managePermission(kind string) string {
	if kind == "quest" {
		return "progression.quests.manage"
	}
	return "progression.achievements.manage"
}
func (r *Repository) SaveDefinition(ctx context.Context, actor, kind, pathID string, d progression.Definition, create bool) (progression.Definition, error) {
	if actor == "" {
		return d, authority.ErrInvalid
	}
	if e := ValidateDefinition(kind, d); e != nil {
		return d, e
	}
	if !create && pathID != d.Identifier {
		return d, progression.ErrInvalid
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return d, e
	}
	e = r.within(ctx, actor, []string{"progression.admin", managePermission(kind)}, func(tx *sql.Tx, now time.Time) error {
		if create {
			res, e := tx.ExecContext(ctx, `INSERT INTO owned_progression_definitions(kind,identifier,definition,updated_at)VALUES($1,$2,$3,$4)ON CONFLICT(kind,identifier)DO NOTHING`, kind, d.Identifier, raw, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				return progression.ErrConflict
			}
		} else {
			res, e := tx.ExecContext(ctx, `UPDATE owned_progression_definitions SET definition=$3,updated_at=$4 WHERE kind=$1 AND identifier=$2`, kind, d.Identifier, raw, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				return progression.ErrNotFound
			}
		}
		return audit(ctx, tx, actor, "definition.save", kind+":"+d.Identifier, now)
	})
	return d, e
}
func (r *Repository) EnableDefinition(ctx context.Context, actor, kind, id string, enabled bool) error {
	if actor == "" {
		return authority.ErrInvalid
	}
	if (kind != "achievement" && kind != "quest") || !identifier.MatchString(id) {
		return progression.ErrInvalid
	}
	return r.within(ctx, actor, []string{"progression.admin", managePermission(kind)}, func(tx *sql.Tx, now time.Time) error {
		res, e := tx.ExecContext(ctx, `UPDATE owned_progression_definitions SET definition=jsonb_set(definition,'{is_enabled}',to_jsonb($3::boolean)),updated_at=$4 WHERE kind=$1 AND identifier=$2`, kind, id, enabled, now)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return progression.ErrNotFound
		}
		return audit(ctx, tx, actor, "definition.enable", kind+":"+id, now)
	})
}
