// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

type chatSubscriptionRow struct {
	lease        chatLease
	room, member string
	expires      time.Time
}

func (o *Organization) subscriptionRows(ctx context.Context, tx *sql.Tx, actor string, ids []string, room string) ([]chatSubscriptionRow, error) {
	rows, e := tx.QueryContext(ctx, `SELECT c.id::text,c.account_id::text,c.session_id::text,c.account_version,c.authorization_epoch,c.mls_device_id,c.expires_at,c.closed_at,m.chat_room_id::text,m.id::text,s.expires_at FROM owned_chat_subscriptions s JOIN owned_chat_connections c ON c.id=s.connection_id JOIN chat_members m ON m.id=s.member_id AND m.account_id=c.account_id WHERE m.chat_room_id=ANY($1::uuid[]) AND m.joined_at=s.joined_at AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND s.expires_at>clock_timestamp() AND (($3::text='' AND c.account_id=$2::uuid) OR m.chat_room_id=NULLIF($3::text,'')::uuid) ORDER BY m.chat_room_id,m.id,c.id LIMIT 1001`, ids, actor, room)
	if e != nil {
		return nil, e
	}
	out := []chatSubscriptionRow{}
	for rows.Next() {
		var v chatSubscriptionRow
		c := &v.lease
		if e = rows.Scan(&c.id, &c.actor, &c.session, &c.version, &c.epoch, &c.device, &c.expires, &c.closed, &v.room, &v.member, &v.expires); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(out) > 1000 {
		return nil, rooms.ErrUnavailable
	}
	live := []chatSubscriptionRow{}
	for _, v := range out {
		ok, e := o.leaseEligible(ctx, tx, &v.lease)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		if v.lease.actor != actor && o.r.social != nil {
			blocked, e := o.r.social.IsBlockedInTransaction(ctx, tx, actor, v.lease.actor)
			if e != nil {
				return nil, e
			}
			if blocked {
				continue
			}
		}
		live = append(live, v)
	}
	// Evaluate expiry after potentially waiting on authority/device checks.
	now, e := timestamp(ctx, tx)
	if e != nil {
		return nil, e
	}
	out = out[:0]
	for _, v := range live {
		if v.expires.After(now) && v.lease.eligibleUntil.After(now) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (o *Organization) RoomSubscriptions(ctx context.Context, actor, rid string) ([]rooms.RoomSubscription, error) {
	if e := valid(actor, rid); e != nil {
		return nil, e
	}
	tx, ids, e := o.beginAll(ctx, actor, "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if !hasID(ids, rid) {
		return nil, rooms.ErrForbidden
	}
	rows, e := o.subscriptionRows(ctx, tx, actor, ids, rid)
	if e != nil {
		return nil, e
	}
	out := []rooms.RoomSubscription{}
	index := map[string]int{}
	for _, v := range rows {
		i, ok := index[v.member]
		if !ok {
			m, e := requireMember(ctx, tx, rid, v.lease.actor)
			if e != nil {
				return nil, e
			}
			m.Account, e = canonicalAccount(ctx, tx, m.AccountID)
			if e != nil {
				return nil, e
			}
			if m.AccountID != actor {
				m.LastReadAt = nil
			}
			i = len(out)
			index[v.member] = i
			out = append(out, rooms.RoomSubscription{RoomID: rid, MemberID: v.member, AccountID: m.AccountID, Member: m, Devices: []rooms.SubscriptionDevice{}})
		}
		if v.lease.actor == actor {
			out[i].Devices = append(out[i].Devices, rooms.SubscriptionDevice{DeviceToken: v.lease.id, ExpiresAt: v.expires})
		}
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return nil, e
	}
	liveMembers, liveDevices := map[string]bool{}, map[string]bool{}
	for _, v := range rows {
		if v.expires.After(now) && v.lease.eligibleUntil.After(now) {
			liveMembers[v.member] = true
			liveDevices[v.lease.id] = true
		}
	}
	filtered := []rooms.RoomSubscription{}
	for _, v := range out {
		if !liveMembers[v.MemberID] {
			continue
		}
		devices := []rooms.SubscriptionDevice{}
		for _, d := range v.Devices {
			if liveDevices[d.DeviceToken] {
				devices = append(devices, d)
			}
		}
		v.Devices = devices
		filtered = append(filtered, v)
	}
	out = filtered
	if e = o.commitAll(ctx, tx, actor, "", ids); e != nil {
		return nil, e
	}
	return out, nil
}
func (o *Organization) accountSubscriptions(ctx context.Context, tx *sql.Tx, actor string, ids []string) ([]rooms.AccountSubscription, map[string]chatLease, error) {
	rows, e := o.subscriptionRows(ctx, tx, actor, ids, "")
	if e != nil {
		return nil, nil, e
	}
	out := []rooms.AccountSubscription{}
	index := map[string]int{}
	leases := map[string]chatLease{}
	for _, v := range rows {
		i, ok := index[v.room]
		if !ok {
			room, e := o.r.detailRoom(ctx, tx, actor, v.room, false)
			if e != nil {
				return nil, nil, e
			}
			if e = o.r.decorateRoom(ctx, tx, &room, actor); e != nil {
				return nil, nil, e
			}
			i = len(out)
			index[v.room] = i
			out = append(out, rooms.AccountSubscription{RoomID: v.room, MemberID: v.member, Room: room, Devices: []rooms.SubscriptionDevice{}})
		}
		out[i].Devices = append(out[i].Devices, rooms.SubscriptionDevice{DeviceToken: v.lease.id, ExpiresAt: v.expires})
		leases[v.lease.id] = v.lease
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return nil, nil, e
	}
	filtered := []rooms.AccountSubscription{}
	for _, v := range out {
		devices := []rooms.SubscriptionDevice{}
		for _, d := range v.Devices {
			if d.ExpiresAt.After(now) && leases[d.DeviceToken].eligibleUntil.After(now) {
				devices = append(devices, d)
			}
		}
		v.Devices = devices
		if len(devices) > 0 {
			filtered = append(filtered, v)
		}
	}
	return filtered, leases, nil
}
func (o *Organization) AccountSubscriptions(ctx context.Context, actor string) ([]rooms.AccountSubscription, error) {
	tx, ids, e := o.beginAll(ctx, actor, "")
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, _, e := o.accountSubscriptions(ctx, tx, actor, ids)
	if e != nil {
		return nil, e
	}
	if e = o.commitAll(ctx, tx, actor, "", ids); e != nil {
		return nil, e
	}
	return out, nil
}
func (o *Organization) ChatStatus(ctx context.Context, actor string) (rooms.ChatAccountStatus, error) {
	out := rooms.ChatAccountStatus{AccountID: actor, PushNotificationsMaySendForUnsubscribedRooms: true, Subscriptions: []rooms.SubscriptionRoomStatus{}}
	tx, ids, e := o.beginAll(ctx, actor, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	subscriptions, leases, e := o.accountSubscriptions(ctx, tx, actor, ids)
	if e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,account_id::text,session_id::text,account_version,authorization_epoch,mls_device_id,expires_at,closed_at FROM owned_chat_connections WHERE account_id=$1 AND expires_at>clock_timestamp() AND closed_at IS NULL ORDER BY id LIMIT 101`, actor)
	if e != nil {
		return out, e
	}
	candidates := []chatLease{}
	for rows.Next() {
		var v chatLease
		if e = rows.Scan(&v.id, &v.actor, &v.session, &v.version, &v.epoch, &v.device, &v.expires, &v.closed); e != nil {
			rows.Close()
			return out, e
		}
		candidates = append(candidates, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(candidates) > 100 {
		return out, rooms.ErrUnavailable
	}
	connected := map[string]chatLease{}
	for _, v := range candidates {
		ok, e := o.leaseEligible(ctx, tx, &v)
		if e != nil {
			return out, e
		}
		if ok {
			connected[v.id] = v
		}
	}
	now, e := timestamp(ctx, tx)
	if e != nil {
		return out, e
	}
	for _, v := range connected {
		if v.expires.After(now) && v.eligibleUntil.After(now) {
			out.HasAnyWebSocketConnection = true
		}
	}
	for _, s := range subscriptions {
		v := rooms.SubscriptionRoomStatus{RoomID: s.RoomID, MemberID: s.MemberID, Room: s.Room, Devices: []rooms.SubscriptionDeviceStatus{}}
		for _, d := range s.Devices {
			if !d.ExpiresAt.After(now) || !leases[d.DeviceToken].eligibleUntil.After(now) {
				continue
			}
			c := leases[d.DeviceToken]
			_, eligible := connected[d.DeviceToken]
			v.Devices = append(v.Devices, rooms.SubscriptionDeviceStatus{SubscriptionDevice: d, IsWebSocketConnected: eligible && c.closed == nil && c.expires.After(now) && c.eligibleUntil.After(now)})
		}
		if len(v.Devices) > 0 {
			v.IsSubscribed = true
			v.PushNotificationsSuppressed = true
			out.Subscriptions = append(out.Subscriptions, v)
		}
	}
	out.HasActiveSubscriptions = len(out.Subscriptions) > 0
	if e = o.commitAll(ctx, tx, actor, "", ids); e != nil {
		return rooms.ChatAccountStatus{}, e
	}
	return out, nil
}
