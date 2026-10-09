// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/develop/bots"
)

type messageEvent struct {
	id, bot, room, adapter, actor, session, message, state string
	generation, version, epoch                             int64
	updated, joined, expires, next                         time.Time
	attempts                                               int
	leaseOwner                                             sql.NullString
	leaseUntil                                             sql.NullTime
}

func messageEventRow(ctx context.Context, tx *sql.Tx, id string, lock bool) (v messageEvent, e error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE NOWAIT"
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text,bot_id::text,room_id::text,adapter,actor_id::text,session_id::text,message_id::text,state,generation,account_version,session_epoch,message_updated_at,actor_joined_at,expires_at,next_attempt_at,attempts,lease_owner::text,lease_until FROM owned_bot_message_outbox WHERE id=$1`+suffix, id).Scan(&v.id, &v.bot, &v.room, &v.adapter, &v.actor, &v.session, &v.message, &v.state, &v.generation, &v.version, &v.epoch, &v.updated, &v.joined, &v.expires, &v.next, &v.attempts, &v.leaseOwner, &v.leaseUntil)
	return
}

type executionAncestry struct{ account, app, project, publisher, owner string }

func observeExecution(ctx context.Context, tx *sql.Tx, bot string) (v executionAncestry, e error) {
	e = tx.QueryRowContext(ctx, `SELECT b.account_id::text,b.app_id::text,b.project_id::text,pub.id::text,COALESCE(pub.account_id::text,'')`+ancestry+` JOIN owned_bots b ON b.app_id=a.id WHERE b.id=$1`, bot).Scan(&v.account, &v.app, &v.project, &v.publisher, &v.owner)
	return
}
func cancelMessageEvent(ctx context.Context, tx *sql.Tx, id, reason string) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state='cancelled',reason=$2,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND state='pending'`, id, reason); e != nil {
		return e
	}
	return tx.Commit()
}

type messageDispatch struct {
	token    string
	attempt  int
	deadline time.Time
	payload  bots.MessageDelivery
	adapter  bots.ExecutionAdapter
	ready    bool
}

// prepareMessageEvent validates the complete current authority twice: once when
// claiming a lease, and again after that claim commits. No adapter runs here.
func (r *Repository) prepareMessageEvent(ctx context.Context, id string, dispatch *messageDispatch) (processed bool, e error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	v, e := messageEventRow(ctx, tx, id, false)
	if e != nil {
		return false, e
	}
	if v.state != "pending" {
		return false, nil
	}
	old, e := enrollment(ctx, tx, v.bot, v.room)
	if e != nil {
		return false, e
	}
	observed, e := observeExecution(ctx, tx, v.bot)
	if e != nil {
		return false, e
	}
	// NULL organization owners are rejected before any operation, preserving the
	// canonical authority prerequisite for imported legacy associations.
	if observed.owner == "" {
		if _, e = messageEventRow(ctx, tx, id, true); e != nil {
			return false, e
		}
		return true, cancelMessageEvent(ctx, tx, id, "owner_unavailable")
	}
	if e = lockExecutionAccounts(ctx, tx, true, v.actor, old.issuer, old.owner, observed.owner, observed.account); e != nil {
		return false, e
	}
	var locked string
	// An enrollment issuer is the original room owner. If ownership changes, this
	// old binding cannot acquire the new owner's identity or permission.
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, observed.publisher).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 FOR UPDATE NOWAIT`, observed.publisher, old.issuer).Scan(&locked); errors.Is(e, sql.ErrNoRows) {
		if _, e = messageEventRow(ctx, tx, id, true); e != nil {
			return false, e
		}
		return true, cancelMessageEvent(ctx, tx, id, "issuer_membership_revoked")
	} else if e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_dev_projects WHERE id=$1 FOR UPDATE NOWAIT`, observed.project).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_custom_apps WHERE id=$1 FOR UPDATE NOWAIT`, observed.app).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_bots WHERE id=$1 FOR UPDATE NOWAIT`, v.bot).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_rooms WHERE id=$1 FOR UPDATE NOWAIT`, v.room).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT bot_id::text FROM owned_bot_enrollments WHERE bot_id=$1 AND room_id=$2 FOR UPDATE NOWAIT`, v.bot, v.room).Scan(&locked); e != nil {
		return false, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM chat_messages WHERE id=$1 FOR UPDATE NOWAIT`, v.message).Scan(&locked); e != nil {
		return false, e
	}
	v, e = messageEventRow(ctx, tx, id, true)
	if e != nil {
		return false, e
	}
	if v.state != "pending" {
		return false, nil
	}
	var at time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&at); e != nil {
		return false, e
	}
	if dispatch.token != "" {
		if !v.leaseOwner.Valid || v.leaseOwner.String != dispatch.token || v.attempts != dispatch.attempt || !v.leaseUntil.Valid || !v.leaseUntil.Time.After(at) {
			return false, nil
		}
	} else if v.leaseUntil.Valid && v.leaseUntil.Time.After(at) {
		return false, nil
	}
	if v.next.After(at) {
		return false, nil
	}
	var prior bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_bot_message_outbox prior JOIN owned_bot_message_outbox current ON current.id=$1 WHERE prior.bot_id=current.bot_id AND prior.room_id=current.room_id AND prior.state='pending' AND prior.sequence<current.sequence)`, id).Scan(&prior); e != nil {
		return false, e
	}
	if prior {
		return false, nil
	}
	current, e := enrollment(ctx, tx, v.bot, v.room)
	if e != nil {
		return false, e
	}
	fresh, e := observeExecution(ctx, tx, v.bot)
	if e != nil {
		return false, e
	}
	cancel := func(reason string) (bool, error) { return true, cancelMessageEvent(ctx, tx, id, reason) }
	if observed != fresh || !current.Active || current.Generation != v.generation || current.Adapter != v.adapter || current.issuer != old.issuer || current.owner != observed.owner || !current.ExpiresAt.After(at) || !v.expires.After(at) {
		return cancel("enrollment_or_ancestry_changed")
	}
	roomOwner, e := executionRoom(ctx, tx, v.room, r.execution.config.NoneEncryptionMode, false)
	if errors.Is(e, bots.ErrNotFound) || errors.Is(e, bots.ErrUnsupported) || e == nil && roomOwner != current.issuer {
		return cancel("room_changed")
	}
	if e != nil {
		return false, e
	}
	app, e := publicBotApp(ctx, tx, v.bot)
	if errors.Is(e, bots.ErrNotFound) {
		return cancel("bot_or_owner_revoked")
	}
	if e != nil {
		return false, e
	}
	if app != fresh.app {
		return cancel("app_changed")
	}
	row, e := loadBot(ctx, tx, app, v.bot)
	if e != nil {
		return false, e
	}
	if row.version != current.botVersion {
		return cancel("bot_version_changed")
	}
	var member bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND revoked_at IS NULL AND role>=50 AND joined_at=$3)`, fresh.publisher, current.issuer, current.issuerJoined).Scan(&member); e != nil {
		return false, e
	}
	if !member {
		return cancel("issuer_membership_revoked")
	}
	var ownerVersion int64
	if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, current.owner).Scan(&ownerVersion); e != nil {
		return false, e
	}
	if ownerVersion != current.ownerVersion {
		return cancel("owner_version_changed")
	}
	for _, a := range []string{v.actor, current.issuer, current.owner, fresh.account} {
		if e = activeAccount(ctx, tx, a, at); errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) {
			return cancel("account_revoked")
		} else if e != nil {
			return false, e
		}
	}
	actorJoined, e := executionMember(ctx, tx, v.room, v.actor)
	if errors.Is(e, bots.ErrForbidden) {
		return cancel("sender_membership_revoked")
	}
	if e != nil {
		return false, e
	}
	if !actorJoined.Equal(v.joined) {
		return cancel("sender_membership_replaced")
	}
	botJoined, e := executionMember(ctx, tx, v.room, fresh.account)
	if errors.Is(e, bots.ErrForbidden) {
		return cancel("bot_membership_revoked")
	}
	if e != nil {
		return false, e
	}
	if !botJoined.Equal(current.joined) {
		return cancel("bot_membership_replaced")
	}
	issuerRoomJoined, e := executionMember(ctx, tx, v.room, current.issuer)
	if errors.Is(e, bots.ErrForbidden) {
		return cancel("room_owner_membership_revoked")
	} else if e != nil {
		return false, e
	}
	if !issuerRoomJoined.Equal(current.issuerRoomJoined) {
		return cancel("issuer_room_membership_replaced")
	}
	if v.updated.Before(botJoined) || v.updated.Before(actorJoined) {
		return cancel("message_precedes_membership")
	}
	cfg, e := chat(ctx, tx, v.bot, row.bot.CreatedAt)
	if e != nil {
		return false, e
	}
	subscribed := false
	for _, s := range cfg.SubscribedEvents {
		if s == "messages.new" {
			subscribed = true
		}
	}
	if !cfg.SupportChat || !subscribed {
		return cancel("subscription_revoked")
	}
	payload := bots.MessageDelivery{EventID: v.id, EventType: "bot.chat.message", BotID: v.bot, BotAccountID: fresh.account, RoomID: v.room, MessageID: v.message, SenderAccountID: v.actor, MessageType: "text"}
	var updated time.Time
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(m.content,''),m.created_at,m.updated_at FROM chat_messages m JOIN chat_members sender ON sender.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND sender.account_id=$3 AND m.type='text' AND m.deleted_at IS NULL AND m.edited_at IS NULL AND m.encryption_meta IS NULL AND m.forwarded_message_id IS NULL AND COALESCE(m.meta->'forwarded_message_id','null'::jsonb)='null'::jsonb AND COALESCE(m.meta->'encryption_meta','null'::jsonb)='null'::jsonb`, v.message, v.room, v.actor).Scan(&payload.Content, &payload.CreatedAt, &updated)
	if errors.Is(e, sql.ErrNoRows) {
		return cancel("message_deleted_or_changed")
	}
	if e != nil {
		return false, e
	}
	if !updated.Equal(v.updated) {
		return cancel("message_changed")
	}
	payload.CreatedAt = payload.CreatedAt.UTC()
	payload.Meta = json.RawMessage(`null`)
	// Arbitrary client metadata may contain private reply/forward/embed data.
	// This bounded text contract never sends it to a third-party adapter.
	adapter := r.execution.adapters[v.adapter]
	if adapter != nil && adapter.BindingID() != current.binding {
		return cancel("adapter_binding_changed")
	}
	if adapter == nil || adapter.BotID() != v.bot {
		return false, bots.ErrUnsupported
	}
	// Fresh recorded proof checks are the final authority observation. They
	// authorize dispatch, not provider acceptance; revocation after this committed
	// observation cannot retract an in-flight external request.
	alive, e := r.execution.recorded.StoredSessionEligible(ctx, r.db, tx, current.issuer, current.session, current.version, current.epoch, bots.ExecutionPermission, "chat.invites.manage")
	if e != nil {
		return false, e
	}
	if !alive {
		return cancel("issuer_authority_revoked")
	}
	alive, e = r.execution.recorded.StoredSessionEligible(ctx, r.db, tx, v.actor, v.session, v.version, v.epoch, bots.MessagePermission)
	if e != nil {
		return false, e
	}
	if !alive {
		return cancel("sender_authority_revoked")
	}
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&at); e != nil {
		return false, e
	}
	if !v.expires.After(at) || !current.ExpiresAt.After(at) {
		return cancel("expired")
	}
	// Time-based disable punishments can start while SQL locks are held.
	for _, actor := range []string{v.actor, current.issuer, current.owner, fresh.account} {
		if e = activeAccount(ctx, tx, actor, at); errors.Is(e, bots.ErrForbidden) || errors.Is(e, bots.ErrNotFound) {
			return cancel("account_revoked")
		} else if e != nil {
			return false, e
		}
	}
	deadline := at.Add(r.execution.config.SendTimeout)
	if v.expires.Before(deadline) {
		deadline = v.expires
	}
	if current.ExpiresAt.Before(deadline) {
		deadline = current.ExpiresAt
	}
	if dispatch.token == "" {
		if v.attempts >= r.execution.config.MaxAttempts {
			_, e = tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state='failed',reason='attempts_exhausted_ambiguous',lease_owner=NULL,lease_until=NULL WHERE id=$1`, id)
			if e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
		token, x := newID()
		if x != nil {
			return false, x
		}
		// The lease includes bounded SQL acknowledgement grace, but send itself is
		// still limited by SendTimeout and both original expiries.
		leaseUntil := deadline.Add(5 * time.Second)
		if v.expires.Before(leaseUntil) {
			leaseUntil = v.expires
		}
		if current.ExpiresAt.Before(leaseUntil) {
			leaseUntil = current.ExpiresAt
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET lease_owner=$2::uuid,lease_until=$3,attempts=attempts+1,reason='dispatch_claimed' WHERE id=$1`, id, token, leaseUntil)
		if e != nil {
			return false, e
		}
		if e = tx.Commit(); e != nil {
			return false, e
		}
		dispatch.token, dispatch.attempt = token, v.attempts+1
		return false, nil
	}
	if v.leaseUntil.Time.Before(deadline) {
		deadline = v.leaseUntil.Time
	}
	if e = tx.Commit(); e != nil {
		return false, e
	}
	dispatch.payload, dispatch.adapter, dispatch.deadline, dispatch.ready = payload, adapter, deadline, true
	return false, nil
}

func (r *Repository) deliverMessageEvent(ctx context.Context, id string) (bool, error) {
	dispatch := &messageDispatch{}
	processed, e := r.prepareMessageEvent(ctx, id, dispatch)
	if e != nil || processed || dispatch.token == "" {
		return processed, e
	}
	processed, e = r.prepareMessageEvent(ctx, id, dispatch)
	if e != nil || processed || !dispatch.ready {
		return processed, e
	}
	// Both SQL transactions have committed. External I/O never retains SQL locks.
	op, stop := context.WithDeadline(ctx, dispatch.deadline)
	if e = op.Err(); e != nil {
		stop()
		return false, e
	}
	sendErr := dispatch.adapter.Deliver(op, dispatch.payload)
	if sendErr == nil {
		sendErr = op.Err()
	}
	stop()
	return r.finishMessageEvent(ctx, id, dispatch, sendErr)
}

func (r *Repository) finishMessageEvent(ctx context.Context, id string, dispatch *messageDispatch, sendErr error) (bool, error) {
	state, reason := "accepted", ""
	delay := time.Duration(0)
	if sendErr != nil {
		state, reason = "pending", "adapter_result_ambiguous"
		if dispatch.attempt >= r.execution.config.MaxAttempts {
			state, reason = "failed", "attempts_exhausted_ambiguous"
		}
		delay = time.Second * time.Duration(1<<min(dispatch.attempt-1, 6))
		if delay > time.Minute {
			delay = time.Minute
		}
	}
	// A stale or late worker cannot clear a newer lease or overwrite revocation.
	// Failed SQL acknowledgement leaves the durable claim for bounded recovery.
	result, e := r.db.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state=$4,reason=NULLIF($5,''),next_attempt_at=clock_timestamp()+$6::bigint*interval '1 millisecond',accepted_at=CASE WHEN $4='accepted' THEN clock_timestamp() ELSE NULL END,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2::uuid AND attempts=$3 AND state='pending' AND lease_until>clock_timestamp()`, id, dispatch.token, dispatch.attempt, state, reason, delay.Milliseconds())
	if e != nil {
		return false, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return false, e
	}
	if n != 1 {
		return false, bots.ErrUnavailable
	}
	if sendErr != nil {
		return false, bots.ErrUnavailable
	}
	return true, nil
}

// ProcessMessagesDue performs bounded at-least-once delivery. Missing adapters,
// SQL failures and contention never turn into successful external acceptance.
func (r *Repository) ProcessMessagesDue(ctx context.Context, limit int) (int, error) {
	if r == nil || r.execution == nil {
		return 0, bots.ErrUnsupported
	}
	if limit < 1 || limit > 100 {
		return 0, bots.ErrInvalid
	}
	names := []string{}
	for name := range r.execution.adapters {
		names = append(names, name)
	}
	if len(names) == 0 {
		return 0, nil
	}
	rows, e := r.db.QueryContext(ctx, `SELECT o.id::text FROM owned_bot_message_outbox o WHERE o.state='pending' AND o.adapter IN(SELECT jsonb_array_elements_text($2::jsonb)) AND o.next_attempt_at<=clock_timestamp() AND(o.lease_until IS NULL OR o.lease_until<=clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM owned_bot_message_outbox prior WHERE prior.bot_id=o.bot_id AND prior.room_id=o.room_id AND prior.state='pending' AND prior.sequence<o.sequence)ORDER BY o.sequence LIMIT $1`, limit, encoded(names))
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	processed := 0
	var result error
	for _, id := range ids {
		if ctx.Err() != nil {
			return processed, errors.Join(result, ctx.Err())
		}
		ok, x := r.deliverMessageEvent(ctx, id)
		var pg *pgconn.PgError
		if errors.As(x, &pg) && pg.Code == "55P03" {
			continue
		}
		if ok && x == nil {
			processed++
		}
		result = errors.Join(result, x)
	}
	return processed, result
}
