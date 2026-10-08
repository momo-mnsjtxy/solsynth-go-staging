// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
)

func (r *Repository) mutateRealm(ctx context.Context, actor, ref string, cmd c.Command) (any, error) {
	switch cmd.Action {
	case "update":
		if cmd.Realm == nil {
			return nil, c.ErrInvalid
		}
		p := cmd.Realm
		if e := social.ValidateRealm(social.RealmPatch{Slug: p.Slug, Name: p.Name, Description: p.Description, IsPublic: p.IsPublic, IsCommunity: p.IsCommunity}, false); e != nil {
			return nil, c.ErrInvalid
		}
		if p.AccountID != nil && !authority.ValidID(*p.AccountID) {
			return nil, c.ErrInvalid
		}
	case "role":
		if !authority.ValidID(cmd.AccountID) || cmd.Role == nil || !social.Role(*cmd.Role).Valid() {
			return nil, c.ErrInvalid
		}
	case "remove_member":
		if !authority.ValidID(cmd.AccountID) {
			return nil, c.ErrInvalid
		}
	case "delete":
	default:
		return nil, c.ErrInvalid
	}
	var observed social.Realm
	permission := c.RealmKind.Permission(cmd.Action)
	tx, e := r.begin(ctx, actor, permission, true, func(tx *sql.Tx) ([]string, error) {
		var e error
		observed, e = r.realm(ctx, tx, ref)
		ids := []string{observed.AccountID, cmd.AccountID}
		if cmd.Realm != nil {
			ids = append(ids, ptr(cmd.Realm.AccountID))
		}
		return ids, e
	})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = lockIDs(ctx, tx, "owned_realms", []string{observed.ID}); e != nil {
		return nil, e
	}
	v, e := r.realm(ctx, tx, ref)
	if e != nil {
		return nil, e
	}
	if v.ID != observed.ID || v.AccountID != observed.AccountID {
		return nil, c.ErrConflict
	}
	// Realm ownership does not grant publisher membership or editing. Every owner
	// transition keeps one joined owner and demotes the former owner to Normal.
	now := r.config.Now().UTC()
	before := map[string]any{"account_id": v.AccountID, "is_public": v.IsPublic, "slug": v.Slug}
	target := ""
	if cmd.Realm != nil && cmd.Realm.AccountID != nil {
		target = *cmd.Realm.AccountID
	}
	if cmd.Action == "role" && *cmd.Role == 100 {
		target = cmd.AccountID
	}
	if target != "" && target != v.AccountID {
		if e = activeTarget(ctx, tx, target); e != nil {
			return nil, e
		}
		var joined bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2 AND leave_at IS NULL AND joined_at IS NOT NULL)`, v.ID, target).Scan(&joined); e != nil {
			return nil, e
		}
		if !joined {
			return nil, c.ErrConflict
		}
	}
	if cmd.Action == "delete" {
		rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_publishers WHERE realm_id=$1 ORDER BY id FOR UPDATE`, v.ID)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	if e = r.authorize(ctx, tx, actor, permission, true); e != nil {
		return nil, e
	}
	if target != "" && target != v.AccountID {
		res, e := tx.ExecContext(ctx, `UPDATE owned_realm_members SET role=0,permission_generation=(SELECT generation FROM owned_realm_role_permissions WHERE realm_id=$1 AND role_level=0),updated_at=$3 WHERE realm_id=$1 AND account_id=$2 AND role=100 AND leave_at IS NULL AND joined_at IS NOT NULL`, v.ID, v.AccountID, now)
		if e != nil {
			return nil, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, c.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET role=100,permission_generation=(SELECT generation FROM owned_realm_role_permissions WHERE realm_id=$1 AND role_level=100),updated_at=$3 WHERE realm_id=$1 AND account_id=$2 AND leave_at IS NULL AND joined_at IS NOT NULL`, v.ID, target, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_realm_user_permissions WHERE realm_id=$1 AND account_id IN($2::uuid,$3::uuid)`, v.ID, v.AccountID, target); e != nil {
			return nil, e
		}
		v.AccountID = target
	}
	var deleted any
	switch cmd.Action {
	case "update":
		p := cmd.Realm
		if p.Slug != nil {
			v.Slug = *p.Slug
		}
		if p.Name != nil {
			v.Name = *p.Name
		}
		if p.Description != nil {
			v.Description = *p.Description
		}
		if p.IsPublic != nil {
			v.IsPublic = *p.IsPublic
		}
		if p.IsCommunity != nil {
			v.IsCommunity = *p.IsCommunity
		}
	case "role", "remove_member":
		var role int
		var joined sql.NullTime
		if e = tx.QueryRowContext(ctx, `SELECT role,joined_at FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2 AND leave_at IS NULL`, v.ID, cmd.AccountID).Scan(&role, &joined); e != nil {
			return nil, mapped(e)
		}
		if cmd.AccountID == v.AccountID && (cmd.Action == "remove_member" || *cmd.Role != 100) {
			return nil, c.ErrConflict
		}
		if cmd.Action == "remove_member" {
			_, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$3,updated_at=$3 WHERE realm_id=$1 AND account_id=$2`, v.ID, cmd.AccountID, now)
		} else {
			if !joined.Valid {
				return nil, c.ErrConflict
			}
			if e = activeTarget(ctx, tx, cmd.AccountID); e != nil {
				return nil, e
			}
			if *cmd.Role == 100 && cmd.AccountID != v.AccountID {
				return nil, c.ErrConflict
			}
			if *cmd.Role > 0 && *cmd.Role < 100 && v.BoostLevel < 2 {
				return nil, c.ErrConflict
			}
			var generation int64
			if e = tx.QueryRowContext(ctx, `SELECT generation FROM owned_realm_role_permissions WHERE realm_id=$1 AND role_level=$2 AND deleted_at IS NULL`, v.ID, *cmd.Role).Scan(&generation); e != nil {
				return nil, mapped(e)
			}
			_, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET role=$3,updated_at=$4,permission_generation=$5 WHERE realm_id=$1 AND account_id=$2`, v.ID, cmd.AccountID, *cmd.Role, now, generation)
		}
		if e != nil {
			return nil, e
		}
	case "delete":
		deleted = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$2,updated_at=$2 WHERE realm_id=$1 AND leave_at IS NULL`, v.ID, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET deleted_at=$2,updated_at=$2 WHERE realm_id=$1 AND deleted_at IS NULL`, v.ID, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$2,updated_at=$2 WHERE publisher_id IN(SELECT id FROM owned_publishers WHERE realm_id=$1)AND revoked_at IS NULL`, v.ID, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_publishing_settings WHERE default_publisher_id IN(SELECT id FROM owned_publishers WHERE realm_id=$1)`, v.ID); e != nil {
			return nil, e
		}
	}
	v.UpdatedAt = now
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realms SET slug=$2,name=$3,description=$4,is_public=$5,is_community=$6,account_id=$7,updated_at=$8,deleted_at=$9 WHERE id=$1`, v.ID, v.Slug, v.Name, v.Description, v.IsPublic, v.IsCommunity, v.AccountID, now, deleted); e != nil {
		return nil, mapped(e)
	}
	if e = r.audit(ctx, tx, actor, c.RealmKind, v.ID, cmd.Action, map[string]any{"before": before, "account_id": v.AccountID, "is_public": v.IsPublic, "slug": v.Slug, "member_account_id": cmd.AccountID, "role": cmd.Role}); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_social_audit(actor_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4)`, actor, "admin.realms."+cmd.Action, v.ID, now); e != nil {
		return nil, e
	}
	var out any = v
	if cmd.Action == "role" {
		out, e = r.member(ctx, tx, actor, v.ID, cmd.AccountID)
		if e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
