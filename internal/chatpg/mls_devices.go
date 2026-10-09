// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func (r *Repository) mlsPublish(ctx context.Context, tx *sql.Tx, actor, device string, p rooms.MLSRequest) (any, error) {
	if p.DeviceID != device || !rooms.ValidMLSOpaque(p.KeyPackage, 16384) {
		return nil, rooms.ErrInvalid
	}
	var count int
	if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_mls_devices WHERE account_id=$1`, actor).Scan(&count); e != nil {
		return nil, e
	}
	var exists bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_devices WHERE id=$1)`, device).Scan(&exists); e != nil {
		return nil, e
	}
	if !exists && count >= 32 {
		return nil, rooms.ErrConflict
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO owned_mls_devices(id,account_id)VALUES($1,$2)ON CONFLICT(id)DO NOTHING`, device, actor); e != nil {
		return nil, e
	}
	if e := mlsDevice(ctx, tx, actor, device); e != nil {
		return nil, e
	}
	digest := sha256.Sum256([]byte(p.KeyPackage))
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_key_packages WHERE device_id=$1 AND digest=$2)`, device, digest[:]).Scan(&exists); e != nil {
		return nil, e
	}
	if exists {
		return map[string]any{"device_id": device, "accepted": true}, nil
	}
	var available int
	if e := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE consumed_at IS NULL AND created_at>clock_timestamp()-interval '30 days') FROM owned_mls_key_packages WHERE device_id=$1`, device).Scan(&count, &available); e != nil {
		return nil, e
	}
	if count >= 4096 || available >= 100 {
		return nil, rooms.ErrConflict
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_mls_key_packages(device_id,digest,package)VALUES($1,$2,$3)`, device, digest[:], p.KeyPackage)
	return map[string]any{"device_id": device, "accepted": true}, e
}
func (r *Repository) mlsShared(ctx context.Context, tx *sql.Tx, actor, target string) error {
	if e := activeOther(ctx, tx, target); e != nil {
		return e
	}
	if target == actor {
		return nil
	}
	var shared bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_members mine JOIN chat_members theirs ON theirs.chat_room_id=mine.chat_room_id JOIN chat_rooms c ON c.id=mine.chat_room_id WHERE mine.account_id=$1 AND theirs.account_id=$2 AND mine.joined_at IS NOT NULL AND mine.leave_at IS NULL AND mine.deleted_at IS NULL AND NOT mine.owned_banned AND theirs.joined_at IS NOT NULL AND theirs.leave_at IS NULL AND theirs.deleted_at IS NULL AND NOT theirs.owned_banned AND c.deleted_at IS NULL AND c.owned_owner_id IS NOT NULL AND `+r.ReadSQL("c.id", "$1")+` AND `+r.ReadSQL("c.id", "$2")+`)`, actor, target).Scan(&shared)
	if e == nil && !shared {
		e = rooms.ErrForbidden
	}
	return e
}
func (r *Repository) mlsClaim(ctx context.Context, tx *sql.Tx, actor, target string) (any, error) {
	account, onlyDevice := target, ""
	if !rooms.ValidID(target) {
		account = actor
		onlyDevice = target
	}
	if e := r.mlsShared(ctx, tx, actor, account); e != nil {
		return nil, e
	}
	// Target account lock serializes claims across replicas, including requests
	// that omit consume=true. The result and consumption commit together.
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_mls_devices WHERE account_id=$1 AND revoked_at IS NULL AND($2='' OR id=$2) ORDER BY id FOR UPDATE`, account, onlyDevice)
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
	out := []map[string]any{}
	for _, d := range devices {
		var packageBytes string
		var digest []byte
		e = tx.QueryRowContext(ctx, `SELECT package,digest FROM owned_mls_key_packages WHERE device_id=$1 AND consumed_at IS NULL AND created_at>clock_timestamp()-interval '30 days' ORDER BY created_at,digest LIMIT 1 FOR UPDATE`, d).Scan(&packageBytes, &digest)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_mls_key_packages SET consumed_at=clock_timestamp(),consumed_by=$3 WHERE device_id=$1 AND digest=$2 AND consumed_at IS NULL`, d, digest, actor); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"device_id": d, "key_package": packageBytes, "key_package_bytes": packageBytes})
	}
	return out, nil
}
func (r *Repository) mlsStatus(ctx context.Context, tx *sql.Tx, actor string) (any, error) {
	rows, e := tx.QueryContext(ctx, `SELECT d.id,count(k.digest) FROM owned_mls_devices d LEFT JOIN owned_mls_key_packages k ON k.device_id=d.id AND k.consumed_at IS NULL AND k.created_at>clock_timestamp()-interval '30 days' WHERE d.account_id=$1 AND d.revoked_at IS NULL GROUP BY d.id ORDER BY d.id`, actor)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	need := []map[string]any{}
	total := 0
	for rows.Next() {
		var d string
		var n int
		if e = rows.Scan(&d, &n); e != nil {
			return nil, e
		}
		total += n
		if n < 3 {
			need = append(need, map[string]any{"device_id": d, "available_count": n})
		}
	}
	return map[string]any{"needs_more_kps": len(need) > 0, "devices_needing_kps": need, "available_count": total}, rows.Err()
}
func (r *Repository) mlsReadyUsers(ctx context.Context, tx *sql.Tx, actor string, ids []string) (any, error) {
	out := []map[string]any{}
	for _, a := range dedup(append([]string{}, ids...)) {
		if e := r.mlsShared(ctx, tx, actor, a); e != nil {
			return nil, e
		}
		var available int
		e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_mls_devices d JOIN owned_mls_key_packages k ON k.device_id=d.id WHERE d.account_id=$1 AND d.revoked_at IS NULL AND k.consumed_at IS NULL AND k.created_at>clock_timestamp()-interval '30 days'`, a).Scan(&available)
		if e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"account_id": a, "is_ready": available > 0, "available_key_packages": available})
	}
	return map[string]any{"users": out}, nil
}
func (r *Repository) mlsRevoke(ctx context.Context, tx *sql.Tx, actor, target string) error {
	if !rooms.ValidMLSIdentifier(target) {
		return rooms.ErrInvalid
	}
	var owner string
	e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_mls_devices WHERE id=$1 FOR UPDATE`, target).Scan(&owner)
	if errors.Is(e, sql.ErrNoRows) || e == nil && owner != actor {
		return rooms.ErrForbidden
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_mls_devices SET revoked_at=coalesce(revoked_at,clock_timestamp())WHERE id=$1`, target)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `DELETE FROM owned_mls_envelopes WHERE device_id=$1;`, target)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_mls_memberships SET reshare_required=true WHERE group_id IN(SELECT group_id FROM owned_mls_memberships WHERE device_id=$1)`, target)
	return e
}
