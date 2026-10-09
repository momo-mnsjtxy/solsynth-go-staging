// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accounthistorypg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/signinpg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/presence"
	"sort"
	"time"
)

func adminAccount(ctx context.Context, tx *sql.Tx, id string) (out accounts.Account, e error) {
	e = tx.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,created_at FROM owned_authority_accounts WHERE id=$1::uuid`, id).Scan(&out.ID, &out.Name, &out.Nick, &out.Language, &out.Region, &out.CreatedAt)
	if e != nil {
		return
	}
	var p accounts.Profile
	var links, verification []byte
	var updated time.Time
	e = tx.QueryRowContext(ctx, `SELECT id::text,bio,pronouns,location,time_zone,links,first_name,middle_name,last_name,gender,birthday,verification,updated_at FROM owned_accounts_profiles WHERE account_id=$1::uuid`, id).Scan(&p.ID, &p.Bio, &p.Pronouns, &p.Location, &p.TimeZone, &links, &p.FirstName, &p.MiddleName, &p.LastName, &p.Gender, &p.Birthday, &verification, &updated)
	if errors.Is(e, sql.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return
	}
	p.Verification, e = accounts.DecodeVerification(verification)
	if e != nil {
		return out, e
	}
	out.UpdatedAt = &updated
	e = json.Unmarshal(links, &p.Links)
	out.Profile = &p
	return
}
func (r *Repository) accountSummary(ctx context.Context, tx *sql.Tx, id string) (out a.Summary, e error) {
	out.Account, e = adminAccount(ctx, tx, id)
	if e != nil {
		return
	}
	out.Status, e = presencepg.AdministrativeStatusInTransaction(ctx, tx, id, r.now().UTC())
	if errors.Is(e, presence.ErrNotFound) {
		label := "Offline"
		out.Status = presence.Status{ID: id, AccountID: id, Label: &label}
		e = nil
	}
	if e != nil {
		return
	}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_progression_badges WHERE account_id=$1::uuid AND deleted_at IS NULL`, id).Scan(&out.BadgeCount)
	if e != nil {
		return
	}
	acts, e := activitypg.AdministrativeActivitiesInTransaction(ctx, r.db, tx, r.guard, id)
	out.ActiveActivityCount = len(acts)
	return
}
func (r *Repository) ListAccounts(ctx context.Context, actor string, f a.Filter) (out []a.Summary, total int, e error) {
	out = []a.Summary{}
	if e = a.ValidateFilter(f); e != nil {
		return
	}
	tx, _, e := r.beginAccountAdmin(ctx, actor, "", a.ViewPermission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	order := map[string]string{"": "created_at DESC,id", "created_at_desc": "created_at DESC,id", "created_at_asc": "created_at,id", "name_asc": "lower(name),id", "name_desc": "lower(name) DESC,id"}[f.OrderBy]
	rs, e := tx.QueryContext(ctx, `WITH filtered AS MATERIALIZED(SELECT id,name,created_at FROM owned_authority_accounts WHERE position(lower($1::text)in lower(name||' '||nick))>0),page AS(SELECT * FROM filtered ORDER BY `+order+` LIMIT $2::integer OFFSET $3::integer)SELECT totals.n,page.id::text FROM(SELECT count(*)n FROM filtered)totals LEFT JOIN page ON true ORDER BY `+order, f.Query, f.Take, f.Offset)
	if e != nil {
		return
	}
	ids := []string{}
	for rs.Next() {
		var id sql.NullString
		if e = rs.Scan(&total, &id); e != nil {
			break
		}
		if id.Valid {
			ids = append(ids, id.String)
		}
	}
	if e == nil {
		e = rs.Err()
	}
	rs.Close()
	if e != nil {
		return nil, 0, e
	}
	// Lock in canonical order; projection keeps the requested page ordering.
	locked := append([]string{}, ids...)
	sort.Strings(locked)
	for _, id := range locked {
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR SHARE NOWAIT`, id).Scan(&held); e != nil {
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "55P03" {
				e = c.ErrConflict
			}
			return nil, 0, e
		}
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, 0, e
	}
	for _, id := range ids {
		v, x := r.accountSummary(ctx, tx, id)
		if x != nil {
			return nil, 0, x
		}
		out = append(out, v)
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, 0, e
	}
	e = tx.Commit()
	return
}
func (r *Repository) AccountDetail(ctx context.Context, actor, ref string) (out a.Detail, e error) {
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ViewPermission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	out.Summary, e = r.accountSummary(ctx, tx, id)
	if e != nil {
		return
	}
	out.Activities, e = activitypg.AdministrativeActivitiesInTransaction(ctx, r.db, tx, r.guard, id)
	if e != nil {
		return
	}
	out.Contacts, e = signinpg.AdministrativeContactsInTransaction(ctx, tx, id)
	if e != nil {
		return
	}
	out.AuthFactors, e = signinpg.AdministrativeFactorsInTransaction(ctx, tx, id)
	if e != nil {
		return
	}
	out.Badges, e = progressionpg.AdministrativeBadgeRowsInTransaction(ctx, tx, id, r.now().UTC())
	if e != nil {
		return
	}
	out.Board = []map[string]any{}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT to_regclass('account_board_items') IS NOT NULL`).Scan(&exists); e != nil {
		return
	}
	out.BoardConfigured = exists
	if exists {
		rs, x := tx.QueryContext(ctx, `SELECT jsonb_build_object('id',id,'order',"order",'kind',kind,'widget_key',widget_key,'custom_app_id',custom_app_id,'custom_app_widget_key',custom_app_widget_key,'is_enabled',is_enabled,'payload',payload) FROM account_board_items WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY "order",id LIMIT 129`, id)
		if x != nil {
			return out, x
		}
		for rs.Next() {
			var raw []byte
			var v map[string]any
			if e = rs.Scan(&raw); e != nil {
				break
			}
			if e = json.Unmarshal(raw, &v); e != nil {
				break
			}
			out.Board = append(out.Board, v)
		}
		if e == nil {
			e = rs.Err()
		}
		rs.Close()
		if e != nil {
			return
		}
		if len(out.Board) > 128 {
			return out, c.ErrConflict
		}
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (r *Repository) AccountSessions(ctx context.Context, actor, ref string) (out []a.Session, e error) {
	out = []a.Session{}
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ViewPermission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	rs, e := tx.QueryContext(ctx, `SELECT id::text,label,type,COALESCE(parent_id::text,''),client_id,created_at,expires_at,revoked_at FROM owned_authority_sessions WHERE account_id=$1::uuid ORDER BY created_at DESC,id LIMIT 2001`, id)
	if e != nil {
		return
	}
	for rs.Next() {
		var v a.Session
		var created, expires time.Time
		var revoked *time.Time
		if e = rs.Scan(&v.ID, &v.Label, &v.Type, &v.ParentID, &v.ClientID, &created, &expires, &revoked); e != nil {
			break
		}
		v.CreatedAt, v.ExpiresAt, v.RevokedAt = created, expires, revoked
		out = append(out, v)
	}
	if e == nil {
		e = rs.Err()
	}
	rs.Close()
	if e != nil {
		return
	}
	if len(out) > 2000 {
		return nil, c.ErrConflict
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ViewPermission); e != nil {
		return nil, e
	}
	e = tx.Commit()
	return
}
func (r *Repository) MutateAccount(ctx context.Context, actor, ref string, m a.Mutation) (out any, e error) {
	tx, id, e := r.beginAccountAdmin(ctx, actor, ref, a.ManagePermission)
	if e != nil {
		return
	}
	defer tx.Rollback()
	// Administrative credential changes never target the acting proof itself.
	// Self service keeps its stronger recent-authentication requirements.
	if id == actor && (m.Operation == "password.reset" || m.Operation == "factor.delete" || m.Operation == "session.revoke") {
		return nil, c.ErrForbidden
	}
	if m.Operation == "credit.grant" {
		if m.Credit == nil {
			return nil, c.ErrInvalid
		}
		if e = r.requireAccountAdmin(ctx, tx, actor, "credits.records.manage"); e != nil {
			return nil, e
		}
		v := m.Credit
		out, e = accounthistorypg.GrantCreditInTransaction(ctx, tx, id, "admin:"+actor, accounthistorypg.Credit{Record: v.Record, ReasonType: v.ReasonType, Reason: v.Reason, Delta: v.Delta, ExpiresAt: v.ExpiresAt}, r.now().UTC())
	} else {
		out, e = signinpg.AdministrativeMutationInTransaction(ctx, tx, id, m, r.now().UTC())
	}
	if e != nil {
		if errors.Is(e, history.ErrInvalid) {
			e = c.ErrInvalid
		} else {
			e = mapped(e)
		}
		return
	}
	if e = r.audit(ctx, tx, actor, "account", id, m.Operation, map[string]any{"resource_id": m.ResourceID}); e != nil {
		return nil, e
	}
	if m.Operation == "credit.grant" {
		if e = r.requireAccountAdmin(ctx, tx, actor, "credits.records.manage"); e != nil {
			return nil, e
		}
	}
	if e = r.requireAccountAdmin(ctx, tx, actor, a.ManagePermission); e != nil {
		return nil, e
	}
	e = tx.Commit()
	return
}

var _ a.ExtendedRepository = (*Repository)(nil)
