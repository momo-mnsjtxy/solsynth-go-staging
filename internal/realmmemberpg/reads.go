// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
)

const memberColumns = `m.id::text,m.realm_id::text,m.account_id::text,m.role,m.joined_at,m.leave_at,m.created_at,m.updated_at,a.id::text,a.name,a.nick,a.language,a.region,a.created_at`

func scanMember(s scanner) (out social.Member, e error) {
	a := &accounts.Account{}
	e = s.Scan(&out.ID, &out.RealmID, &out.AccountID, &out.Role, &out.JoinedAt, &out.LeaveAt, &out.CreatedAt, &out.UpdatedAt, &a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt)
	out.Account = a
	return out, mapped(e)
}
func member(ctx context.Context, tx *sql.Tx, realm, account string) (social.Member, error) {
	return scanMember(tx.QueryRowContext(ctx, `SELECT `+memberColumns+` FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id WHERE m.realm_id=$1::uuid AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND NOT a.disabled`, realm, account))
}
func (r *Repository) EnrichMemberInTransaction(ctx context.Context, tx *sql.Tx, out *social.Member) error {
	if r == nil || tx == nil || out == nil {
		return m.ErrUnavailable
	}
	out.Label = nil
	out.LabelID = nil
	out.Progression = nil
	out.Nick = nil
	out.Bio = nil
	if out.JoinedAt == nil || out.LeaveAt != nil {
		return nil
	}
	e := tx.QueryRowContext(ctx, `SELECT p.nick,p.bio FROM owned_realm_member_profiles p JOIN owned_realm_members m ON m.id=p.member_id AND m.realm_id=p.realm_id AND m.joined_at=p.joined_at WHERE m.id=$1::uuid AND m.realm_id=$2::uuid AND m.account_id=$3::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL`, out.ID, out.RealmID, out.AccountID).Scan(&out.Nick, &out.Bio)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	v, e := label(tx.QueryRowContext(ctx, `SELECT l.id::text,l.realm_id::text,l.name,l.description,l.color,l.icon,l.created_by_account_id::text,l.created_at,l.updated_at FROM owned_realm_label_assignments x JOIN owned_realm_labels l ON l.id=x.label_id AND l.realm_id=x.realm_id JOIN owned_realm_members m ON m.id=x.member_id AND m.realm_id=x.realm_id AND m.joined_at=x.joined_at WHERE m.id=$1::uuid AND m.realm_id=$2::uuid AND m.account_id=$3::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL`, out.ID, out.RealmID, out.AccountID))
	if e != nil && !errors.Is(e, m.ErrNotFound) {
		return e
	}
	if e == nil {
		out.Label = &v
		id := v.ID
		out.LabelID = &id
	}
	p, e := r.progression(ctx, tx, out.RealmID, out.AccountID)
	if e != nil {
		return e
	}
	out.Progression = &p
	return nil
}
func (r *Repository) MembersWithLabel(ctx context.Context, actor, ref, id string, offset, take int) (out []social.Member, total int64, e error) {
	out = []social.Member{}
	if !authority.ValidID(id) || social.ValidatePage(offset, take) != nil {
		return out, 0, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_labels WHERE id=$1::uuid AND realm_id=$2::uuid)`, id, v.ID).Scan(&exists); e != nil {
		return
	}
	if !exists {
		return out, 0, m.ErrLabelRealm
	}
	from := ` FROM owned_realm_members m JOIN owned_authority_accounts a ON a.id=m.account_id JOIN owned_realm_label_assignments x ON x.member_id=m.id AND x.realm_id=m.realm_id AND x.joined_at=m.joined_at WHERE m.realm_id=$1::uuid AND x.label_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND NOT a.disabled`
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+from, v.ID, id).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+from+` ORDER BY m.role DESC,m.created_at,m.id OFFSET $3 LIMIT $4`, v.ID, id, offset, take)
	if e != nil {
		return
	}
	for rows.Next() {
		x, err := scanMember(rows)
		if err != nil {
			rows.Close()
			return out, 0, err
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	for i := range out {
		if e = r.EnrichMemberInTransaction(ctx, tx, &out[i]); e != nil {
			return
		}
	}
	return out, total, tx.Commit()
}
func project(experience int64, thresholds []int64) social.MemberProgression {
	out := social.MemberProgression{RecordedExperience: experience, ExperienceComplete: false, Sources: []string{"realm.boost.purchase", "realm.tenure.daily"}}
	if len(thresholds) == 0 {
		return out
	}
	i := sort.Search(len(thresholds), func(i int) bool { return thresholds[i] > experience }) - 1
	if i < 0 {
		i = 0
	}
	level := i
	floor := thresholds[i]
	policy := "owned.configured.scoped"
	out.Level = &level
	out.LevelFloor = &floor
	out.LevelPolicy = &policy
	if i+1 < len(thresholds) {
		next := thresholds[i+1]
		out.NextLevelAt = &next
	}
	return out
}
func (r *Repository) progression(ctx context.Context, tx *sql.Tx, realm, account string) (social.MemberProgression, error) {
	var total int64
	e := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(delta),0)::bigint FROM(SELECT delta FROM owned_realm_boost_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid UNION ALL SELECT delta FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid)x`, realm, account).Scan(&total)
	return project(total, r.cfg.LevelThresholds), e
}
func (r *Repository) Progression(ctx context.Context, actor, ref, account string) (out social.MemberProgression, e error) {
	if !authority.ValidID(account) {
		return out, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = member(ctx, tx, v.ID, account); e != nil {
		return
	}
	out, e = r.progression(ctx, tx, v.ID, account)
	if e != nil {
		return
	}
	return out, tx.Commit()
}
func (r *Repository) History(ctx context.Context, actor, ref, account string, take int) (out []b.Experience, e error) {
	out = []b.Experience{}
	if !authority.ValidID(account) || take < 1 || take > 200 {
		return out, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = member(ctx, tx, v.ID, account); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,realm_id::text,account_id::text,reason_type,reason,delta,created_at FROM(SELECT id,realm_id,account_id,'realm.boost.purchase'::text AS reason_type,order_id::text AS reason,delta,created_at FROM owned_realm_boost_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid UNION ALL SELECT id,realm_id,account_id,'realm.tenure.daily'::text,to_char(day,'YYYY-MM-DD'),delta,created_at FROM owned_realm_tenure_experience WHERE realm_id=$1::uuid AND account_id=$2::uuid)x ORDER BY created_at DESC,reason_type,id LIMIT $3`, v.ID, account, take)
	if e != nil {
		return
	}
	for rows.Next() {
		var x b.Experience
		if e = rows.Scan(&x.ID, &x.RealmID, &x.AccountID, &x.ReasonType, &x.Reason, &x.Delta, &x.CreatedAt); e != nil {
			rows.Close()
			return
		}
		x.UpdatedAt = x.CreatedAt
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	return out, tx.Commit()
}
