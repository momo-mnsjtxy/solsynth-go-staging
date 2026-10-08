// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
)

type mlsRecipient struct {
	device  string
	member  rooms.Member
	payload rooms.MLSPayload
}

func mlsApplication(raw []byte) (rooms.EncryptionMeta, string, error) {
	var p struct {
		Type        string                     `json:"type"`
		Encryption  *rooms.EncryptionMeta      `json:"encryption_meta"`
		ClientID    string                     `json:"client_message_id"`
		Attachments []string                   `json:"attachments_id"`
		Meta        map[string]json.RawMessage `json:"meta"`
		Replied     *string                    `json:"replied_message_id"`
		Forwarded   *string                    `json:"forwarded_message_id"`
		Survey      *string                    `json:"survey_id"`
		Fund        *string                    `json:"fund_id"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || p.Type != "text" || rooms.ValidateEncryption(p.Encryption) != nil || !rooms.ValidMLSClientID(p.ClientID) {
		return rooms.EncryptionMeta{}, "", rooms.ErrInvalid
	}
	if p.Forwarded != nil || p.Survey != nil || p.Fund != nil {
		return rooms.EncryptionMeta{}, "", rooms.ErrUnsupported
	}
	if len(p.Attachments) > 32 {
		return rooms.EncryptionMeta{}, "", rooms.ErrInvalid
	}
	for _, id := range p.Attachments {
		if !rooms.ValidID(id) {
			return rooms.EncryptionMeta{}, "", rooms.ErrInvalid
		}
	}
	if p.Replied != nil && !rooms.ValidID(*p.Replied) {
		return rooms.EncryptionMeta{}, "", rooms.ErrInvalid
	}
	rawMeta, _ := json.Marshal(p.Meta)
	if e := rooms.ValidateMLSReferenceMeta(rawMeta); e != nil {
		return rooms.EncryptionMeta{}, "", e
	}
	return *p.Encryption, p.ClientID, nil
}
func (r *Repository) mlsFanout(ctx context.Context, tx *sql.Tx, actor, device string, m rooms.Member, s rooms.MLSState, state string, p rooms.MLSRequest, op string) (any, error) {
	kind, epoch := rooms.MLSApplication, s.Epoch
	client := p.ClientMessageID
	common := rooms.MLSPayload{Ciphertext: p.Ciphertext, Header: p.Header, ClientMessageID: client}
	switch op {
	case "welcome":
		kind = rooms.MLSWelcome
		if !rooms.ValidID(p.RecipientAccountID) || len(p.Payloads) == 0 || len(p.Payloads) > 32 {
			return nil, rooms.ErrInvalid
		}
		p.Payloads = append([]rooms.MLSPayload{}, p.Payloads...)
		sort.Slice(p.Payloads, func(i, j int) bool { return p.Payloads[i].RecipientDeviceID < p.Payloads[j].RecipientDeviceID })
		client = p.Payloads[0].ClientMessageID
		for i, v := range p.Payloads {
			if !rooms.ValidMLSIdentifier(v.RecipientDeviceID) || i > 0 && v.RecipientDeviceID == p.Payloads[i-1].RecipientDeviceID || v.ClientMessageID != client || !rooms.ValidMLSOpaque(v.Ciphertext, 65536) || !rooms.ValidMLSOpaque(v.Header, 4096) || len(v.Meta) > 1 || len(v.Meta) == 1 && v.Meta["invited_by"] != actor {
				return nil, rooms.ErrInvalid
			}
		}
	case "commit-fanout":
		kind = rooms.MLSCommit
		if p.Epoch == nil || !rooms.ValidMLSOpaque(p.Ciphertext, 65536) || !rooms.ValidMLSOpaque(p.Header, 4096) {
			return nil, rooms.ErrInvalid
		}
		epoch = *p.Epoch
		for k, v := range p.Meta {
			if k != "reason" && k != "client_message_id" || len(v) > 256 || k == "client_message_id" && v != client {
				return nil, rooms.ErrInvalid
			}
		}
	case "application":
		meta, key, e := mlsApplication(p.Payload)
		if e != nil {
			return nil, e
		}
		client = key
		if meta.StateVersion != nil && *meta.StateVersion != s.StateVersion {
			return nil, rooms.ErrConflict
		}
		epoch = meta.Epoch
		common = rooms.MLSPayload{Ciphertext: meta.Ciphertext, Header: meta.Header, ClientMessageID: key}
	default:
		return nil, rooms.ErrUnsupported
	}
	if !rooms.ValidMLSClientID(client) {
		return nil, rooms.ErrInvalid
	}
	raw, _ := json.Marshal(p)
	digest := sha256.Sum256(raw)
	var stored []byte
	var priorEpoch int64
	var delivered int
	e := tx.QueryRowContext(ctx, `SELECT digest,epoch,delivered FROM owned_mls_requests WHERE group_id=$1 AND state_version=$2 AND sender_device=$3 AND operation=$4 AND client_id=$5`, s.GroupID, s.StateVersion, device, op, client).Scan(&stored, &priorEpoch, &delivered)
	if e == nil {
		if !bytes.Equal(stored, digest[:]) {
			return nil, rooms.ErrConflict
		}
		return map[string]any{"epoch": priorEpoch, "state_version": s.StateVersion, "delivered": delivered}, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if kind == rooms.MLSCommit {
		if s.Epoch >= rooms.MLSMaxEpoch || epoch != s.Epoch+1 {
			return nil, rooms.ErrConflict
		}
	} else if epoch != s.Epoch {
		return nil, rooms.ErrConflict
	}
	var requests int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_mls_requests WHERE group_id=$1`, s.GroupID).Scan(&requests); e != nil {
		return nil, e
	}
	if requests >= 100000 {
		return nil, rooms.ErrConflict
	}
	recipients := []mlsRecipient{}
	if kind == rooms.MLSWelcome {
		target, e := r.mlsMember(ctx, tx, p.RecipientAccountID, s.RoomID, false)
		if e != nil {
			return nil, e
		}
		for _, payload := range p.Payloads {
			if e = mlsDevice(ctx, tx, p.RecipientAccountID, payload.RecipientDeviceID); e != nil {
				return nil, e
			}
			if payload.RecipientDeviceID == device {
				return nil, rooms.ErrInvalid
			}
			recipients = append(recipients, mlsRecipient{payload.RecipientDeviceID, target, payload})
		}
	} else {
		rows, e := tx.QueryContext(ctx, `SELECT d.id,d.account_id::text FROM owned_mls_memberships membership JOIN owned_mls_devices d ON d.id=membership.device_id JOIN chat_members m ON m.id=membership.member_id AND m.joined_at=membership.membership_since WHERE membership.group_id=$1 AND membership.state='active' AND d.revoked_at IS NULL AND d.id<>$2 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned ORDER BY d.id LIMIT 257`, s.GroupID, device)
		if e != nil {
			return nil, e
		}
		targets := [][2]string{}
		for rows.Next() {
			var d, a string
			if e = rows.Scan(&d, &a); e != nil {
				rows.Close()
				return nil, e
			}
			targets = append(targets, [2]string{d, a})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if len(targets) > 256 {
			return nil, rooms.ErrUnsupported
		}
		for _, v := range targets {
			target, e := r.mlsMember(ctx, tx, v[1], s.RoomID, false)
			if errors.Is(e, rooms.ErrForbidden) {
				continue
			}
			if e != nil {
				return nil, e
			}
			if e = mlsDevice(ctx, tx, v[1], v[0]); e != nil {
				return nil, e
			}
			recipients = append(recipients, mlsRecipient{v[0], target, common})
		}
	}
	for _, target := range recipients {
		// A bounded durable queue. Cleanup removes delivered/expired ciphertext,
		// while request digests remain to reject changed retries and duplicate sends.
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_mls_envelopes WHERE device_id=$1 AND(acked_at IS NOT NULL OR created_at<=clock_timestamp()-interval '7 days')`, target.device); e != nil {
			return nil, e
		}
		var queued int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_mls_envelopes WHERE device_id=$1`, target.device).Scan(&queued); e != nil {
			return nil, e
		}
		if queued >= 2048 {
			return nil, rooms.ErrConflict
		}
		var seq int64
		e = tx.QueryRowContext(ctx, `UPDATE owned_mls_devices SET next_sequence=next_sequence+1 WHERE id=$1 AND next_sequence<9223372036854775807 RETURNING next_sequence`, target.device).Scan(&seq)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, rooms.ErrConflict
		}
		if e != nil {
			return nil, e
		}
		eid, e := id()
		if e != nil {
			return nil, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_mls_envelopes(id,group_id,device_id,member_id,membership_since,sequence,state_version,type,epoch,ciphertext,header)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, eid, s.GroupID, target.device, target.member.ID, target.member.JoinedAt, seq, s.StateVersion, kind, epoch, target.payload.Ciphertext, target.payload.Header)
		if e != nil {
			return nil, e
		}
		if kind == rooms.MLSWelcome {
			if e = mlsGrant(ctx, tx, s, target.device, target.member, "welcomed", epoch); e != nil {
				return nil, e
			}
		}
	}
	if kind == rooms.MLSCommit {
		_, e = tx.ExecContext(ctx, `UPDATE owned_mls_groups SET epoch=$2,group_info=NULL,ratchet_tree=NULL,info_epoch=NULL,updated_at=clock_timestamp()WHERE id=$1`, s.GroupID, epoch)
		if e != nil {
			return nil, e
		}
		if e = mlsGrant(ctx, tx, s, device, m, "active", epoch); e != nil {
			return nil, e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_mls_requests(group_id,state_version,sender_device,operation,client_id,digest,epoch,delivered)VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.GroupID, s.StateVersion, device, op, client, digest[:], epoch, len(recipients))
	if e != nil {
		return nil, e
	}
	return map[string]any{"epoch": epoch, "state_version": s.StateVersion, "delivered": len(recipients)}, nil
}
func (r *Repository) mlsQueue(ctx context.Context, tx *sql.Tx, actor, device, op, target string, p rooms.MLSRequest) (any, error) {
	// Recheck canonical membership incarnation, account state and relation policy
	// at each read/ack. Leaving, blocking, revocation and reset withdraw delivery.
	eligible := ` d.account_id=$1 AND d.id=$2 AND d.revoked_at IS NULL AND c.deleted_at IS NULL AND c.encryption_mode=3 AND m.account_id=$1 AND m.joined_at=e.membership_since AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND e.state_version=g.state_version AND ` + r.ReadSQL("c.id", "$1")
	if op == "ack" {
		if !rooms.ValidID(target) {
			return nil, rooms.ErrInvalid
		}
		var version int64
		e := tx.QueryRowContext(ctx, `SELECT g.state_version FROM owned_mls_envelopes e JOIN owned_mls_devices d ON d.id=e.device_id JOIN owned_mls_groups g ON g.id=e.group_id JOIN chat_rooms c ON c.id=g.room_id JOIN chat_members m ON m.id=e.member_id WHERE e.id=$3 AND `+eligible, actor, device, target).Scan(&version)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, rooms.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		if e = mlsGeneration(p, rooms.MLSState{StateVersion: version}); e != nil {
			return nil, e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_mls_envelopes SET acked_at=coalesce(acked_at,clock_timestamp())WHERE id=$1 AND device_id=$2`, target, device)
		return nil, e
	}
	if op == "recoveries" {
		rows, e := tx.QueryContext(ctx, `SELECT g.id,g.state_version FROM owned_mls_memberships membership JOIN owned_mls_groups g ON g.id=membership.group_id JOIN chat_rooms c ON c.id=g.room_id JOIN chat_members m ON m.id=membership.member_id AND m.joined_at=membership.membership_since WHERE membership.device_id=$2 AND membership.reshare_required AND m.account_id=$1 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND c.deleted_at IS NULL AND `+r.ReadSQL("c.id", "$1")+` ORDER BY g.id LIMIT 100`, actor, device)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var gid string
			var version int64
			if e = rows.Scan(&gid, &version); e != nil {
				return nil, e
			}
			out = append(out, map[string]any{"mls_group_id": gid, "state_version": version})
		}
		return out, rows.Err()
	}
	rows, e := tx.QueryContext(ctx, `SELECT e.id::text,e.type,e.ciphertext,e.group_id,e.header,e.epoch,e.sequence,e.state_version FROM owned_mls_envelopes e JOIN owned_mls_devices d ON d.id=e.device_id JOIN owned_mls_groups g ON g.id=e.group_id JOIN chat_rooms c ON c.id=g.room_id JOIN chat_members m ON m.id=e.member_id WHERE e.acked_at IS NULL AND e.created_at>clock_timestamp()-interval '7 days' AND `+eligible+` ORDER BY e.sequence LIMIT 100`, actor, device)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []rooms.MLSEnvelope{}
	for rows.Next() {
		var v rooms.MLSEnvelope
		if e = rows.Scan(&v.ID, &v.Type, &v.Ciphertext, &v.GroupID, &v.Header, &v.Epoch, &v.Sequence, &v.StateVersion); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
