// SPDX-License-Identifier: AGPL-3.0-only
package callpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/calls"
	"time"
)

func (r *Repository) End(ctx context.Context, actor, rid string) error {
	if e := r.configured(); e != nil {
		return e
	}
	tx, _, v, _, e := r.locked(ctx, actor, rid, "")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor != v.AccountID {
		return calls.ErrForbidden
	}
	c, e := ongoing(ctx, tx, rid)
	if errors.Is(e, calls.ErrNotFound) {
		return tx.Commit()
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE call_v1.calls SET state='ending',updated_at=clock_timestamp() WHERE id=$1`, c.ID)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return r.finishEnd(ctx, c.ID)
}
func (r *Repository) finishEnd(ctx context.Context, cid string) error {
	// All known identities must be revoked even when DeleteRoom alone would
	// disconnect them: a cached join token can otherwise recreate the room.
	_, e := r.db.ExecContext(ctx, `UPDATE call_v1.members SET state='removing' WHERE call_id=$1 AND state='admitted' AND EXISTS(SELECT 1 FROM call_v1.calls WHERE id=$1 AND state='ending')`, cid)
	if e != nil {
		return e
	}
	if e = r.finishRemovals(ctx, cid, "", "*"); e != nil {
		return e
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var rid string
	e = tx.QueryRowContext(ctx, `SELECT room_id::text FROM call_v1.calls WHERE id=$1`, cid).Scan(&rid)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `SELECT 1 FROM chat_rooms WHERE id=$1 FOR UPDATE`, rid); e != nil {
		return e
	}
	c, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM call_v1.calls WHERE id=$1`, cid))
	if e != nil {
		return e
	}
	if c.State == "ended" {
		return tx.Commit()
	}
	if c.State != "ending" {
		return calls.ErrConflict
	}
	if e = r.checkProvider(c); e != nil {
		return e
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = r.provider.DeleteRoom(op, c.SessionID)
	cancel()
	if e != nil {
		return calls.ErrProvider
	}
	c, e = scan(tx.QueryRowContext(ctx, `UPDATE call_v1.calls SET state='ended',ended_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 RETURNING `+fields, cid))
	if e != nil {
		return e
	}
	if e = event(ctx, tx, c, "owned.call.ended", "", ""); e != nil {
		return e
	}
	return tx.Commit()
}

const invalidAdmission = `NOT EXISTS(SELECT 1 FROM chat_members m JOIN owned_authority_accounts a ON a.id=cm.account_id JOIN owned_authority_sessions s ON s.id=cm.session_id WHERE m.id=cm.member_id AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND NOT a.disabled AND a.version=cm.account_version AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND s.epoch=cm.session_epoch AND s.account_version=a.version)
 OR EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=cm.account_id AND p.kind='disable_account' AND (p.starts_at IS NULL OR p.starts_at<=clock_timestamp()) AND (p.expires_at IS NULL OR p.expires_at>clock_timestamp()))
 OR EXISTS(WITH RECURSIVE parents AS(SELECT s.id,s.parent_id,s.revoked_at,s.expires_at,ARRAY[s.id] AS seen,false AS cycle FROM owned_authority_sessions s WHERE s.id=cm.session_id UNION ALL SELECT s.id,s.parent_id,s.revoked_at,s.expires_at,p.seen||s.id,s.id=ANY(p.seen) FROM owned_authority_sessions s JOIN parents p ON s.id=p.parent_id WHERE NOT p.cycle AND cardinality(p.seen)<64) SELECT 1 FROM parents WHERE revoked_at IS NOT NULL OR expires_at<=clock_timestamp() OR cycle OR cardinality(seen)>=64)`

// ProcessDue performs bounded expiry/revocation recovery and projects durable
// call events into the existing SSE/WebSocket replay stream. No goroutine starts
// from New; the embedding host owns scheduler cadence and cancellation.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, calls.ErrInvalid
	}
	if e := r.configured(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT c.id::text,c.room_id::text FROM call_v1.calls c JOIN chat_rooms r ON r.id=c.room_id WHERE c.ended_at IS NULL AND (c.state='ending' OR c.expires_at<=clock_timestamp() OR r.deleted_at IS NOT NULL OR r.encryption_mode<>$1 OR EXISTS(SELECT 1 FROM call_v1.members cm WHERE cm.call_id=c.id AND (cm.state='removing' OR cm.state='admitted' AND (`+invalidAdmission+`)))) ORDER BY c.updated_at,c.id LIMIT $2`, r.cfg.NoneEncryptionMode, limit)
	if e != nil {
		return 0, e
	}
	type due struct{ id, room string }
	list := []due{}
	for rows.Next() {
		var v due
		if e = rows.Scan(&v.id, &v.room); e != nil {
			rows.Close()
			return 0, e
		}
		list = append(list, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	var result error
	for _, v := range list {
		ending, e := r.markDue(ctx, v.id, v.room)
		if e == nil {
			if ending {
				e = r.finishEnd(ctx, v.id)
			} else {
				e = r.finishRemovals(ctx, v.id, "", "*")
			}
		}
		if e != nil {
			result = errors.Join(result, e)
		} else {
			n++
		}
	}
	projected, e := r.ProjectEvents(ctx, limit)
	return n + projected, errors.Join(result, e)
}
func (r *Repository) markDue(ctx context.Context, cid, rid string) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT 1 FROM chat_rooms WHERE id=$1 FOR UPDATE`, rid); e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE call_v1.calls SET state='ending',updated_at=clock_timestamp() WHERE id=$1 AND ended_at IS NULL AND (expires_at<=clock_timestamp() OR EXISTS(SELECT 1 FROM chat_rooms WHERE id=$2 AND (deleted_at IS NOT NULL OR encryption_mode<>$3)))`, cid, rid, r.cfg.NoneEncryptionMode)
	if e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `UPDATE call_v1.members cm SET state='removing' WHERE call_id=$1 AND state='admitted' AND (`+invalidAdmission+`)`, cid)
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE call_v1.calls SET updated_at=clock_timestamp() WHERE id=$1`, cid); e != nil {
		return false, e
	}
	var ending bool
	e = tx.QueryRowContext(ctx, `SELECT state='ending' FROM call_v1.calls WHERE id=$1`, cid).Scan(&ending)
	if e != nil {
		return false, e
	}
	return ending, tx.Commit()
}

// ProjectEvents keeps recipient/account locks outside room operations to match
// notificationpg's canonical account/stream ordering and avoid lock inversion.
func (r *Repository) ProjectEvents(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, calls.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		ok, e := r.projectOne(ctx)
		if e != nil {
			return n, e
		}
		if !ok {
			return n, nil
		}
	}
	return limit, nil
}
func (r *Repository) projectOne(ctx context.Context) (bool, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var locked bool
	e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "call_v1:project:"+r.cfg.NotificationApp).Scan(&locked)
	if e != nil || !locked {
		return false, e
	}
	var id, rid, kind string
	var target sql.NullString
	var data []byte
	var at time.Time
	e = tx.QueryRowContext(ctx, `SELECT id::text,room_id::text,target_id::text,type,payload,created_at FROM call_v1.events WHERE projected_at IS NULL ORDER BY sequence LIMIT 1 FOR UPDATE`).Scan(&id, &rid, &target, &kind, &data, &at)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.account_id::text FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.chat_room_id=$1 AND m.joined_at IS NOT NULL AND m.joined_at<=$2 AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND ($3::uuid IS NULL OR m.account_id=$3) ORDER BY m.account_id`, rid, at, target)
	if e != nil {
		return false, e
	}
	recipients := []string{}
	for rows.Next() {
		var a string
		if e = rows.Scan(&a); e != nil {
			rows.Close()
			return false, e
		}
		recipients = append(recipients, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, a := range recipients {
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, a).Scan(&enabled)
		if e != nil {
			return false, e
		}
		if !enabled {
			continue
		}
		var seq int64
		e = tx.QueryRowContext(ctx, `INSERT INTO notification_v1.streams(account_id,app_id,sequence) VALUES($1,$2,1) ON CONFLICT(account_id,app_id) DO UPDATE SET sequence=notification_v1.streams.sequence+1 RETURNING sequence`, a, r.cfg.NotificationApp).Scan(&seq)
		if e != nil {
			return false, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO notification_v1.events(account_id,app_id,sequence,source_key,type,data,audience,audience_id,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,'chat',$7,$8)`, a, r.cfg.NotificationApp, seq, "call:"+id, kind, string(data), rid, at)
		if e != nil {
			return false, e
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE call_v1.events SET projected_at=clock_timestamp() WHERE id=$1`, id)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}
