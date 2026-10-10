// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"reflect"

	"solsynth.local/go-migration/ports/messager/rooms"
)

//go:embed organization_schema.sql
var OrganizationSchema string

// Organization composes existing canonical room writes and message hydration.
// A missing entitlement never enables cloud search. Schema installation is an
// explicit operator migration, separate from constructing the repository.
type Organization struct {
	r           *Repository
	reader      rooms.OrganizationMessageReader
	entitlement rooms.SearchEntitlement
}

var _ rooms.OrganizationRepository = (*Organization)(nil)

func NewOrganization(r *Repository, reader rooms.OrganizationMessageReader, entitlement rooms.SearchEntitlement) *Organization {
	return &Organization{r: r, reader: reader, entitlement: entitlement}
}
func (o *Organization) Ready(ctx context.Context) error {
	if o == nil || o.r == nil || o.r.db == nil || o.reader == nil {
		return rooms.ErrUnavailable
	}
	_, e := o.r.db.ExecContext(ctx, `SELECT id,account_id,name,sort_order FROM owned_chat_groups LIMIT 0;SELECT member_id,group_id,account_id FROM owned_chat_group_members LIMIT 0;SELECT id,room_id,message_id,expires_at FROM owned_chat_pins LIMIT 0;SELECT member_id,last_read_at FROM owned_chat_read_receipts LIMIT 0;SELECT id,member_id,room_id,account_id,last_read_at,excluded_device_id FROM owned_chat_read_events LIMIT 0;SELECT id,account_id,session_id,account_version,authorization_epoch,mls_device_id,expires_at,closed_at FROM owned_chat_connections LIMIT 0;SELECT connection_id,member_id,joined_at,expires_at FROM owned_chat_subscriptions LIMIT 0`)
	return e
}
func (o *Organization) valid(actor string) error {
	if e := valid(actor); e != nil {
		return e
	}
	if o == nil || o.r == nil || o.r.db == nil {
		return rooms.ErrUnavailable
	}
	return nil
}
func (o *Organization) activeRooms(ctx context.Context, tx *sql.Tx, actor string) ([]string, error) {
	rows, e := tx.QueryContext(ctx, `SELECT room.id::text FROM chat_rooms room JOIN chat_members member ON member.chat_room_id=room.id WHERE member.account_id=$1::uuid AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL AND `+o.r.ReadSQL("room.id", "$1")+` ORDER BY room.id LIMIT 1001`, actor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var rid string
		if e = rows.Scan(&rid); e != nil {
			return nil, e
		}
		out = append(out, rid)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(out) > 1000 {
		return nil, rooms.ErrUnavailable
	}
	return out, nil
}

// Aggregate operations gather all current associations before locking sorted
// accounts, then realms, then rooms, matching the existing forwarding protocol.
// No domain lookup precedes the original proof's nonlocking preflight. A changed
// membership/association set fails atomically instead of using a stale snapshot.
func (o *Organization) beginAll(ctx context.Context, actor, permission string) (*sql.Tx, []string, error) {
	if e := o.valid(actor); e != nil {
		return nil, nil, e
	}
	r := o.r
	if !r.fixture && r.guard == nil {
		return nil, nil, rooms.ErrUnavailable
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return nil, nil, e
	}
	fail := func(e error) (*sql.Tx, []string, error) { _ = tx.Rollback(); return nil, nil, e }
	if !r.fixture {
		if e = r.preflight(ctx, tx, actor, permission); e != nil {
			return fail(e)
		}
	}
	roomIDs, e := o.activeRooms(ctx, tx, actor)
	if e != nil {
		return fail(e)
	}
	accounts, realms := []string{actor}, []string{}
	type relation struct{ accounts, realms []string }
	initial := map[string]relation{}
	for _, rid := range roomIDs {
		ids, rs, x := r.associated(ctx, tx, rid)
		if x != nil {
			return fail(x)
		}
		initial[rid] = relation{dedup(ids), dedup(rs)}
		accounts = append(accounts, ids...)
		realms = append(realms, rs...)
	}
	accounts = dedup(accounts)
	realms = dedup(realms)
	for _, id := range accounts {
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
	} else if e = account(ctx, tx, actor); e != nil {
		return fail(e)
	}
	for _, id := range realms {
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_realms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&owner); e != nil {
			return fail(e)
		}
		if !hasID(accounts, owner) {
			return fail(rooms.ErrConflict)
		}
	}
	for _, rid := range roomIDs {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, rid).Scan(&found); e != nil {
			return fail(rooms.ErrConflict)
		}
		ids, rs, x := r.associated(ctx, tx, rid)
		if x != nil {
			return fail(x)
		}
		before := initial[rid]
		if !reflect.DeepEqual(dedup(ids), before.accounts) || !reflect.DeepEqual(dedup(rs), before.realms) {
			return fail(rooms.ErrConflict)
		}
	}
	fresh, e := o.activeRooms(ctx, tx, actor)
	if e != nil {
		return fail(e)
	}
	if !reflect.DeepEqual(fresh, roomIDs) {
		return fail(rooms.ErrConflict)
	}
	return tx, roomIDs, nil
}
func (o *Organization) commitAll(ctx context.Context, tx *sql.Tx, actor, permission string, ids []string) error {
	fresh, e := o.activeRooms(ctx, tx, actor)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(fresh, ids) {
		return rooms.ErrConflict
	}
	return o.r.commit(ctx, tx, actor, permission)
}
func organizationNotFound(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return rooms.ErrNotFound
	}
	return e
}
