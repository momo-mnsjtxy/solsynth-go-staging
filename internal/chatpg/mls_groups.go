// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func (r *Repository) mlsEnable(ctx context.Context, tx *sql.Tx, actor, rid string, p rooms.MLSRequest) (any, error) {
	v, e := scanRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL AND owned_owner_id IS NOT NULL`, rid))
	if e != nil {
		return nil, e
	}
	if v.EncryptionMode != r.policy.NoneEncryptionMode && v.EncryptionMode != rooms.MLSEncryptionMode {
		return nil, rooms.ErrUnsupported
	}
	if _, e = r.mlsMember(ctx, tx, actor, rid, false); e != nil {
		return nil, e
	}
	if v.Type != 1 {
		if e = r.mlsManage(ctx, tx, v, actor); e != nil {
			return nil, e
		}
	}
	gid := p.GroupID
	if gid == "" {
		gid = "room:" + rid
	}
	if !rooms.ValidMLSIdentifier(gid) {
		return nil, rooms.ErrInvalid
	}
	policy := p.Policy
	if len(policy) == 0 {
		policy = []byte(`{}`)
	}
	var object map[string]any
	if len(policy) > 4096 || json.Unmarshal(policy, &object) != nil || object == nil {
		return nil, rooms.ErrInvalid
	}
	for k, value := range object {
		if k != "ciphersuite" || value != "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519" {
			return nil, rooms.ErrUnsupported
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_mls_groups(id,room_id,policy)VALUES($1,$2,$3::jsonb)ON CONFLICT DO NOTHING`, gid, rid, policy)
	if e != nil {
		return nil, e
	}
	var stored string
	e = tx.QueryRowContext(ctx, `SELECT id FROM owned_mls_groups WHERE room_id=$1`, rid).Scan(&stored)
	if errors.Is(e, sql.ErrNoRows) || e == nil && stored != gid {
		return nil, rooms.ErrConflict
	}
	if e != nil {
		return nil, e
	}
	state, x := mlsState(ctx, tx, rid)
	if x != nil {
		return nil, x
	}
	if x = mlsGeneration(p, state); x != nil {
		return nil, x
	}
	e = tx.QueryRowContext(ctx, `UPDATE chat_rooms SET encryption_mode=$2,owned_updated_at=clock_timestamp() WHERE id=$1 RETURNING owned_updated_at`, rid, rooms.MLSEncryptionMode).Scan(&v.UpdatedAt)
	if e != nil {
		return nil, e
	}
	v.EncryptionMode = rooms.MLSEncryptionMode
	v.MLSGroupID = &gid
	if e = tx.QueryRowContext(ctx, `SELECT epoch,state_version FROM owned_mls_groups WHERE id=$1`, gid).Scan(&v.MLSEpoch, &v.MLSStateVersion); e != nil {
		return nil, e
	}
	return v, nil
}
func mlsState(ctx context.Context, tx *sql.Tx, rid string) (s rooms.MLSState, e error) {
	var bootstrap sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT id,room_id::text,epoch,state_version,bootstrap_device FROM owned_mls_groups WHERE room_id=$1 FOR UPDATE`, rid).Scan(&s.GroupID, &s.RoomID, &s.Epoch, &s.StateVersion, &bootstrap)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrNotFound
	}
	s.Meta = map[string]string{}
	if bootstrap.Valid {
		s.Meta["bootstrap_device_id"] = bootstrap.String
	}
	return
}
func mlsGeneration(p rooms.MLSRequest, s rooms.MLSState) error {
	if p.Generation == nil {
		if s.StateVersion > 1 {
			return rooms.ErrConflict
		}
		return nil
	}
	if *p.Generation != s.StateVersion {
		return rooms.ErrConflict
	}
	return nil
}
func mlsMembership(ctx context.Context, tx *sql.Tx, s rooms.MLSState, device string, m rooms.Member) (state string, epoch int64, e error) {
	e = tx.QueryRowContext(ctx, `SELECT state,epoch FROM owned_mls_memberships WHERE group_id=$1 AND device_id=$2 AND member_id=$3 AND membership_since=$4 AND(expires_at IS NULL OR expires_at>clock_timestamp())`, s.GroupID, device, m.ID, m.JoinedAt).Scan(&state, &epoch)
	if errors.Is(e, sql.ErrNoRows) {
		e = rooms.ErrForbidden
	}
	return
}
func mlsGrant(ctx context.Context, tx *sql.Tx, s rooms.MLSState, device string, m rooms.Member, state string, epoch int64) error {
	// Joining grants are ten minutes; Welcome grants last the delivery retention
	// window. Membership incarnation prevents leave/rejoin from reviving access.
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_mls_memberships(group_id,device_id,member_id,membership_since,state,epoch,expires_at)VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $5='joining' THEN clock_timestamp()+interval '10 minutes' WHEN $5='welcomed' THEN clock_timestamp()+interval '7 days' ELSE NULL END)ON CONFLICT(group_id,device_id)DO UPDATE SET member_id=EXCLUDED.member_id,membership_since=EXCLUDED.membership_since,state=EXCLUDED.state,epoch=EXCLUDED.epoch,expires_at=EXCLUDED.expires_at`, s.GroupID, device, m.ID, m.JoinedAt, state, epoch)
	return e
}
func (r *Repository) mlsGroupOperation(ctx context.Context, tx *sql.Tx, actor, device, rid, op, target string, p rooms.MLSRequest) (any, error) {
	s, e := mlsState(ctx, tx, rid)
	if e != nil {
		return nil, e
	}
	var mode int32
	e = tx.QueryRowContext(ctx, `SELECT encryption_mode FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL`, rid).Scan(&mode)
	if e != nil {
		return nil, e
	}
	if mode != rooms.MLSEncryptionMode {
		return nil, rooms.ErrConflict
	}
	send := op == "welcome" || op == "commit-fanout" || op == "application"
	m, e := r.mlsMember(ctx, tx, actor, rid, send)
	if e != nil {
		return nil, e
	}
	if op != "groupinfo" {
		if e = mlsGeneration(p, s); e != nil {
			return nil, e
		}
	}
	if op == "bootstrap" {
		if p.Epoch == nil || *p.Epoch != 0 || p.StateVersion == nil || *p.StateVersion != s.StateVersion || p.Meta["bootstrap_device_id"] != device || len(p.Meta) != 1 {
			return nil, rooms.ErrInvalid
		}
		if s.Meta["bootstrap_device_id"] == "" {
			_, e = tx.ExecContext(ctx, `UPDATE owned_mls_groups SET bootstrap_device=$2 WHERE id=$1 AND bootstrap_device IS NULL`, s.GroupID, device)
			if e != nil {
				return nil, e
			}
			s.Meta["bootstrap_device_id"] = device
			if e = mlsGrant(ctx, tx, s, device, m, "active", 0); e != nil {
				return nil, e
			}
		}
		return s, nil
	}
	if op == "groupinfo" {
		var info, tree sql.NullString
		var epoch sql.NullInt64
		e = tx.QueryRowContext(ctx, `SELECT group_info,ratchet_tree,info_epoch FROM owned_mls_groups WHERE id=$1`, s.GroupID).Scan(&info, &tree, &epoch)
		if e != nil {
			return nil, e
		}
		if !info.Valid || !tree.Valid || !epoch.Valid || epoch.Int64 != s.Epoch {
			return nil, rooms.ErrNotFound
		}
		state, _, x := mlsMembership(ctx, tx, s, device, m)
		if x != nil && !errors.Is(x, rooms.ErrForbidden) {
			return nil, x
		}
		if state != "active" {
			if e = mlsGrant(ctx, tx, s, device, m, "joining", s.Epoch); e != nil {
				return nil, e
			}
		}
		return map[string]any{"group_info": info.String, "ratchet_tree": tree.String, "epoch": s.Epoch, "state_version": s.StateVersion}, nil
	}
	if op == "recover" {
		return r.mlsRecover(ctx, tx, actor, device, m, s, p)
	}
	state, memberEpoch, e := mlsMembership(ctx, tx, s, device, m)
	if e != nil && !(op == "reset" && errors.Is(e, rooms.ErrForbidden)) {
		return nil, e
	}
	if op == "joined" {
		if p.Epoch == nil || *p.Epoch != s.Epoch || memberEpoch != s.Epoch {
			return nil, rooms.ErrConflict
		}
		if state == "joining" {
			return nil, rooms.ErrConflict
		} // Commit acceptance first.
		e = mlsGrant(ctx, tx, s, device, m, "active", s.Epoch)
		return s, e
	}
	if op != "reset" && state != "active" && !(op == "commit-fanout" && state == "joining" && memberEpoch == s.Epoch) {
		return nil, rooms.ErrForbidden
	}
	switch op {
	case "reset":
		if !p.ResetCapable || p.Generation == nil {
			return nil, rooms.ErrUnsupported
		}
		v, x := scanRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE id=$1`, rid))
		if x != nil {
			return nil, x
		}
		if x = r.mlsManage(ctx, tx, v, actor); x != nil {
			return nil, x
		}
		if p.NewEpoch == nil || *p.NewEpoch != 0 || p.StateVersion == nil || *p.StateVersion != s.StateVersion+1 || *p.StateVersion > rooms.MLSMaxEpoch || p.Reason == "" || len(p.Reason) > 256 {
			return nil, rooms.ErrInvalid
		}
		for _, q := range []string{`DELETE FROM owned_mls_memberships WHERE group_id=$1`, `DELETE FROM owned_mls_envelopes WHERE group_id=$1`} {
			if _, e = tx.ExecContext(ctx, q, s.GroupID); e != nil {
				return nil, e
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_mls_groups SET epoch=0,state_version=$2,bootstrap_device=NULL,group_info=NULL,ratchet_tree=NULL,info_epoch=NULL,updated_at=clock_timestamp()WHERE id=$1`, s.GroupID, *p.StateVersion)
		s.Epoch = 0
		s.StateVersion = *p.StateVersion
		s.Meta = map[string]string{}
		return s, e
	case "groupinfo-put":
		if p.Epoch == nil || *p.Epoch != s.Epoch || !rooms.ValidMLSOpaque(p.GroupInfo, 131072) || !rooms.ValidMLSOpaque(p.RatchetTree, 131072) {
			return nil, rooms.ErrInvalid
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_mls_groups SET group_info=$2,ratchet_tree=$3,info_epoch=epoch,updated_at=clock_timestamp()WHERE id=$1`, s.GroupID, p.GroupInfo, p.RatchetTree)
		return nil, e
	case "commit":
		// A report cannot advance a delivery epoch without an accepted envelope.
		if p.Epoch == nil || *p.Epoch != s.Epoch || p.Reason != "client_commit" {
			return nil, rooms.ErrConflict
		}
		return s, nil
	case "welcome", "commit-fanout", "application":
		return r.mlsFanout(ctx, tx, actor, device, m, s, state, p, op)
	case "recovered":
		_, e = tx.ExecContext(ctx, `UPDATE owned_mls_memberships SET reshare_required=false WHERE group_id=$1 AND device_id=$2`, s.GroupID, device)
		return nil, e
	default:
		return nil, rooms.ErrUnsupported
	}
}
func (r *Repository) mlsRecover(ctx context.Context, tx *sql.Tx, actor, device string, m rooms.Member, s rooms.MLSState, p rooms.MLSRequest) (any, error) {
	if p.TargetAccountID == "" {
		if p.ChatRoomID != "" || p.TargetGroupID != "" || p.TargetDeviceID != "" || p.Epoch != nil || p.Reason != "" {
			return nil, rooms.ErrInvalid
		}
		if _, _, e := mlsMembership(ctx, tx, s, device, m); e != nil {
			if e = mlsGrant(ctx, tx, s, device, m, "joining", s.Epoch); e != nil {
				return nil, e
			}
		}
		_, e := tx.ExecContext(ctx, `UPDATE owned_mls_memberships SET reshare_required=true WHERE group_id=$1 AND device_id=$2`, s.GroupID, device)
		return nil, e
	}
	if p.ChatRoomID != s.RoomID || p.TargetGroupID != s.GroupID || p.TargetDeviceID != "all" || p.Epoch == nil || *p.Epoch != 0 || s.Epoch != 0 || p.Reason != "room_upgrade_to_mls" || !rooms.ValidID(p.TargetAccountID) {
		return nil, rooms.ErrInvalid
	}
	v, e := scanRoom(tx.QueryRowContext(ctx, `SELECT `+roomColumns+` FROM chat_rooms WHERE id=$1`, s.RoomID))
	if e != nil {
		return nil, e
	}
	if e = r.mlsManage(ctx, tx, v, actor); e != nil {
		return nil, e
	}
	target, e := r.mlsMember(ctx, tx, p.TargetAccountID, s.RoomID, false)
	if e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_mls_devices WHERE account_id=$1 AND revoked_at IS NULL ORDER BY id`, p.TargetAccountID)
	if e != nil {
		return nil, e
	}
	devices := []string{}
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			rows.Close()
			return nil, e
		}
		devices = append(devices, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, d := range devices {
		if e = mlsGrant(ctx, tx, s, d, target, "joining", s.Epoch); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_mls_memberships SET reshare_required=true WHERE group_id=$1 AND device_id=$2`, s.GroupID, d); e != nil {
			return nil, e
		}
	}
	return nil, nil
}

func (r *Repository) mlsManage(ctx context.Context, tx *sql.Tx, v rooms.Room, actor string) error {
	if e := r.decorateRoom(ctx, tx, &v, actor); e != nil {
		return e
	}
	return r.manage(ctx, tx, v, actor)
}
