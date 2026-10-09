// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

var _ rooms.ChatConnections = (*Organization)(nil)

type recordedChatAuthentication interface {
	StoredConnectionEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64) (bool, error)
}
type chatLease struct {
	id, actor, session, device string
	version, epoch             int64
	expires, eligibleUntil     time.Time
	closed                     *time.Time
}

func (o *Organization) connectionPrincipal(ctx context.Context, tx *sql.Tx, actor string) (authority.Principal, error) {
	if o == nil || o.r == nil || o.r.guard == nil {
		return authority.Principal{}, rooms.ErrUnavailable
	}
	p, e := o.r.guard.Authenticate(ctx, o.r.db, tx, actor)
	if e != nil {
		return p, e
	}
	if !rooms.ValidID(p.Session.ID) || p.Session.AuthorizationEpoch < 1 || p.Account.Version < 1 {
		return p, rooms.ErrForbidden
	}
	g, ok := o.r.guard.(recordedChatAuthentication)
	if !ok {
		return p, rooms.ErrUnavailable
	}
	eligible, e := g.StoredConnectionEligible(ctx, o.r.db, tx, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch)
	if e != nil {
		return p, e
	}
	if !eligible {
		return p, rooms.ErrForbidden
	}
	return p, nil
}
func (o *Organization) OpenConnection(ctx context.Context, actor string) (string, error) {
	if e := o.valid(actor); e != nil {
		return "", e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "", "")
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	p, e := o.connectionPrincipal(ctx, tx, actor)
	if e != nil {
		return "", e
	}
	device := rooms.MLSWriteFrom(ctx).Device
	if device != "" {
		if !o.r.mls || !rooms.ValidMLSIdentifier(device) {
			return "", rooms.ErrForbidden
		}
		if e = mlsDevice(ctx, tx, actor, device); e != nil {
			return "", e
		}
	}
	var n int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_chat_connections WHERE account_id=$1 AND closed_at IS NULL AND expires_at>clock_timestamp()`, actor).Scan(&n); e != nil {
		return "", e
	}
	if n >= 100 {
		return "", rooms.ErrConflict
	}
	id, e := id()
	if e != nil {
		return "", e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_connections(id,account_id,session_id,account_version,authorization_epoch,mls_device_id,created_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,clock_timestamp(),clock_timestamp()+interval '60 seconds')`, id, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, device)
	if e != nil {
		return "", e
	}
	if e = o.r.commit(ctx, tx, actor, ""); e != nil {
		return "", e
	}
	return id, nil
}
func (o *Organization) ownedConnection(ctx context.Context, tx *sql.Tx, actor, id string, allowClosed bool) (chatLease, error) {
	var v chatLease
	if !rooms.ValidID(id) {
		return v, rooms.ErrInvalid
	}
	p, e := o.connectionPrincipal(ctx, tx, actor)
	if e != nil {
		return v, e
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,session_id::text,account_version,authorization_epoch,mls_device_id,expires_at,closed_at FROM owned_chat_connections WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, actor).Scan(&v.id, &v.actor, &v.session, &v.version, &v.epoch, &v.device, &v.expires, &v.closed)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrForbidden
	}
	if e != nil {
		return v, e
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return v, e
	}
	if v.session != p.Session.ID || v.version != p.Account.Version || v.epoch != p.Session.AuthorizationEpoch || (!allowClosed && (v.closed != nil || !v.expires.After(now))) {
		return v, rooms.ErrForbidden
	}
	if v.device != rooms.MLSWriteFrom(ctx).Device {
		return v, rooms.ErrForbidden
	}
	if v.device != "" {
		if !o.r.mls {
			return v, rooms.ErrForbidden
		}
		if e = mlsDevice(ctx, tx, actor, v.device); e != nil {
			return v, e
		}
	}
	return v, nil
}
func (o *Organization) connectionAction(ctx context.Context, actor, id, op string) (string, error) {
	if e := o.valid(actor); e != nil {
		return "", e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, "", "", "")
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	v, e := o.ownedConnection(ctx, tx, actor, id, op == "close")
	if e != nil {
		return "", e
	}
	switch op {
	case "renew":
		_, e = tx.ExecContext(ctx, `UPDATE owned_chat_connections SET expires_at=clock_timestamp()+interval '60 seconds' WHERE id=$1`, id)
	case "close":
		_, e = tx.ExecContext(ctx, `UPDATE owned_chat_connections SET closed_at=coalesce(closed_at,clock_timestamp()) WHERE id=$1`, id)
	}
	if e != nil {
		return "", e
	}
	if e = o.r.commit(ctx, tx, actor, ""); e != nil {
		return "", e
	}
	return v.device, nil
}
func (o *Organization) RenewConnection(ctx context.Context, actor, id string) error {
	_, e := o.connectionAction(ctx, actor, id, "renew")
	return e
}
func (o *Organization) CloseConnection(ctx context.Context, actor, id string) error {
	_, e := o.connectionAction(ctx, actor, id, "close")
	return e
}
func (o *Organization) CurrentConnectionDevice(ctx context.Context, actor, id string) (string, error) {
	return o.connectionAction(ctx, actor, id, "read")
}
func (o *Organization) SetSubscription(ctx context.Context, actor, connection, rid string, subscribed bool) error {
	if e := o.valid(actor); e != nil {
		return e
	}
	if e := valid(connection, rid); e != nil {
		return e
	}
	tx, e := o.r.beginRoomWrite(ctx, actor, rid, "", "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = o.ownedConnection(ctx, tx, actor, connection, false); e != nil {
		return e
	}
	room, e := o.r.detailRoom(ctx, tx, actor, rid, false)
	if e != nil {
		return e
	}
	member, e := requireMember(ctx, tx, room.ID, actor)
	if e != nil {
		return e
	}
	if subscribed {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_chat_subscriptions(connection_id,member_id,joined_at,expires_at)VALUES($1,$2,$3,clock_timestamp()+interval '5 minutes') ON CONFLICT(connection_id,member_id) DO UPDATE SET joined_at=EXCLUDED.joined_at,expires_at=EXCLUDED.expires_at`, connection, member.ID, member.JoinedAt)
	} else {
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_chat_subscriptions WHERE connection_id=$1 AND member_id=$2`, connection, member.ID)
	}
	if e != nil {
		return e
	}
	return o.r.commit(ctx, tx, actor, "")
}
func (o *Organization) leaseEligible(ctx context.Context, tx *sql.Tx, v *chatLease) (bool, error) {
	g, ok := o.r.guard.(recordedChatAuthentication)
	if !ok {
		return false, rooms.ErrUnavailable
	}
	if e := detailReadLock(ctx, tx, "owned_authority_accounts", v.actor); e != nil {
		return false, e
	}
	ok, e := g.StoredConnectionEligible(ctx, o.r.db, tx, v.actor, v.session, v.version, v.epoch)
	if e != nil || !ok {
		return false, e
	}
	// Preserve time boundaries through later room/member hydration. Canonical
	// account/binding locks already prevent generation and relationship changes.
	var delegated bool
	e = tx.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,expires_at,type,ARRAY[id] AS path FROM owned_authority_sessions WHERE id=$1 AND account_id=$2 UNION ALL SELECT s.id,s.parent_id,s.expires_at,s.type,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$2 WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64) SELECT min(expires_at),bool_or(type=1) FROM chain`, v.session, v.actor).Scan(&v.eligibleUntil, &delegated)
	if e != nil {
		return false, e
	}
	if delegated {
		var until time.Time
		e = tx.QueryRowContext(ctx, `SELECT least(g.expires_at,c.expires_at,secret.expires_at) FROM owned_oauth_grants g JOIN owned_app_consents c ON c.account_id=g.account_id AND c.app_id=g.app_id LEFT JOIN owned_app_secrets secret ON secret.id=g.secret_id WHERE g.session_id=$1`, v.session).Scan(&until)
		if e != nil {
			return false, e
		}
		if until.Before(v.eligibleUntil) {
			v.eligibleUntil = until
		}
	}
	if v.device != "" {
		if !o.r.mls {
			return false, nil
		}
		var live bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_devices WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL)`, v.device, v.actor).Scan(&live)
		return live, e
	}
	return true, nil
}
