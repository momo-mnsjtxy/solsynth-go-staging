// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

// profileAuthority deliberately has no realm moderator/management gate. The
// authored self-profile operation needs the global scope and current membership.
func (r *Repository) profileAuthority(ctx context.Context, tx *sql.Tx, actor string, v scope, perk bool) error {
	if _, e := r.guard.Authorize(ctx, r.db, tx, actor, "realms.members.manage"); e != nil {
		return e
	}
	if e := currentAccount(ctx, tx, v.Owner); e != nil {
		return e
	}
	if e := activeIdentity(ctx, tx, actor, v); e != nil {
		return e
	}
	if perk {
		p, ok := r.boosts.(interface {
			ProfilePerkInTransaction(context.Context, *sql.Tx, string, time.Time) (int, error)
		})
		if !ok {
			return m.ErrUnavailable
		}
		level, e := p.ProfilePerkInTransaction(ctx, tx, actor, r.cfg.Now().UTC())
		if e != nil {
			return e
		}
		if level < 2 {
			return m.ErrForbidden
		}
	}
	return nil
}
func activeIdentity(ctx context.Context, tx *sql.Tx, actor string, v scope) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id LEFT JOIN owned_realm_role_permissions p ON p.realm_id=m.realm_id AND p.role_level=m.role WHERE m.realm_id=$1::uuid AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL AND((r.account_id=m.account_id AND m.role=100)OR(r.account_id<>m.account_id AND m.role>=0 AND m.role<100 AND p.deleted_at IS NULL AND p.generation=m.permission_generation)))`, v.ID, actor).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return m.ErrNotFound
	}
	return nil
}
func (r *Repository) UpdateProfile(ctx context.Context, actor, ref string, q m.ProfileRequest) (social.Member, error) {
	if e := m.ValidateProfile(q); e != nil {
		return social.Member{}, e
	}
	return r.changeProfile(ctx, actor, ref, q, false)
}
func (r *Repository) DeleteProfile(ctx context.Context, actor, ref string) (social.Member, error) {
	return r.changeProfile(ctx, actor, ref, m.ProfileRequest{}, true)
}
func (r *Repository) changeProfile(ctx context.Context, actor, ref string, q m.ProfileRequest, clear bool) (out social.Member, e error) {
	defer func() { e = mapped(e) }()
	if !authority.ValidID(actor) {
		return out, authority.ErrInvalid
	}
	// The shared realm lock serializes membership, roles, visibility and ownership
	// against canonical realm writers. No upgrade from SHARE to UPDATE is needed.
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = r.profileAuthority(ctx, tx, actor, v, !clear); e != nil {
		return
	}
	out, e = member(ctx, tx, v.ID, actor)
	if e != nil {
		return
	}
	now := r.cfg.Now().UTC().Truncate(time.Microsecond)
	action := "profile.updated"
	if clear {
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_realm_member_profiles WHERE member_id=$1::uuid AND realm_id=$2::uuid`, out.ID, v.ID)
		action = "profile.cleared"
	} else {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_member_profiles(member_id,realm_id,joined_at,nick,bio,updated_at)VALUES($1::uuid,$2::uuid,$3::timestamptz,$4::text,$5::text,$6::timestamptz)ON CONFLICT(member_id)DO UPDATE SET realm_id=EXCLUDED.realm_id,joined_at=EXCLUDED.joined_at,nick=EXCLUDED.nick,bio=EXCLUDED.bio,updated_at=EXCLUDED.updated_at`, out.ID, v.ID, out.JoinedAt, q.Nick, q.Bio, now)
	}
	if e != nil {
		return
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET updated_at=$2::timestamptz WHERE id=$1::uuid`, out.ID, now); e != nil {
		return
	}
	out.UpdatedAt = now
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_profile_audit(actor_id,realm_id,member_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,$5::timestamptz)`, actor, v.ID, out.ID, action, now); e != nil {
		return
	}
	if e = r.EnrichMemberInTransaction(ctx, tx, &out); e != nil {
		return
	}
	// Subscription time and original session/scope proof are checked again after
	// every query and potential trigger wait. Failure rolls back all profile data.
	if e = r.profileAuthority(ctx, tx, actor, v, !clear); e != nil {
		return
	}
	return out, tx.Commit()
}
func (r *Repository) ModerationLogs(ctx context.Context, actor, ref string, offset, take int) (out []m.ModerationLog, total int64, e error) {
	out = []m.ModerationLog{}
	defer func() { e = mapped(e) }()
	if !authority.ValidID(actor) {
		return out, 0, authority.ErrInvalid
	}
	if social.ValidatePage(offset, take) != nil {
		return out, 0, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	check := func() error {
		if _, e := r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
			return e
		}
		if e := currentAccount(ctx, tx, v.Owner); e != nil {
			return e
		}
		if e := activeIdentity(ctx, tx, actor, v); e != nil {
			return e
		}
		p, e := r.permissions.RealmPermissionsInTransaction(ctx, tx, actor, v.ID)
		if e != nil {
			return e
		}
		if !p.Allows(social.CanModeratePosts) {
			return m.ErrForbidden
		}
		return nil
	}
	if e = check(); e != nil {
		return
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_realm_post_moderation WHERE realm_id=$1::uuid`, v.ID).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,realm_id::text,post_id::text,moderator_id::text,reason,created_at FROM owned_realm_post_moderation WHERE realm_id=$1::uuid ORDER BY created_at DESC,id OFFSET $2 LIMIT $3`, v.ID, offset, take)
	if e != nil {
		return
	}
	for rows.Next() {
		var x m.ModerationLog
		if e = rows.Scan(&x.ID, &x.RealmID, &x.PostID, &x.ModeratorAccountID, &x.Reason, &x.CreatedAt); e != nil {
			rows.Close()
			return
		}
		x.ModeratedAt = x.CreatedAt
		x.UpdatedAt = x.CreatedAt
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	if e = check(); e != nil {
		return
	}
	return out, total, tx.Commit()
}
