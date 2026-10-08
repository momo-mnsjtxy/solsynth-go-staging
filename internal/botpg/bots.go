// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"time"
)

type details struct {
	FirstName  *string    `json:"first_name"`
	MiddleName *string    `json:"middle_name"`
	LastName   *string    `json:"last_name"`
	Gender     *string    `json:"gender"`
	Birthday   *time.Time `json:"birthday"`
}
type botRow struct {
	bot              bots.Bot
	active, disabled bool
	version          int64
	controlled       sql.NullInt64
	details          details
}

func mutableBot(ctx context.Context, tx *sql.Tx, row botRow, now time.Time) error {
	if row.disabled && (!row.controlled.Valid || row.controlled.Int64 != row.version) {
		return bots.ErrForbidden
	}
	var punished bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1 AND kind='disable_account' AND(starts_at IS NULL OR starts_at<=$2)AND(expires_at IS NULL OR expires_at>$2))`, row.bot.Account.ID, now).Scan(&punished); e != nil {
		return e
	}
	if punished {
		return bots.ErrForbidden
	}
	return nil
}

func loadBot(ctx context.Context, tx *sql.Tx, app, id string) (v botRow, e error) {
	var raw, links []byte
	e = tx.QueryRowContext(ctx, `SELECT b.id::text,b.slug,b.is_active,b.app_id::text,b.project_id::text,b.created_at,b.updated_at,b.deleted_at,b.details,b.controlled_disabled_version,account.id::text,account.name,account.nick,account.language,account.region,account.created_at,account.disabled,account.version,profile.id::text,profile.bio,profile.pronouns,profile.location,profile.time_zone,profile.links FROM owned_bots b JOIN owned_authority_accounts account ON account.id=b.account_id JOIN owned_accounts_profiles profile ON profile.account_id=account.id WHERE b.id=$1 AND b.app_id=$2`, id, app).Scan(&v.bot.ID, &v.bot.Slug, &v.active, &v.bot.AppID, &v.bot.ProjectID, &v.bot.CreatedAt, &v.bot.UpdatedAt, &v.bot.DeletedAt, &raw, &v.controlled, &v.bot.Account.ID, &v.bot.Account.Name, &v.bot.Account.Nick, &v.bot.Account.Language, &v.bot.Account.Region, &v.bot.Account.CreatedAt, &v.disabled, &v.version, &v.bot.Account.Profile.ID, &v.bot.Account.Profile.Bio, &v.bot.Account.Profile.Pronouns, &v.bot.Account.Profile.Location, &v.bot.Account.Profile.TimeZone, &links)
	if e != nil {
		return v, mapped(e)
	}
	if e = json.Unmarshal(raw, &v.details); e != nil {
		return v, e
	}
	if e = json.Unmarshal(links, &v.bot.Account.Profile.Links); e != nil {
		return v, e
	}
	v.bot.IsActive = v.active && !v.disabled
	v.bot.Account.AutomatedID = v.bot.ID
	v.bot.Account.UpdatedAt = v.bot.UpdatedAt
	v.bot.Account.Profile.FirstName = v.details.FirstName
	v.bot.Account.Profile.MiddleName = v.details.MiddleName
	v.bot.Account.Profile.LastName = v.details.LastName
	v.bot.Account.Profile.Gender = v.details.Gender
	v.bot.Account.Profile.Birthday = v.details.Birthday
	return v, nil
}
func mergeDetails(old details, p bots.Patch) details {
	if p.FirstName != nil {
		old.FirstName = p.FirstName
	}
	if p.MiddleName != nil {
		old.MiddleName = p.MiddleName
	}
	if p.LastName != nil {
		old.LastName = p.LastName
	}
	if p.Gender != nil {
		old.Gender = p.Gender
	}
	if p.Birthday != nil {
		old.Birthday = p.Birthday
	}
	return old
}
func textOr(p *string, fallback string) string {
	if p != nil {
		return *p
	}
	return fallback
}
func (r *Repository) Bots(ctx context.Context, s apps.Scope, ref string, p *bots.Patch, action string) (out []bots.Bot, e error) {
	out = []bots.Bot{}
	permission := ""
	role := 0
	switch action {
	case "read":
		if !authority.ValidID(ref) {
			return out, bots.ErrInvalid
		}
	case "create", "update", "delete":
		permission = "developers.bots." + action
		role = 50
	default:
		return out, bots.ErrInvalid
	}
	if action == "create" && ref != "" || action != "create" && !authority.ValidID(ref) {
		return out, bots.ErrInvalid
	}
	if action == "create" || action == "update" {
		if p == nil {
			return out, bots.ErrInvalid
		}
		if e = bots.ValidatePatch(*p, action == "create"); e != nil {
			return out, e
		}
	}
	e = r.within(ctx, s, ref, permission, role, func(tx *sql.Tx, scope appScope, now time.Time) error {
		if action == "create" {
			if e := capacity(ctx, tx, scope.publisher, false, now); e != nil {
				return e
			}
			botID, e := newID()
			if e != nil {
				return e
			}
			accountID, e := newID()
			if e != nil {
				return e
			}
			profileID, e := newID()
			if e != nil {
				return e
			}
			active := true
			if p.IsActive != nil {
				active = *p.IsActive
			}
			var controlled any
			if !active {
				controlled = int64(1)
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_accounts(id,name,nick,language,disabled,created_at)VALUES($1,$2,$3,$4,$5,$6)`, accountID, *p.Name, *p.Nick, textOr(p.Language, "en-us"), !active, now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_accounts_profiles(account_id,id,bio,pronouns,location,time_zone,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7)`, accountID, profileID, textOr(p.Bio, ""), textOr(p.Pronouns, ""), textOr(p.Location, ""), textOr(p.TimeZone, ""), now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_bots(id,account_id,app_id,project_id,slug,is_active,details,controlled_disabled_version,created_by,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, botID, accountID, s.App, s.Project, *p.Slug, active, encoded(mergeDetails(details{}, *p)), controlled, s.Actor, now); e != nil {
				return e
			}
			ref = botID
		} else {
			row, e := loadBot(ctx, tx, s.App, ref)
			if e != nil {
				return e
			}
			if row.bot.DeletedAt != nil {
				if action == "delete" {
					return nil
				}
				return bots.ErrNotFound
			}
			if row.bot.Account.ID == s.Actor {
				return bots.ErrForbidden
			}
			if action == "delete" {
				if _, e = tx.ExecContext(ctx, `UPDATE owned_bots SET is_active=false,deleted_at=$2,updated_at=$2 WHERE id=$1`, ref, now); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, row.bot.Account.ID); e != nil {
					return e
				}
				return audit(ctx, tx, s, "bot.delete", ref, now)
			}
			if action == "update" {
				if e = mutableBot(ctx, tx, row, now); e != nil {
					return e
				}
				active := row.active
				if p.IsActive != nil {
					active = *p.IsActive
				}
				disabled := !active
				var nextControlled any
				if disabled {
					nextControlled = row.version + 1
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET name=$2,nick=$3,language=$4,disabled=$5,version=version+1 WHERE id=$1`, row.bot.Account.ID, textOr(p.Name, row.bot.Account.Name), textOr(p.Nick, row.bot.Account.Nick), textOr(p.Language, row.bot.Account.Language), disabled); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_accounts_profiles SET bio=$2,pronouns=$3,location=$4,time_zone=$5,updated_at=$6 WHERE account_id=$1`, row.bot.Account.ID, textOr(p.Bio, row.bot.Account.Profile.Bio), textOr(p.Pronouns, row.bot.Account.Profile.Pronouns), textOr(p.Location, row.bot.Account.Profile.Location), textOr(p.TimeZone, row.bot.Account.Profile.TimeZone), now); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE owned_bots SET slug=$2,is_active=$3,details=$4,controlled_disabled_version=$5,updated_at=$6 WHERE id=$1`, ref, textOr(p.Slug, row.bot.Slug), active, encoded(mergeDetails(row.details, *p)), nextControlled, now); e != nil {
					return e
				}
			}
		}
		row, e := loadBot(ctx, tx, s.App, ref)
		if e != nil {
			return e
		}
		out = append(out, row.bot)
		if action == "read" {
			return nil
		}
		return audit(ctx, tx, s, "bot."+action, ref, now)
	})
	return
}
func (r *Repository) ListBots(ctx context.Context, s apps.Scope, offset, take int) (out []bots.Bot, total int64, e error) {
	out = []bots.Bot{}
	if offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return out, 0, bots.ErrInvalid
	}
	e = r.within(ctx, s, "", "", 0, func(tx *sql.Tx, _ appScope, _ time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_bots WHERE app_id=$1 AND deleted_at IS NULL`, s.App).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_bots WHERE app_id=$1 AND deleted_at IS NULL ORDER BY created_at,id OFFSET $2 LIMIT $3`, s.App, offset, take)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			v, e := loadBot(ctx, tx, s.App, id)
			if e != nil {
				return e
			}
			out = append(out, v.bot)
		}
		return nil
	})
	return
}
func publicBotApp(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	if !authority.ValidID(id) {
		return "", bots.ErrInvalid
	}
	var app string
	e := tx.QueryRowContext(ctx, `SELECT b.app_id::text FROM owned_bots b JOIN owned_custom_apps a ON a.id=b.app_id JOIN owned_dev_projects project ON project.id=a.project_id JOIN owned_developers d ON d.id=project.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id JOIN owned_authority_accounts account ON account.id=b.account_id WHERE b.id=$1 AND b.deleted_at IS NULL AND b.is_active AND NOT account.disabled AND `+publicAncestry+` AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=account.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp()))`, id).Scan(&app)
	return app, mapped(e)
}
func (r *Repository) PublicBot(ctx context.Context, id string) (v bots.Bot, e error) {
	e = r.public(ctx, func(tx *sql.Tx, _ time.Time) error {
		app, e := publicBotApp(ctx, tx, id)
		if e != nil {
			return e
		}
		row, e := loadBot(ctx, tx, app, id)
		if e != nil {
			return e
		}
		v = row.bot
		d, e := developer(ctx, tx, app)
		if e != nil {
			return e
		}
		v.Developer = &d
		return nil
	})
	return
}
func (r *Repository) PublicDeveloper(ctx context.Context, id string) (v bots.Developer, e error) {
	e = r.public(ctx, func(tx *sql.Tx, _ time.Time) error {
		app, e := publicBotApp(ctx, tx, id)
		if e != nil {
			return e
		}
		v, e = developer(ctx, tx, app)
		return e
	})
	return
}
