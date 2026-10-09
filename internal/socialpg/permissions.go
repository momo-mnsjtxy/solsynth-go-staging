// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

var _ social.PermissionRepository = (*Repository)(nil)
var _ social.PermissionProvider = (*Repository)(nil)
var permissionExpression = regexp.MustCompile(`^(\$[1-9][0-9]*|[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?)$`)

func (r *Repository) RealmPermissionSQL(realmID, actor string, key social.RealmPermission) string {
	if !permissionExpression.MatchString(realmID) || !permissionExpression.MatchString(actor) || key.Bit() == 0 {
		return "FALSE"
	}
	return fmt.Sprintf(`(owned_realm_permission_mask((%s)::uuid,NULLIF((%s)::text,'')::uuid) & %d)<>0`, realmID, actor, key.Bit())
}
func (r *Repository) RealmPermissionsInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (social.RealmPermissions, error) {
	if tx == nil {
		return social.RealmPermissions{}, social.ErrUnavailable
	}
	if !authority.ValidID(actor) || !authority.ValidID(id) {
		return social.RealmPermissions{}, social.ErrInvalid
	}
	if _, e := requireRole(ctx, tx, id, actor, social.Normal); e != nil {
		return social.RealmPermissions{}, e
	}
	var mask int
	e := tx.QueryRowContext(ctx, `SELECT owned_realm_permission_mask($1::uuid,$2::uuid)`, id, actor).Scan(&mask)
	return social.PermissionsFromMask(mask), e
}
func (r *Repository) requirePermission(ctx context.Context, tx *sql.Tx, actor, id string, key social.RealmPermission) (social.RealmPermissions, error) {
	if key.Bit() == 0 {
		return social.RealmPermissions{}, social.ErrForbidden
	}
	p, e := r.RealmPermissionsInTransaction(ctx, tx, actor, id)
	if e == nil && !p.Allows(key) {
		e = social.ErrForbidden
	}
	return p, e
}
func (r *Repository) RealmPermissionForWriteInTransaction(ctx context.Context, tx *sql.Tx, actor, id string, key social.RealmPermission) error {
	if key.Bit() == 0 {
		return social.ErrForbidden
	}
	if _, e := r.RealmRoleForWriteInTransaction(ctx, tx, actor, id); e != nil {
		return e
	}
	_, e := r.requirePermission(ctx, tx, actor, id, key)
	return e
}
func (r *Repository) PermissionsReady(ctx context.Context) error {
	if r == nil || r.db == nil {
		return social.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT role_level,generation,permission_mask,deleted_at FROM owned_realm_role_permissions LIMIT 0;SELECT member_id,membership_since,allow_mask,deny_mask FROM owned_realm_user_permissions LIMIT 0;SELECT permission_generation FROM owned_realm_members LIMIT 0;SELECT owned_realm_permission_mask(NULL::uuid,NULL::uuid)`)
	return e
}
func (r *Repository) EffectivePermissions(ctx context.Context, actor, ref string) (out social.RealmPermissions, err error) {
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		v, e := r.realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		out, e = r.RealmPermissionsInTransaction(ctx, tx, actor, v.ID)
		return e
	})
	return
}

const rolePermissionColumns = `id::text,realm_id::text,role_level,generation,permission_mask,created_at,updated_at`

func scanRolePermission(s scanner) (v social.RolePermission, e error) {
	var mask int
	e = s.Scan(&v.ID, &v.RealmID, &v.RoleLevel, &v.Generation, &mask, &v.CreatedAt, &v.UpdatedAt)
	v.RealmPermissions = social.PermissionsFromMask(mask)
	return v, mapped(e)
}
func rolePermission(ctx context.Context, tx *sql.Tx, rid string, level social.Role) (social.RolePermission, error) {
	return scanRolePermission(tx.QueryRowContext(ctx, `SELECT `+rolePermissionColumns+` FROM owned_realm_role_permissions WHERE realm_id=$1 AND role_level=$2 AND deleted_at IS NULL`, rid, level))
}
func (r *Repository) RolePermissions(ctx context.Context, actor, ref string) (out []social.RolePermission, err error) {
	out = []social.RolePermission{}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		v, e := r.realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		if _, e = r.requirePermission(ctx, tx, actor, v.ID, social.CanManageRealm); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+rolePermissionColumns+` FROM owned_realm_role_permissions WHERE realm_id=$1 AND deleted_at IS NULL ORDER BY role_level DESC`, v.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanRolePermission(rows)
			if e != nil {
				return e
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return
}
func (r *Repository) permissionManager(ctx context.Context, tx *sql.Tx, actor string, v social.Realm, target social.Role) (social.RealmPermissions, error) {
	m, e := requireRole(ctx, tx, v.ID, actor, social.Normal)
	if e != nil {
		return social.RealmPermissions{}, e
	}
	p, e := r.requirePermission(ctx, tx, actor, v.ID, social.CanManageRealm)
	if e != nil {
		return p, e
	}
	if target >= social.Owner || v.AccountID != actor && m.Role <= target {
		return p, social.ErrForbidden
	}
	return p, nil
}
func (r *Repository) SetRolePermission(ctx context.Context, actor, ref string, p social.RolePermissionRequest) (out social.RolePermission, err error) {
	allow, deny := p.Masks()
	if p.RoleLevel == nil || !p.RoleLevel.Valid() || *p.RoleLevel >= social.Owner || allow|deny != 255 {
		return out, social.ErrInvalid
	}
	err = r.write(ctx, actor, nil, []string{"realms.permissions.manage"}, func(tx *sql.Tx, now time.Time) error {
		v, e := r.realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		grants, e := r.permissionManager(ctx, tx, actor, v, *p.RoleLevel)
		if e != nil {
			return e
		}
		if allow & ^grants.Mask() != 0 {
			return social.ErrForbidden
		}
		out, e = scanRolePermission(tx.QueryRowContext(ctx, `INSERT INTO owned_realm_role_permissions(realm_id,role_level,permission_mask,created_at,updated_at)VALUES($1,$2,$3,$4,$4)ON CONFLICT(realm_id,role_level)DO UPDATE SET permission_mask=EXCLUDED.permission_mask,generation=CASE WHEN owned_realm_role_permissions.deleted_at IS NOT NULL THEN owned_realm_role_permissions.generation+1 ELSE owned_realm_role_permissions.generation END,deleted_at=NULL,updated_at=EXCLUDED.updated_at RETURNING `+rolePermissionColumns, v.ID, *p.RoleLevel, allow, now))
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.permissions.role.update", out.ID, now)
	})
	return
}
func (r *Repository) DeleteRolePermission(ctx context.Context, actor, ref string, level social.Role) error {
	if !level.Valid() {
		return social.ErrInvalid
	}
	if level == social.Normal || level == social.Moderator || level == social.Owner {
		return social.ErrForbidden
	}
	return r.write(ctx, actor, nil, []string{"realms.permissions.manage"}, func(tx *sql.Tx, now time.Time) error {
		v, e := r.realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if _, e = r.permissionManager(ctx, tx, actor, v, level); e != nil {
			return e
		}
		p, e := rolePermission(ctx, tx, v.ID, level)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_role_permissions SET deleted_at=$3,updated_at=$3,permission_mask=0 WHERE realm_id=$1 AND role_level=$2`, v.ID, level, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_realm_user_permissions WHERE realm_id=$1 AND account_id IN(SELECT account_id FROM owned_realm_members WHERE realm_id=$1 AND role=$2)`, v.ID, level); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.permissions.role.delete", p.ID, now)
	})
}

const userPermissionColumns = `u.id::text,u.realm_id::text,u.account_id::text,u.allow_mask,u.deny_mask,u.created_at,u.updated_at`

func scanUserPermission(s scanner) (v social.UserPermission, e error) {
	var a, d int
	e = s.Scan(&v.ID, &v.RealmID, &v.AccountID, &a, &d, &v.CreatedAt, &v.UpdatedAt)
	v.PermissionOverrides = social.OverridesFromMasks(a, d)
	return v, mapped(e)
}
func (r *Repository) UserPermissions(ctx context.Context, actor, ref, target string) (out *social.UserPermission, err error) {
	if !authority.ValidID(target) {
		return nil, social.ErrInvalid
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		v, e := r.realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		if _, e = r.requirePermission(ctx, tx, actor, v.ID, social.CanManageRealm); e != nil {
			return e
		}
		if _, e = requireRole(ctx, tx, v.ID, target, social.Normal); e != nil {
			return e
		}
		p, e := scanUserPermission(tx.QueryRowContext(ctx, `SELECT `+userPermissionColumns+` FROM owned_realm_user_permissions u JOIN owned_realm_members m ON m.id=u.member_id AND m.joined_at=u.membership_since WHERE u.realm_id=$1 AND u.account_id=$2 AND m.leave_at IS NULL`, v.ID, target))
		if errors.Is(e, social.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		out = &p
		return nil
	})
	return
}
func (r *Repository) SetUserPermission(ctx context.Context, actor, ref string, p social.UserPermissionRequest) (out *social.UserPermission, err error) {
	if !authority.ValidID(p.AccountID) {
		return nil, social.ErrInvalid
	}
	if p.AccountID == actor {
		return nil, social.ErrForbidden
	}
	err = r.write(ctx, actor, []string{p.AccountID}, []string{"realms.permissions.manage"}, func(tx *sql.Tx, now time.Time) error {
		v, e := r.realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		target, e := requireRole(ctx, tx, v.ID, p.AccountID, social.Normal)
		if e != nil {
			return e
		}
		if p.AccountID == v.AccountID {
			return social.ErrForbidden
		}
		grants, e := r.permissionManager(ctx, tx, actor, v, target.Role)
		if e != nil {
			return e
		}
		base, e := rolePermission(ctx, tx, v.ID, target.Role)
		if e != nil {
			return e
		}
		var generation int64
		if e = tx.QueryRowContext(ctx, `SELECT permission_generation FROM owned_realm_members WHERE id=$1`, target.ID).Scan(&generation); e != nil {
			return e
		}
		if generation != base.Generation {
			return social.ErrConflict
		}
		allow, deny := p.Masks()
		if ((base.Mask()|allow) & ^deny) & ^grants.Mask() != 0 {
			return social.ErrForbidden
		}
		if allow|deny == 0 {
			if _, e = tx.ExecContext(ctx, `DELETE FROM owned_realm_user_permissions WHERE realm_id=$1 AND account_id=$2`, v.ID, p.AccountID); e != nil {
				return e
			}
			return audit(ctx, tx, actor, "realms.permissions.user.clear", target.ID, now)
		}
		row, e := scanUserPermission(tx.QueryRowContext(ctx, `INSERT INTO owned_realm_user_permissions(realm_id,account_id,member_id,membership_since,allow_mask,deny_mask,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7)ON CONFLICT(realm_id,account_id)DO UPDATE SET member_id=EXCLUDED.member_id,membership_since=EXCLUDED.membership_since,allow_mask=EXCLUDED.allow_mask,deny_mask=EXCLUDED.deny_mask,updated_at=EXCLUDED.updated_at RETURNING id::text,realm_id::text,account_id::text,allow_mask,deny_mask,created_at,updated_at`, v.ID, p.AccountID, target.ID, target.JoinedAt, allow, deny, now))
		if e != nil {
			return e
		}
		out = &row
		return audit(ctx, tx, actor, "realms.permissions.user.update", target.ID, now)
	})
	return
}

func (r *Repository) memberManager(ctx context.Context, tx *sql.Tx, actor, rid string) (social.Member, error) {
	m, e := requireRole(ctx, tx, rid, actor, social.Normal)
	if e != nil {
		return m, e
	}
	_, e = r.requirePermission(ctx, tx, actor, rid, social.CanManageMembers)
	if e == nil {
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1`, rid).Scan(&owner); e != nil {
			return m, e
		}
		if owner == actor {
			m.Role = social.Owner
		} else if m.Role == social.Owner {
			return m, social.ErrForbidden
		}
	}
	return m, e
}
func (r *Repository) canGrantRole(ctx context.Context, tx *sql.Tx, actor string, v social.Realm, target social.RolePermission) error {
	grants, e := r.RealmPermissionsInTransaction(ctx, tx, actor, v.ID)
	if e != nil {
		return e
	}
	if target.Mask() & ^grants.Mask() != 0 {
		return social.ErrForbidden
	}
	return nil
}

// The caller retains the already ordered realm lock through its transaction.
func (r *Repository) RequireRealmPermissionInTransaction(ctx context.Context, tx *sql.Tx, actor, id string, key social.RealmPermission) error {
	_, e := r.requirePermission(ctx, tx, actor, id, key)
	return e
}
