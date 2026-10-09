// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
)

var _ rooms.DetailRepository = (*Repository)(nil)

func (r *Repository) WithProfilePerks(p rooms.ProfileEntitlement) *Repository {
	r.profilePerks = p
	return r
}
func normalizedSlug(p *string) *string { v, _ := rooms.NormalizeSlug(p); return v }
func slugRealm(p rooms.RoomPatch, v rooms.Room) *string {
	if p.RealmID != nil {
		return p.RealmID
	}
	return v.RealmID
}
func mapDetailError(e error) error {
	var pg interface{ SQLState() string }
	if errors.As(e, &pg) && pg.SQLState() == "23505" {
		return rooms.ErrConflict
	}
	return e
}
func (r *Repository) detailRead(ctx context.Context, actor string) (*sql.Tx, error) {
	if actor == "" {
		return r.begin(ctx, false)
	}
	if e := valid(actor); e != nil {
		return nil, e
	}
	return r.beginWrite(ctx, actor, "")
}
func (r *Repository) detailCommit(ctx context.Context, tx *sql.Tx, actor string) error {
	if actor == "" {
		return tx.Commit()
	}
	return r.commit(ctx, tx, actor, "")
}

// Gate bare room metadata before hydrating realm/media data. An inaccessible
// room with an unsupported mode or unavailable decorator still has the same 404
// boundary as an absent room; private existence never depends on hydration.
func (r *Repository) detailRoom(ctx context.Context, tx *sql.Tx, actor, rid string, publicOnly bool) (rooms.Room, error) {
	v, e := scanRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL AND owned_owner_id IS NOT NULL`, rid))
	if e != nil {
		return rooms.Room{}, e
	}
	if publicOnly && (!v.IsPublic || v.Type != 0 || v.EncryptionMode != r.policy.NoneEncryptionMode) {
		return rooms.Room{}, rooms.ErrNotFound
	}
	if v.EncryptionMode != r.policy.NoneEncryptionMode && !(r.mls && v.EncryptionMode == rooms.MLSEncryptionMode) {
		return rooms.Room{}, rooms.ErrNotFound
	}
	if e = r.readable(ctx, tx, v, actor); errors.Is(e, rooms.ErrForbidden) {
		return rooms.Room{}, rooms.ErrNotFound
	} else if e != nil {
		return rooms.Room{}, e
	}
	if publicOnly && actor != "" && actor != v.AccountID {
		if r.social == nil {
			return rooms.Room{}, rooms.ErrUnavailable
		}
		blocked, x := r.social.IsBlockedInTransaction(ctx, tx, actor, v.AccountID)
		if x != nil {
			return rooms.Room{}, x
		}
		if blocked {
			return rooms.Room{}, rooms.ErrNotFound
		}
	}
	if v.RelationGuard && v.Type == 0 {
		var realm string
		if e = tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_chat_realm_links WHERE room_id=$1`, rid).Scan(&realm); e != nil {
			return rooms.Room{}, e
		}
		v.RealmID = &realm
	}
	return v, nil
}

// Resolving both branches together preserves account-room precedence. A later
// re-resolution must select this exact room too: checking only the old room's
// alias can overlook account name reuse or a new higher-precedence account room.
func detailSlugID(ctx context.Context, tx *sql.Tx, scope, slug string) (string, error) {
	var rid string
	e := tx.QueryRowContext(ctx, detailSlugSQL, scope, slug).Scan(&rid)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	return rid, e
}

const detailSlugSQL = `SELECT id::text FROM (
 SELECT r.id,0 AS precedence FROM chat_rooms r JOIN owned_authority_accounts a ON a.id=r.owned_owner_id
 WHERE a.name=$1 AND NOT a.disabled AND r.owned_slug=$2 AND r.deleted_at IS NULL AND r.owned_type=0 AND r.owned_slug_realm_id IS NULL AND NOT r.owned_relation_guard
 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))
 UNION ALL
 SELECT r.id,1 FROM chat_rooms r JOIN owned_chat_realm_links l ON l.room_id=r.id JOIN owned_realms realm ON realm.id=l.realm_id JOIN owned_authority_accounts a ON a.id=realm.account_id
 WHERE lower(realm.slug)=lower($1) AND realm.deleted_at IS NULL AND NOT a.disabled AND r.owned_slug=$2 AND r.deleted_at IS NULL AND r.owned_type=0 AND r.owned_relation_guard AND r.owned_slug_realm_id=l.realm_id
 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp()))
 ) scopes ORDER BY precedence,id LIMIT 1`

func (r *Repository) PublicBySlug(ctx context.Context, actor, scope, slug string) (rooms.Room, error) {
	var out rooms.Room
	p, e := rooms.NormalizeSlug(&slug)
	if e != nil || p == nil || scope == "" || len(scope) > 256 || strings.TrimSpace(scope) != scope {
		return out, rooms.ErrNotFound
	}
	tx, e := r.detailRead(ctx, actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rid, e := detailSlugID(ctx, tx, scope, *p)
	if e != nil {
		return out, e
	}
	out, e = r.detailRoom(ctx, tx, actor, rid, true)
	if e != nil {
		return rooms.Room{}, e
	}
	if e = detailReadLock(ctx, tx, "chat_rooms", rid); e != nil {
		return rooms.Room{}, e
	}
	// The association may have changed between discovery and the room lock.
	out, e = r.detailRoom(ctx, tx, actor, rid, true)
	if e != nil {
		return rooms.Room{}, e
	}
	if e = detailReadLock(ctx, tx, "owned_authority_accounts", out.AccountID); e != nil {
		return rooms.Room{}, e
	}
	if out.RealmID != nil {
		if e = detailReadLock(ctx, tx, "owned_realms", *out.RealmID); e != nil {
			return rooms.Room{}, e
		}
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1`, *out.RealmID).Scan(&owner); e != nil {
			return rooms.Room{}, e
		}
		if e = detailReadLock(ctx, tx, "owned_authority_accounts", owner); e != nil {
			return rooms.Room{}, e
		}
	}
	// All response hydration follows the nonblocking canonical lock set.
	out, e = r.detailRoom(ctx, tx, actor, rid, true)
	if e != nil {
		return rooms.Room{}, e
	}
	if e = activeOther(ctx, tx, out.AccountID); e != nil {
		return rooms.Room{}, rooms.ErrNotFound
	}
	if e = r.decorateRoom(ctx, tx, &out, actor); e != nil {
		return rooms.Room{}, e
	}
	// Recheck current identity, visibility and timed restrictions after hydration.
	current, e := detailSlugID(ctx, tx, scope, *p)
	if e != nil {
		return rooms.Room{}, e
	}
	if current != rid {
		return rooms.Room{}, rooms.ErrConflict
	}
	if _, e = r.detailRoom(ctx, tx, actor, rid, true); e != nil {
		return rooms.Room{}, e
	}
	if e = activeOther(ctx, tx, out.AccountID); e != nil {
		return rooms.Room{}, rooms.ErrNotFound
	}
	if e = r.detailCommit(ctx, tx, actor); e != nil {
		return rooms.Room{}, e
	}
	return out, nil
}

// Secondary read locks never wait after an actor/room lock. Canonical presence,
// relationship, session and account writers use the same account rows; NOWAIT
// makes a competing multi-account writer a retryable conflict instead of an
// inversion or a response assembled across privacy changes.
func detailReadLock(ctx context.Context, tx *sql.Tx, table, id string) error {
	if table != "owned_authority_accounts" && table != "chat_rooms" && table != "owned_realms" {
		return rooms.ErrInvalid
	}
	var got string
	e := tx.QueryRowContext(ctx, `SELECT id::text FROM `+table+` WHERE id=$1 FOR SHARE NOWAIT`, id).Scan(&got)
	if errors.Is(e, sql.ErrNoRows) {
		return rooms.ErrNotFound
	}
	var pg interface{ SQLState() string }
	if errors.As(e, &pg) && pg.SQLState() == "55P03" {
		return rooms.ErrConflict
	}
	return e
}
func (r *Repository) OnlineMembers(ctx context.Context, actor, rid string) (out rooms.OnlineMembers, e error) {
	out = rooms.OnlineMembers{OnlineUserNames: []string{}, OnlineAccounts: []accounts.Account{}}
	defer func() {
		if e != nil {
			out = rooms.OnlineMembers{}
		}
	}()
	if e := valid(rid); e != nil {
		return out, e
	}
	tx, e := r.detailRead(ctx, actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	room, e := r.detailRoom(ctx, tx, actor, rid, false)
	if e != nil {
		return out, e
	}
	if e = r.readable(ctx, tx, room, actor); errors.Is(e, rooms.ErrForbidden) {
		return out, rooms.ErrNotFound
	} else if e != nil {
		return out, e
	}
	if e = detailReadLock(ctx, tx, "chat_rooms", rid); e != nil {
		return out, e
	}
	room, e = r.detailRoom(ctx, tx, actor, rid, false)
	if e != nil {
		return out, e
	}
	if e = r.readable(ctx, tx, room, actor); errors.Is(e, rooms.ErrForbidden) {
		return out, rooms.ErrNotFound
	} else if e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.account_id::text FROM chat_members m WHERE m.chat_room_id=$1 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned ORDER BY m.account_id LIMIT 10001`, rid)
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
	if len(ids) > 10000 {
		return out, rooms.ErrUnavailable
	}
	lockIDs := append([]string{}, ids...)
	lockIDs = append(lockIDs, room.AccountID)
	if room.RealmID != nil {
		if e = detailReadLock(ctx, tx, "owned_realms", *room.RealmID); e != nil {
			return out, e
		}
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1`, *room.RealmID).Scan(&owner); e != nil {
			return out, e
		}
		lockIDs = append(lockIDs, owner)
	}
	for _, id := range dedup(lockIDs) {
		if e = detailReadLock(ctx, tx, "owned_authority_accounts", id); e != nil {
			return out, e
		}
	}
	if e = activeOther(ctx, tx, room.AccountID); e != nil {
		return out, rooms.ErrNotFound
	}
	// Hydrate account fields before the final presence observation; no private
	// account fields, activities or room decorators participate in this response.
	projected := map[string]*accounts.Account{}
	for _, id := range ids {
		a, x := canonicalAccount(ctx, tx, id)
		if errors.Is(x, sql.ErrNoRows) {
			continue
		}
		if x != nil {
			return out, x
		}
		projected[id] = a
	}
	if _, e = r.detailRoom(ctx, tx, actor, rid, false); e != nil {
		return out, e
	}
	statuses, e := presencepg.ChatStatusesInTransaction(ctx, tx, actor, ids)
	if e != nil {
		return out, e
	}
	for _, id := range ids {
		status, exists := statuses[id]
		if !exists {
			continue
		}
		if room.Type == 1 && id != actor {
			out.DirectMessageStatus = &status
		}
		if !status.IsOnline {
			continue
		}
		out.OnlineCount++
		if room.Type == 1 {
			continue
		}
		a := projected[id]
		if a == nil {
			return out, rooms.ErrConflict
		}
		out.OnlineAccounts = append(out.OnlineAccounts, *a)
		if strings.TrimSpace(a.Nick) != "" {
			out.OnlineUserNames = append(out.OnlineUserNames, a.Nick)
		}
	}
	if _, e = r.detailRoom(ctx, tx, actor, rid, false); e != nil {
		return out, e
	}
	if e = activeOther(ctx, tx, room.AccountID); e != nil {
		return out, rooms.ErrNotFound
	}
	if e = r.detailCommit(ctx, tx, actor); e != nil {
		return rooms.OnlineMembers{}, e
	}
	return out, nil
}
func (r *Repository) UpdateProfile(ctx context.Context, actor, rid string, q rooms.ProfileRequest) (rooms.Member, error) {
	if e := rooms.ValidateProfile(q); e != nil {
		return rooms.Member{}, e
	}
	return r.changeProfile(ctx, actor, rid, q, false)
}
func (r *Repository) ClearProfile(ctx context.Context, actor, rid string) (rooms.Member, error) {
	return r.changeProfile(ctx, actor, rid, rooms.ProfileRequest{}, true)
}
func (r *Repository) changeProfile(ctx context.Context, actor, rid string, q rooms.ProfileRequest, clear bool) (out rooms.Member, e error) {
	if e = valid(actor, rid); e != nil {
		return
	}
	permission := ""
	if !clear {
		permission = "chat.members.manage"
		if r.profilePerks == nil {
			return out, rooms.ErrUnavailable
		}
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, permission, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	check := func() error {
		room, x := r.detailRoom(ctx, tx, actor, rid, false)
		if x != nil {
			return x
		}
		if x = r.readable(ctx, tx, room, actor); x != nil {
			return x
		}
		if _, x = member(ctx, tx, rid, actor, true); x != nil {
			return x
		}
		if !clear {
			now, x := timestamp(ctx, tx)
			if x != nil {
				return x
			}
			return r.profilePerks.RequireChatProfile(ctx, r.db, tx, actor, now)
		}
		return nil
	}
	if e = check(); e != nil {
		return
	}
	out, e = member(ctx, tx, rid, actor, true)
	if e != nil {
		return
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	out.Nick = q.Nick
	if _, e = tx.ExecContext(ctx, `UPDATE chat_members SET owned_nick=$2,owned_updated_at=$3 WHERE id=$1`, out.ID, out.Nick, now); e != nil {
		return
	}
	action := "profile.updated"
	if clear {
		action = "profile.cleared"
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_detail_audit(actor_id,room_id,member_id,action,occurred_at)VALUES($1,$2,$3,$4,$5)`, actor, rid, out.ID, action, now); e != nil {
		return
	}
	out.Account, e = canonicalAccount(ctx, tx, actor)
	if e != nil {
		return
	}
	if e = check(); e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, permission)
	return
}
