// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

// Called after the canonical room lock, before message or sync-event mutation.
// The ordinary caller has no device header: account authority plus an active
// registered membership is the accepted boundary. Optional device selectors
// narrow it but never prove possession of the device's cryptographic keys.
func (r *Repository) mlsMessageCheck(ctx context.Context, tx *sql.Tx, actor, rid string, p *rooms.EncryptionMeta, deleting bool) error {
	var mode int32
	if e := tx.QueryRowContext(ctx, `SELECT encryption_mode FROM chat_rooms WHERE id=$1`, rid).Scan(&mode); e != nil {
		return e
	}
	if mode == r.policy.NoneEncryptionMode {
		if p != nil {
			return rooms.ErrInvalid
		}
		return nil
	}
	if !r.mls || mode != rooms.MLSEncryptionMode {
		return rooms.ErrUnsupported
	}
	if deleting && p == nil {
		return rooms.ErrConflict
	}
	if p == nil {
		return rooms.ErrInvalid
	}
	s, e := mlsState(ctx, tx, rid)
	if e != nil {
		return e
	}
	proof := rooms.MLSWriteFrom(ctx)
	if e = mlsGeneration(rooms.MLSRequest{Generation: proof.Generation}, s); e != nil {
		return e
	}
	if p != nil {
		if e = rooms.ValidateEncryption(p); e != nil {
			return e
		}
		if p.StateVersion != nil && *p.StateVersion != s.StateVersion {
			return rooms.ErrConflict
		}
		if p.Epoch != s.Epoch {
			return rooms.ErrConflict
		}
	}
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_memberships membership JOIN owned_mls_devices d ON d.id=membership.device_id JOIN chat_members m ON m.id=membership.member_id AND m.joined_at=membership.membership_since WHERE membership.group_id=$1 AND membership.state='active' AND d.account_id=$2 AND d.revoked_at IS NULL AND($3='' OR d.id=$3) AND m.account_id=$2 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned)`, s.GroupID, actor, proof.Device).Scan(&active)
	if e == nil && !active {
		e = rooms.ErrForbidden
	}
	return e
}
func setMessageEncryption(ctx context.Context, tx *sql.Tx, v *messager.MessageProjection, p *rooms.EncryptionMeta) error {
	if p == nil {
		return nil
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE chat_messages SET encryption_meta=$2::jsonb,content=NULL WHERE id=$1`, v.ID, raw)
	if e == nil {
		v.EncryptionMeta = raw
		v.Content = nil
	}
	return e
}

func normalizedMLSEncryption(ctx context.Context, p *rooms.EncryptionMeta) *rooms.EncryptionMeta {
	if p == nil {
		return nil
	}
	copy := *p
	generation := int64(1)
	if proof := rooms.MLSWriteFrom(ctx); proof.Generation != nil {
		generation = *proof.Generation
	}
	copy.StateVersion = &generation
	return &copy
}
