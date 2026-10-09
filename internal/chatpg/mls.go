// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
)

//go:embed mls_schema.sql
var MLSSchema string

// WithMLSDelivery is explicitly opt-in. It enables opaque authenticated delivery,
// not MLS cryptographic validation. Install MLSSchema before enabling it.
func (r *Repository) WithMLSDelivery() *Repository {
	r.mls = r.policy.NoneEncryptionMode != rooms.MLSEncryptionMode
	return r
}
func (r *Repository) MLSReady(ctx context.Context) error {
	if r == nil || !r.mls {
		return nil
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,room_id,epoch FROM owned_mls_groups LIMIT 0;SELECT id,account_id,revoked_at FROM owned_mls_devices LIMIT 0;SELECT group_id,device_id,membership_since FROM owned_mls_memberships LIMIT 0;SELECT device_id,sequence,acked_at FROM owned_mls_envelopes LIMIT 0;SELECT device_id,digest,consumed_at,consumed_by FROM owned_mls_key_packages LIMIT 0;SELECT group_id,client_id,digest FROM owned_mls_requests LIMIT 0`)
	return e
}

var _ rooms.MLSRepository = (*Repository)(nil)

func mlsPermission(op string) string {
	switch op {
	case "enable", "reset":
		return "chat.update"
	case "application", "commit-fanout", "welcome":
		return "chat.messages.create"
	}
	return ""
}
func (r *Repository) mlsLookup(ctx context.Context, actor, op, target string, p rooms.MLSRequest) (rid string, extras []string, e error) {
	tx, e := r.beginRoomWrite(ctx, actor, "", mlsPermission(op), "")
	if e != nil {
		return "", nil, e
	}
	defer tx.Rollback()
	switch op {
	case "enable", "joined":
		rid = target
	case "bootstrap", "commit", "groupinfo", "groupinfo-put", "welcome", "commit-fanout", "recover", "recovered", "reset", "application":
		if op == "application" {
			target = p.RoomID
		}
		if !rooms.ValidMLSIdentifier(target) {
			return "", nil, rooms.ErrInvalid
		}
		e = tx.QueryRowContext(ctx, `SELECT room_id::text FROM owned_mls_groups WHERE id=$1`, target).Scan(&rid)
		if errors.Is(e, sql.ErrNoRows) {
			e = rooms.ErrNotFound
		}
		if e != nil {
			return "", nil, e
		}
	case "claim":
		if rooms.ValidID(target) {
			extras = []string{target}
		} else {
			// Bounded recovery compatibility: only an owned device selector may stand
			// in for this route's account ID. No cross-account device enumeration.
			var owner string
			e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_mls_devices WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL`, target, actor).Scan(&owner)
			if e != nil {
				return "", nil, rooms.ErrForbidden
			}
			extras = []string{owner}
		}
	case "ready":
		extras = append([]string{}, p.AccountIDs...)
	}
	if rid != "" {
		if !rooms.ValidID(rid) {
			return "", nil, rooms.ErrInvalid
		}
		rows, x := tx.QueryContext(ctx, `SELECT account_id::text FROM chat_members WHERE chat_room_id=$1 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned ORDER BY account_id LIMIT 257`, rid)
		if x != nil {
			return "", nil, x
		}
		for rows.Next() {
			var id string
			if x = rows.Scan(&id); x != nil {
				rows.Close()
				return "", nil, x
			}
			extras = append(extras, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", nil, e
		}
		if len(extras) > 256 {
			return "", nil, rooms.ErrUnsupported
		}
	}
	return rid, dedup(extras), nil
}

func (r *Repository) MLS(ctx context.Context, actor, device, op, target string, p rooms.MLSRequest) (out any, e error) {
	if r == nil || !r.mls {
		return nil, rooms.ErrUnsupported
	}
	if !rooms.ValidID(actor) || op != "enable" && !rooms.ValidMLSIdentifier(device) {
		return nil, rooms.ErrInvalid
	}
	if p.Epoch != nil && (*p.Epoch < 0 || *p.Epoch > rooms.MLSMaxEpoch) {
		return nil, rooms.ErrInvalid
	}
	if op == "ready" {
		if len(p.AccountIDs) == 0 || len(p.AccountIDs) > 100 {
			return nil, rooms.ErrInvalid
		}
		for _, v := range p.AccountIDs {
			if !rooms.ValidID(v) {
				return nil, rooms.ErrInvalid
			}
		}
	}
	rid, extras, e := r.mlsLookup(ctx, actor, op, target, p)
	if e != nil {
		return nil, e
	}
	tx, e := r.beginRoomWrite(ctx, actor, rid, mlsPermission(op), "", extras...)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = activeOther(ctx, tx, actor); e != nil {
		return nil, e
	}
	if op != "publish" && op != "enable" {
		if e = mlsDevice(ctx, tx, actor, device); e != nil {
			return nil, e
		}
	}
	if rid != "" {
		rows, x := tx.QueryContext(ctx, `SELECT account_id::text FROM chat_members WHERE chat_room_id=$1 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned`, rid)
		if x != nil {
			return nil, x
		}
		for rows.Next() {
			var id string
			if x = rows.Scan(&id); x != nil {
				rows.Close()
				return nil, x
			}
			if !hasID(extras, id) {
				rows.Close()
				return nil, rooms.ErrConflict
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	switch op {
	case "publish":
		out, e = r.mlsPublish(ctx, tx, actor, device, p)
	case "claim":
		out, e = r.mlsClaim(ctx, tx, actor, target)
	case "status":
		out, e = r.mlsStatus(ctx, tx, actor)
	case "ready":
		out, e = r.mlsReadyUsers(ctx, tx, actor, p.AccountIDs)
	case "revoke":
		e = r.mlsRevoke(ctx, tx, actor, target)
	case "pending", "ack", "recoveries":
		out, e = r.mlsQueue(ctx, tx, actor, device, op, target, p)
	case "enable":
		out, e = r.mlsEnable(ctx, tx, actor, rid, p)
	default:
		out, e = r.mlsGroupOperation(ctx, tx, actor, device, rid, op, target, p)
	}
	if e != nil {
		return nil, e
	}
	if e = r.commit(ctx, tx, actor, mlsPermission(op)); e != nil {
		return nil, e
	}
	return out, nil
}
func mlsDevice(ctx context.Context, tx *sql.Tx, actor, device string) error {
	var owner string
	var revoked sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT account_id::text,revoked_at FROM owned_mls_devices WHERE id=$1 FOR UPDATE`, device).Scan(&owner, &revoked)
	if errors.Is(e, sql.ErrNoRows) || e == nil && (owner != actor || revoked.Valid) {
		return rooms.ErrForbidden
	}
	return e
}
func (r *Repository) mlsMember(ctx context.Context, tx *sql.Tx, actor, rid string, send bool) (rooms.Member, error) {
	if e := activeOther(ctx, tx, actor); e != nil {
		return rooms.Member{}, e
	}
	m, e := requireMember(ctx, tx, rid, actor)
	if e != nil {
		return m, e
	}
	var allowed bool
	if send {
		e = tx.QueryRowContext(ctx, `SELECT `+r.SendSQL("$1", "$2"), rid, actor).Scan(&allowed)
	} else {
		// The standalone read policy has only the room parameter; the composed
		// policy also binds the actor. Use the canonical adapter for both forms.
		allowed, e = r.CanReadInTransaction(ctx, tx, actor, rid)
	}
	if e == nil && !allowed {
		e = rooms.ErrForbidden
	}
	return m, e
}

func (r *Repository) MLSDeliveryEnabled() bool { return r != nil && r.mls }
