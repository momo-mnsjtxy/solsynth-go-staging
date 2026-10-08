// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"time"
)

var _ rooms.RelationRepository = (*Repository)(nil)

type authorityPreflight interface {
	Preflight(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}

func (r *Repository) preflight(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if guard, ok := r.guard.(authorityPreflight); ok {
		var permissions []string
		if permission != "" {
			permissions = []string{permission}
		}
		_, e := guard.Preflight(ctx, r.db, tx, actor, permissions...)
		return e
	}
	// Preserve exact denials from existing custom guards before any domain SQL.
	// A successful guard without a nonlocking preflight cannot safely establish
	// the complete multi-account lock order, so it never reaches domain writes.
	var e error
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	if e != nil {
		return e
	}
	return rooms.ErrUnavailable
}

func (r *Repository) associated(ctx context.Context, tx *sql.Tx, rid string) (ids, realms []string, e error) {
	if rid == "" {
		return nil, nil, nil
	}
	var guarded bool
	var kind int
	if e = tx.QueryRowContext(ctx, `SELECT owned_relation_guard,owned_type FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL`, rid).Scan(&guarded, &kind); errors.Is(e, sql.ErrNoRows) {
		return nil, nil, rooms.ErrNotFound
	}
	if e != nil || !guarded {
		return
	}
	if r.social == nil {
		return nil, nil, rooms.ErrUnavailable
	}
	if kind == 1 {
		var a, b string
		e = tx.QueryRowContext(ctx, `SELECT account_low::text,account_high::text FROM owned_chat_direct_pairs WHERE room_id=$1`, rid).Scan(&a, &b)
		return []string{a, b}, nil, e
	}
	if kind != 0 {
		return nil, nil, rooms.ErrUnsupported
	}
	var realm, owner string
	e = tx.QueryRowContext(ctx, `SELECT realm.id::text,realm.account_id::text FROM owned_chat_realm_links link JOIN owned_realms realm ON realm.id=link.realm_id WHERE link.room_id=$1`, rid).Scan(&realm, &owner)
	return []string{owner}, []string{realm}, e
}
func dedup(ids []string) []string {
	sort.Strings(ids)
	out := []string{}
	for _, id := range ids {
		if id != "" && (len(out) == 0 || out[len(out)-1] != id) {
			out = append(out, id)
		}
	}
	return out
}

// Complete canonical account set precedes the guard; realm locks precede room
// locks. Association changes during the wait fail with a retryable conflict.
func (r *Repository) beginRoomWrite(ctx context.Context, actor, rid, permission, newRealm string, extra ...string) (tx *sql.Tx, e error) {
	if r == nil || (!r.fixture && r.guard == nil) {
		return nil, rooms.ErrUnavailable
	}
	tx, e = r.begin(ctx, false)
	if e != nil {
		return nil, e
	}
	fail := func(err error) (*sql.Tx, error) { _ = tx.Rollback(); return nil, err }
	if !r.fixture {
		if e = r.preflight(ctx, tx, actor, permission); e != nil {
			return fail(e)
		}
	}
	ids, realms, e := r.associated(ctx, tx, rid)
	if e != nil {
		return fail(e)
	}
	initial := append([]string{}, realms...)
	if newRealm != "" {
		if r.social == nil {
			return fail(rooms.ErrUnavailable)
		}
		if !rooms.ValidID(newRealm) {
			return fail(rooms.ErrInvalid)
		}
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1`, newRealm).Scan(&owner); e != nil {
			return fail(rooms.ErrNotFound)
		}
		ids = append(ids, owner)
		realms = append(realms, newRealm)
	}
	ids = dedup(append(append(ids, extra...), actor))
	for _, id := range ids {
		if !rooms.ValidID(id) {
			return fail(rooms.ErrInvalid)
		}
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&found); e != nil {
			return fail(e)
		}
	}
	if !r.fixture {
		if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if e != nil {
			return fail(e)
		}
	}
	for _, id := range dedup(realms) {
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1 FOR UPDATE`, id).Scan(&owner); e != nil {
			return fail(e)
		}
		if !hasID(ids, owner) {
			return fail(rooms.ErrConflict)
		}
		if id == newRealm {
			if e = activeOther(ctx, tx, owner); e != nil {
				return fail(e)
			}
		}
	}
	if rid != "" {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, rid).Scan(&found); e != nil {
			return fail(rooms.ErrNotFound)
		}
		freshIDs, freshRealms, x := r.associated(ctx, tx, rid)
		if x != nil {
			return fail(x)
		}
		if len(initial) != len(freshRealms) {
			return fail(rooms.ErrConflict)
		}
		for i := range initial {
			if initial[i] != freshRealms[i] {
				return fail(rooms.ErrConflict)
			}
		}
		for _, id := range freshIDs {
			if !hasID(ids, id) {
				return fail(rooms.ErrConflict)
			}
		}
	}
	return tx, nil
}
func hasID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (r *Repository) commit(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if !r.fixture {
		var e error
		if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}

// Other canonical adapters may already own the actor lock. Secondary NOWAIT
// locks avoid inversion while retaining policy rows through their commit.
func (r *Repository) LockForAction(ctx context.Context, tx *sql.Tx, actor, rid string) error {
	if tx == nil || !rooms.ValidID(actor) || !rooms.ValidID(rid) {
		return rooms.ErrInvalid
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil {
		return e
	}
	if isolation != "read committed" {
		return rooms.ErrUnavailable
	}
	ids, realms, e := r.associated(ctx, tx, rid)
	if e != nil {
		return e
	}
	for _, id := range dedup(ids) {
		if id == actor {
			continue
		}
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR SHARE NOWAIT`, id).Scan(&found); e != nil {
			return rooms.ErrUnavailable
		}
	}
	for _, id := range dedup(realms) {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1 FOR SHARE NOWAIT`, id).Scan(&found); e != nil {
			return rooms.ErrUnavailable
		}
	}
	var room string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE NOWAIT`, rid).Scan(&room); e != nil {
		return rooms.ErrUnavailable
	}
	freshIDs, freshRealms, e := r.associated(ctx, tx, rid)
	if e != nil {
		return e
	}
	if len(freshRealms) != len(realms) || len(freshIDs) != len(ids) {
		return rooms.ErrConflict
	}
	for _, id := range freshIDs {
		if !hasID(ids, id) {
			return rooms.ErrConflict
		}
	}
	for _, id := range freshRealms {
		if !hasID(realms, id) {
			return rooms.ErrConflict
		}
	}
	return nil
}
func relationAudit(ctx context.Context, tx *sql.Tx, actor, rid, action string, target any, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_chat_relations_audit(actor_id,room_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4,$5)`, actor, rid, action, target, now)
	return e
}
func activeOther(ctx context.Context, tx *sql.Tx, id string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))FROM owned_authority_accounts a WHERE id=$1`, id).Scan(&active)
	if e != nil || !active {
		return rooms.ErrForbidden
	}
	return nil
}

func (r *Repository) CreateDirect(ctx context.Context, actor string, p rooms.DirectRequest) (out rooms.Room, e error) {
	if r == nil || r.social == nil {
		return out, rooms.ErrUnavailable
	}
	if valid(actor, p.RelatedUserID) != nil || actor == p.RelatedUserID {
		return out, rooms.ErrInvalid
	}
	if p.EncryptionMode != nil && *p.EncryptionMode != r.policy.NoneEncryptionMode {
		return out, rooms.ErrUnsupported
	}
	tx, e := r.beginRoomWrite(ctx, actor, "", "chat.create", "", p.RelatedUserID)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = activeOther(ctx, tx, p.RelatedUserID); e != nil {
		return out, e
	}
	blocked, e := r.social.IsBlockedInTransaction(ctx, tx, actor, p.RelatedUserID)
	if e != nil {
		return out, e
	}
	if blocked {
		return out, rooms.ErrForbidden
	}
	pair := dedup([]string{actor, p.RelatedUserID})
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_chat_direct_pairs pair JOIN chat_rooms room ON room.id=pair.room_id WHERE pair.account_low=$1 AND pair.account_high=$2 AND room.deleted_at IS NULL)`, pair[0], pair[1]).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		return out, rooms.ErrConflict
	}
	rid, e := id()
	if e != nil {
		return out, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	out, e = scanRoom(tx.QueryRowContext(ctx, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_type,owned_relation_guard,owned_created_at,owned_updated_at)VALUES($1,false,$2,$3,1,true,$4,$4) RETURNING `+roomColumns, rid, r.policy.NoneEncryptionMode, actor, now))
	if e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_direct_pairs(account_low,account_high,room_id)VALUES($1,$2,$3)ON CONFLICT(account_low,account_high)DO UPDATE SET room_id=EXCLUDED.room_id`, pair[0], pair[1], rid); e != nil {
		return out, e
	}
	for _, account := range pair {
		mid, x := id()
		if x != nil {
			return out, x
		}
		var confirmed any
		if account == actor {
			confirmed = now
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,last_read_at,owned_confirmed_at,owned_updated_at)VALUES($1,$2,$3,$4,$5,$5,$6,$5)`, mid, rid, account, r.policy.NotifyAll, now, confirmed); e != nil {
			return out, e
		}
	}
	if e = relationAudit(ctx, tx, actor, rid, "direct.create", p.RelatedUserID, now); e != nil {
		return out, e
	}
	if e = r.decorateRoom(ctx, tx, &out, actor); e != nil {
		return out, e
	}
	return out, r.commit(ctx, tx, actor, "chat.create")
}
func (r *Repository) GetDirect(ctx context.Context, actor, target string) (out rooms.Room, e error) {
	if r == nil || r.social == nil {
		return out, rooms.ErrUnavailable
	}
	if valid(actor, target) != nil || actor == target {
		return out, rooms.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	pair := dedup([]string{actor, target})
	var rid string
	if e = tx.QueryRowContext(ctx, `SELECT room_id::text FROM owned_chat_direct_pairs WHERE account_low=$1 AND account_high=$2`, pair[0], pair[1]).Scan(&rid); e != nil {
		return out, rooms.ErrNotFound
	}
	out, e = r.room(ctx, tx, rid, false)
	if e != nil {
		return out, e
	}
	if e = r.decorateRoom(ctx, tx, &out, actor); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) ChangeDirect(ctx context.Context, actor, rid, action string) (out rooms.Room, e error) {
	if r == nil || r.social == nil {
		return out, rooms.ErrUnavailable
	}
	if valid(actor, rid) != nil {
		return out, rooms.ErrInvalid
	}
	if action != "confirm" && action != "block" && action != "unblock" {
		return out, rooms.ErrInvalid
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	out, e = r.room(ctx, tx, rid, true)
	if e != nil {
		return out, e
	}
	if out.Type != 1 || actor == out.AccountID {
		return out, rooms.ErrForbidden
	}
	m, e := member(ctx, tx, rid, actor, false)
	if e != nil || m.JoinedAt == nil {
		return out, rooms.ErrForbidden
	}
	if m.ConfirmedAt != nil {
		return out, rooms.ErrConflict
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	switch action {
	case "confirm":
		if m.LeaveAt != nil {
			return out, rooms.ErrForbidden
		}
		yes, x := r.CanReadInTransaction(ctx, tx, actor, rid)
		if x != nil {
			return out, x
		}
		if !yes {
			return out, rooms.ErrForbidden
		}
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET owned_confirmed_at=$2,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
	case "block":
		if m.LeaveAt != nil {
			return out, rooms.ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET leave_at=$2,owned_banned=true,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
	case "unblock":
		if m.LeaveAt == nil {
			return out, rooms.ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET leave_at=NULL,joined_at=$2,owned_banned=false,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
	}
	if e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE chat_rooms SET owned_updated_at=$2 WHERE id=$1`, rid, now); e != nil {
		return out, e
	}
	out.UpdatedAt = now
	if e = relationAudit(ctx, tx, actor, rid, "direct."+action, nil, now); e != nil {
		return out, e
	}
	if e = r.decorateRoom(ctx, tx, &out, actor); e != nil {
		return out, e
	}
	return out, r.commit(ctx, tx, actor, "")
}

func (r *Repository) RealmRooms(ctx context.Context, actor, ref string, take, offset int) (out []rooms.Room, e error) {
	out = []rooms.Room{}
	if r == nil || r.social == nil {
		return out, rooms.ErrUnavailable
	}
	if valid(actor) != nil || page(take, offset) != nil || len(ref) > 256 {
		return out, rooms.ErrInvalid
	}
	tx, e := r.begin(ctx, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	realm, e := r.social.RealmInTransaction(ctx, tx, ref)
	if e != nil {
		return out, rooms.ErrNotFound
	}
	if !realm.IsPublic {
		if _, e = r.social.RealmRoleInTransaction(ctx, tx, actor, realm.ID); e != nil {
			return out, rooms.ErrForbidden
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT room_id::text FROM owned_chat_realm_links link JOIN chat_rooms room ON room.id=link.room_id WHERE link.realm_id=$1 AND room.deleted_at IS NULL ORDER BY room.owned_updated_at DESC,room.id LIMIT $2 OFFSET $3`, realm.ID, take, offset)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, id := range ids {
		v, x := r.room(ctx, tx, id, false)
		if x != nil {
			return out, x
		}
		out = append(out, v)
	}
	return out, tx.Commit()
}
func (r *Repository) manage(ctx context.Context, tx *sql.Tx, v rooms.Room, actor string) error {
	if v.RealmID != nil {
		if r.social == nil {
			return rooms.ErrUnavailable
		}
		e := r.realmPermission(ctx, tx, actor, *v.RealmID, social.CanModerateChat)
		if e != nil {
			return rooms.ErrForbidden
		}
		allowed, e := r.CanReadInTransaction(ctx, tx, actor, v.ID)
		if e != nil {
			return e
		}
		if !allowed {
			return rooms.ErrForbidden
		}
		return nil
	}
	if v.Type != 1 && v.AccountID != actor {
		return rooms.ErrForbidden
	}
	_, e := requireMember(ctx, tx, v.ID, actor)
	return e
}
func (r *Repository) Timeout(ctx context.Context, actor, rid, target string, p *rooms.TimeoutRequest) error {
	if valid(actor, rid, target) != nil || target == actor {
		return rooms.ErrInvalid
	}
	if e := rooms.ValidateTimeout(p); e != nil {
		return e
	}
	permission := "chat.members.timeout"
	tx, e := r.beginRoomWrite(ctx, actor, rid, permission, "", target)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := r.room(ctx, tx, rid, true)
	if e != nil {
		return e
	}
	if v.Type == 1 {
		return rooms.ErrForbidden
	}
	if e = r.manage(ctx, tx, v, actor); e != nil {
		return e
	}
	m, e := requireMember(ctx, tx, rid, target)
	if e != nil {
		return e
	}
	if v.RealmID == nil && target == v.AccountID {
		return rooms.ErrForbidden
	}
	if v.RealmID != nil {
		actorRole, x := r.social.RealmRoleInTransaction(ctx, tx, actor, *v.RealmID)
		targetRole, y := r.social.RealmRoleInTransaction(ctx, tx, target, *v.RealmID)
		if x != nil || y != nil || targetRole >= actorRole {
			return rooms.ErrForbidden
		}
	}
	op, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return e
	}
	if p == nil {
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET owned_timeout_until=NULL,owned_timeout_reason=NULL,owned_timeout_sender=NULL,owned_timeout_since=NULL,owned_updated_at=$2 WHERE id=$1`, m.ID, now)
	} else {
		if !p.TimeoutUntil.After(now) || p.TimeoutUntil.After(now.Add(30*24*time.Hour)) {
			return rooms.ErrInvalid
		}
		_, e = tx.ExecContext(ctx, `UPDATE chat_members SET owned_timeout_until=$2,owned_timeout_reason=$3,owned_timeout_sender=$4,owned_timeout_since=$5,owned_updated_at=$5 WHERE id=$1`, m.ID, p.TimeoutUntil, p.Reason, op.ID, now)
	}
	if e != nil {
		return e
	}
	if r.social != nil {
		if e = relationAudit(ctx, tx, actor, rid, "member.timeout", target, now); e != nil {
			return e
		}
	}
	return r.commit(ctx, tx, actor, permission)
}
