// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

const realmColumns = `r.id::text,r.slug,r.name,r.description,r.account_id::text,r.is_public,r.is_community,r.boost_level,r.created_at,r.updated_at`
const memberColumns = `m.id::text,m.realm_id::text,m.account_id::text,m.role,m.joined_at,m.leave_at,m.created_at,m.updated_at`

func scanRealm(s scanner) (v social.Realm, e error) {
	e = s.Scan(&v.ID, &v.Slug, &v.Name, &v.Description, &v.AccountID, &v.IsPublic, &v.IsCommunity, &v.BoostLevel, &v.CreatedAt, &v.UpdatedAt)
	return v, mapped(e)
}
func realm(ctx context.Context, q querier, ref string, lock bool) (social.Realm, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF r"
	}
	return scanRealm(q.QueryRowContext(ctx, `SELECT `+realmColumns+` FROM owned_realms r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.deleted_at IS NULL AND NOT a.disabled AND (r.id::text=$1 OR lower(r.slug)=lower($1))`+suffix, ref))
}
func scanMember(s scanner) (v social.Member, e error) {
	e = s.Scan(&v.ID, &v.RealmID, &v.AccountID, &v.Role, &v.JoinedAt, &v.LeaveAt, &v.CreatedAt, &v.UpdatedAt)
	return v, mapped(e)
}
func membership(ctx context.Context, q querier, realmID, actor string) (social.Member, error) {
	return scanMember(q.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1 AND m.account_id=$2 AND m.leave_at IS NULL AND NOT a.disabled`, realmID, actor))
}
func requireRole(ctx context.Context, q querier, realmID, actor string, minimum social.Role) (social.Member, error) {
	m, e := membership(ctx, q, realmID, actor)
	if errors.Is(e, social.ErrNotFound) {
		return m, social.ErrForbidden
	}
	if e != nil {
		return m, e
	}
	if m.JoinedAt == nil || m.Role < minimum {
		return m, social.ErrForbidden
	}
	return m, nil
}
func canViewRealm(ctx context.Context, q querier, v social.Realm, actor string) error {
	if v.IsPublic {
		return nil
	}
	if actor == "" {
		return social.ErrNotFound
	}
	m, e := membership(ctx, q, v.ID, actor)
	if e != nil {
		if errors.Is(e, social.ErrNotFound) {
			return social.ErrNotFound
		}
		return e
	}
	if m.JoinedAt == nil {
		return social.ErrNotFound
	}
	return nil
}
func (r *Repository) CreateRealm(ctx context.Context, actor string, p social.RealmPatch) (out social.Realm, err error) {
	if e := social.ValidateRealm(p, true); e != nil {
		return out, e
	}
	err = r.write(ctx, actor, nil, []string{"realms.create"}, func(tx *sql.Tx, now time.Time) error {
		var quota, used int64
		e := tx.QueryRowContext(ctx, `SELECT total FROM owned_realm_quota WHERE account_id=$1 AND starts_at<=statement_timestamp() AND(expires_at IS NULL OR expires_at>statement_timestamp())`, actor).Scan(&quota)
		if errors.Is(e, sql.ErrNoRows) {
			return social.ErrQuota
		}
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_realms WHERE account_id=$1 AND deleted_at IS NULL`, actor).Scan(&used); e != nil {
			return e
		}
		if used >= quota {
			return social.ErrQuota
		}
		id, e := newID()
		if e != nil {
			return e
		}
		mid, e := newID()
		if e != nil {
			return e
		}
		out = social.Realm{ID: id, AccountID: actor, Slug: *p.Slug, Name: *p.Name, CreatedAt: now, UpdatedAt: now}
		if p.Description != nil {
			out.Description = *p.Description
		}
		if p.IsPublic != nil {
			out.IsPublic = *p.IsPublic
		}
		if p.IsCommunity != nil {
			out.IsCommunity = *p.IsCommunity
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_realms(id,slug,name,description,account_id,is_public,is_community,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, id, out.Slug, out.Name, out.Description, actor, out.IsPublic, out.IsCommunity, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,100,$4,$4,$4)`, mid, id, actor, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.create", id, now)
	})
	return
}
func (r *Repository) GetRealm(ctx context.Context, actor, ref string) (out social.Realm, err error) {
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		var e error
		out, e = realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		return canViewRealm(ctx, tx, out, actor)
	})
	return
}
func (r *Repository) ListRealms(ctx context.Context, actor string, offset, take int) (out []social.Realm, err error) {
	out = []social.Realm{}
	if actor == "" {
		return out, social.ErrForbidden
	}
	if e := social.ValidatePage(offset, take); e != nil {
		return out, e
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+realmColumns+` FROM owned_realms r JOIN owned_realm_members m ON m.realm_id=r.id JOIN owned_authority_accounts a ON a.id=r.account_id WHERE m.account_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL AND NOT a.disabled ORDER BY r.created_at,r.id OFFSET $2 LIMIT $3`, actor, offset, take)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanRealm(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
func (r *Repository) UpdateRealm(ctx context.Context, actor, ref string, p social.RealmPatch) (out social.Realm, err error) {
	if e := social.ValidateRealm(p, false); e != nil {
		return out, e
	}
	err = r.write(ctx, actor, nil, []string{"realms.update"}, func(tx *sql.Tx, now time.Time) error {
		var e error
		out, e = realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		if _, e = requireRole(ctx, tx, out.ID, actor, social.Moderator); e != nil {
			return e
		}
		if p.Slug != nil {
			out.Slug = *p.Slug
		}
		if p.Name != nil {
			out.Name = *p.Name
		}
		if p.Description != nil {
			out.Description = *p.Description
		}
		if p.IsPublic != nil {
			out.IsPublic = *p.IsPublic
		}
		if p.IsCommunity != nil {
			out.IsCommunity = *p.IsCommunity
		}
		out.UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realms SET slug=$2,name=$3,description=$4,is_public=$5,is_community=$6,updated_at=$7 WHERE id=$1`, out.ID, out.Slug, out.Name, out.Description, out.IsPublic, out.IsCommunity, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.update", out.ID, now)
	})
	return
}
func (r *Repository) DeleteRealm(ctx context.Context, actor, ref string) error {
	return r.write(ctx, actor, nil, []string{"realms.delete"}, func(tx *sql.Tx, now time.Time) error {
		v, e := realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		if _, e = requireRole(ctx, tx, v.ID, actor, social.Owner); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realms SET deleted_at=$2,updated_at=$2 WHERE id=$1`, v.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$2,updated_at=$2 WHERE realm_id=$1 AND leave_at IS NULL`, v.ID, now); e != nil {
			return e
		}
		// Canonical organization publishers cannot outlive their owning realm.
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET deleted_at=$2,updated_at=$2 WHERE realm_id=$1 AND deleted_at IS NULL`, v.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$2,updated_at=$2 WHERE publisher_id IN(SELECT id FROM owned_publishers WHERE realm_id=$1) AND revoked_at IS NULL`, v.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_publishing_settings WHERE default_publisher_id IN(SELECT id FROM owned_publishers WHERE realm_id=$1)`, v.ID); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.delete", v.ID, now)
	})
}
func (r *Repository) Members(ctx context.Context, actor, ref string, offset, take int) (out []social.Member, total int64, err error) {
	out = []social.Member{}
	if e := social.ValidatePage(offset, take); e != nil {
		return out, 0, e
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		v, e := realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		if e = canViewRealm(ctx, tx, v, actor); e != nil {
			return e
		}
		from := ` FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1 AND m.leave_at IS NULL AND m.joined_at IS NOT NULL AND NOT a.disabled`
		if e = tx.QueryRowContext(ctx, `SELECT count(*)`+from, v.ID).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+from+` ORDER BY m.role DESC,m.created_at,m.id OFFSET $2 LIMIT $3`, v.ID, offset, take)
		if e != nil {
			return e
		}
		for rows.Next() {
			m, e := scanMember(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out = append(out, m)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for i := range out {
			out[i].Account, e = account(ctx, tx, out[i].AccountID)
			if e != nil {
				return e
			}
		}
		return nil
	})
	return
}
func (r *Repository) Me(ctx context.Context, actor, ref string) (out social.Member, err error) {
	if actor == "" {
		return out, social.ErrForbidden
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		v, e := realm(ctx, tx, ref, false)
		if e != nil {
			return e
		}
		out, e = requireRole(ctx, tx, v.ID, actor, social.Normal)
		if e != nil {
			return e
		}
		out.Realm = &v
		out.Account, e = account(ctx, tx, actor)
		return e
	})
	return
}
func (r *Repository) Invites(ctx context.Context, actor string, offset, take int) (out []social.Member, err error) {
	out = []social.Member{}
	if actor == "" {
		return out, social.ErrForbidden
	}
	if e := social.ValidatePage(offset, take); e != nil {
		return out, e
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+` FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id JOIN owned_authority_accounts a ON a.id=r.account_id WHERE m.account_id=$1 AND m.joined_at IS NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL AND NOT a.disabled ORDER BY m.created_at,m.id OFFSET $2 LIMIT $3`, actor, offset, take)
		if e != nil {
			return e
		}
		for rows.Next() {
			m, e := scanMember(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out = append(out, m)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for i := range out {
			v, e := realm(ctx, tx, out[i].RealmID, false)
			if e != nil {
				return e
			}
			out[i].Realm = &v
			out[i].Account, e = account(ctx, tx, actor)
			if e != nil {
				return e
			}
		}
		return nil
	})
	return
}
func (r *Repository) ChangeMember(ctx context.Context, actor, ref string, c social.MemberChange) (out social.Member, err error) {
	if c.Action == social.Accept || c.Action == social.Decline || c.Action == social.Join || c.Action == social.Leave {
		c.AccountID = actor
	}
	if !authority.ValidID(c.AccountID) {
		return out, social.ErrInvalid
	}
	if (c.Action == social.Invite || c.Action == social.ChangeRole) && (!c.Role.Valid() || c.Role == social.Owner) {
		return out, social.ErrInvalid
	}
	permission := "realms.members.manage"
	if c.Action == social.Invite || c.Action == social.Accept || c.Action == social.Decline {
		permission = "realms.invites.manage"
	}
	err = r.write(ctx, actor, []string{c.AccountID}, []string{permission}, func(tx *sql.Tx, now time.Time) error {
		v, e := realm(ctx, tx, ref, true)
		if e != nil {
			return e
		}
		m, e := membership(ctx, tx, v.ID, c.AccountID)
		found := e == nil
		if e != nil && !errors.Is(e, social.ErrNotFound) {
			return e
		}
		switch c.Action {
		case social.Invite:
			am, e := requireRole(ctx, tx, v.ID, actor, social.Moderator)
			if e != nil {
				return e
			}
			if actor == c.AccountID || am.Role <= c.Role {
				return social.ErrForbidden
			}
			if c.Role > social.Normal && v.BoostLevel < 2 {
				return social.ErrForbidden
			}
			if found {
				return social.ErrConflict
			}
			if yes, e := blocked(ctx, tx, actor, c.AccountID); e != nil {
				return e
			} else if yes {
				return social.ErrForbidden
			}
			id, e := newID()
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,invited_by,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$6) ON CONFLICT(realm_id,account_id) DO UPDATE SET role=EXCLUDED.role,invited_by=EXCLUDED.invited_by,joined_at=NULL,leave_at=NULL,updated_at=EXCLUDED.updated_at WHERE owned_realm_members.leave_at IS NOT NULL`, id, v.ID, c.AccountID, c.Role, actor, now)
			if e != nil {
				return e
			}
		case social.Accept:
			if !found || m.JoinedAt != nil {
				return social.ErrNotFound
			}
			var inviter string
			if e = tx.QueryRowContext(ctx, `SELECT invited_by::text FROM owned_realm_members WHERE id=$1`, m.ID).Scan(&inviter); e != nil {
				return e
			}
			am, e := requireRole(ctx, tx, v.ID, inviter, social.Moderator)
			if e != nil {
				return e
			}
			if am.Role <= m.Role {
				return social.ErrForbidden
			}
			if yes, e := blocked(ctx, tx, actor, inviter); e != nil {
				return e
			} else if yes {
				return social.ErrForbidden
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET joined_at=$2,updated_at=$2 WHERE id=$1`, m.ID, now); e != nil {
				return e
			}
		case social.Decline:
			if !found || m.JoinedAt != nil {
				return social.ErrNotFound
			}
			m.LeaveAt = &now
			if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$2,updated_at=$2 WHERE id=$1`, m.ID, now); e != nil {
				return e
			}
		case social.Join:
			if !v.IsCommunity {
				return social.ErrForbidden
			}
			if found {
				return social.ErrConflict
			}
			if yes, e := blocked(ctx, tx, actor, v.AccountID); e != nil {
				return e
			} else if yes {
				return social.ErrForbidden
			}
			id, e := newID()
			if e != nil {
				return e
			}
			// Rejoining cannot revive a previous moderator role or a pending invitation.
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,0,$4,$4,$4) ON CONFLICT(realm_id,account_id) DO UPDATE SET role=0,invited_by=NULL,joined_at=EXCLUDED.joined_at,leave_at=NULL,updated_at=EXCLUDED.updated_at WHERE owned_realm_members.leave_at IS NOT NULL`, id, v.ID, actor, now); e != nil {
				return e
			}
		case social.Leave, social.Remove:
			if !found || m.JoinedAt == nil {
				return social.ErrNotFound
			}
			if m.Role == social.Owner {
				return social.ErrForbidden
			}
			if c.Action == social.Remove {
				am, e := requireRole(ctx, tx, v.ID, actor, social.Moderator)
				if e != nil {
					return e
				}
				if actor == c.AccountID || am.Role <= m.Role {
					return social.ErrForbidden
				}
			}
			m.LeaveAt = &now
			if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$2,updated_at=$2 WHERE id=$1`, m.ID, now); e != nil {
				return e
			}
		case social.ChangeRole:
			if !found || m.JoinedAt == nil {
				return social.ErrNotFound
			}
			am, e := requireRole(ctx, tx, v.ID, actor, social.Moderator)
			if e != nil {
				return e
			}
			if actor == c.AccountID || am.Role <= m.Role || am.Role <= c.Role {
				return social.ErrForbidden
			}
			if c.Role > social.Normal && v.BoostLevel < 2 {
				return social.ErrForbidden
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET role=$2,updated_at=$3 WHERE id=$1`, m.ID, c.Role, now); e != nil {
				return e
			}
		default:
			return social.ErrInvalid
		}
		if m.LeaveAt != nil {
			out = m
			out.UpdatedAt = now
		} else {
			out, e = membership(ctx, tx, v.ID, c.AccountID)
			if e != nil {
				return e
			}
		}
		out.Realm = &v
		out.Account, e = account(ctx, tx, out.AccountID)
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "realms.members."+string(c.Action), out.ID, now)
	})
	return
}
