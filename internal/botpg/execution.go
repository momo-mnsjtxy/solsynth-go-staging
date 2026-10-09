// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"sort"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
)

//go:embed execution_schema.sql
var ExecutionSchema string

type recordedGuard interface {
	StoredSessionEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64, ...string) (bool, error)
}

// Snapshot registry identity. Host-provided adapters must keep their transport
// configuration immutable; drift in their declared identity fails closed.
type configuredAdapter struct {
	name, bot, binding string
	target             bots.ExecutionAdapter
}

func (a configuredAdapter) Name() string      { return a.name }
func (a configuredAdapter) BotID() string     { return a.bot }
func (a configuredAdapter) BindingID() string { return a.binding }
func (a configuredAdapter) Deliver(ctx context.Context, v bots.MessageDelivery) error {
	if a.target.Name() != a.name || a.target.BotID() != a.bot || a.target.BindingID() != a.binding {
		return bots.ErrUnavailable
	}
	return a.target.Deliver(ctx, v)
}

type executionRuntime struct {
	config   bots.ExecutionConfig
	adapters map[string]bots.ExecutionAdapter
	recorded recordedGuard
}

// WithExecution is initialization-only. It neither installs schema nor grants
// authority. An empty registry disables enrollment and delivery.
func (r *Repository) WithExecution(c bots.ExecutionConfig, adapters ...bots.ExecutionAdapter) error {
	if r == nil || r.db == nil || r.guard == nil {
		return bots.ErrUnavailable
	}
	if e := c.Validate(); e != nil {
		return e
	}
	g, ok := r.guard.(recordedGuard)
	if !ok {
		return bots.ErrUnavailable
	}
	runtime := &executionRuntime{config: c, adapters: map[string]bots.ExecutionAdapter{}, recorded: g}
	for _, a := range adapters {
		if a == nil || !bots.ValidAdapterName(a.Name()) || !authority.ValidID(a.BotID()) || !bots.ValidAdapterName(a.BindingID()) || runtime.adapters[a.Name()] != nil {
			return bots.ErrInvalid
		}
		runtime.adapters[a.Name()] = configuredAdapter{name: a.Name(), bot: a.BotID(), binding: a.BindingID(), target: a}
	}
	r.execution = runtime
	return nil
}
func (r *Repository) ExecutionReady(ctx context.Context) error {
	if r == nil || r.execution == nil {
		return bots.ErrUnsupported
	}
	_, e := r.db.ExecContext(ctx, `SELECT bot_id,room_id,generation,session_id,issuer_room_joined_at FROM owned_bot_enrollments LIMIT 0;SELECT id,message_id,state,attempts,lease_owner,lease_until FROM owned_bot_message_outbox LIMIT 0;SELECT owned_relation_guard,encryption_mode FROM chat_rooms LIMIT 0;SELECT owned_banned,owned_timeout_until FROM chat_members LIMIT 0`)
	return e
}

type enrollmentRow struct {
	bots.Enrollment
	issuer, session, owner, binding          string
	version, epoch, botVersion, ownerVersion int64
	joined, issuerJoined, issuerRoomJoined   time.Time
}

func enrollment(ctx context.Context, tx *sql.Tx, bot, room string) (v enrollmentRow, e error) {
	e = tx.QueryRowContext(ctx, `SELECT bot_id::text,room_id::text,adapter,generation,active,expires_at,updated_at,issuer_id::text,session_id::text,owner_id::text,account_version,session_epoch,bot_version,joined_at,owner_version,issuer_joined_at,adapter_binding,issuer_room_joined_at FROM owned_bot_enrollments WHERE bot_id=$1 AND room_id=$2`, bot, room).Scan(&v.BotID, &v.RoomID, &v.Adapter, &v.Generation, &v.Active, &v.ExpiresAt, &v.UpdatedAt, &v.issuer, &v.session, &v.owner, &v.version, &v.epoch, &v.botVersion, &v.joined, &v.ownerVersion, &v.issuerJoined, &v.binding, &v.issuerRoomJoined)
	return
}
func executionRoom(ctx context.Context, tx *sql.Tx, rid string, mode int, lock bool) (owner string, e error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var encryption, kind int
	var guarded bool
	e = tx.QueryRowContext(ctx, `SELECT owned_owner_id::text,encryption_mode,owned_type,owned_relation_guard FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL AND owned_owner_id IS NOT NULL`+suffix, rid).Scan(&owner, &encryption, &kind, &guarded)
	if e != nil {
		return "", mapped(e)
	}
	// Encrypted/DM/realm rooms need their complete canonical access lock protocol.
	// Never turn a missing adapter into a membership bypass or decrypt ciphertext.
	if encryption != mode || kind != 0 || guarded {
		return "", bots.ErrUnsupported
	}
	return
}
func executionMember(ctx context.Context, tx *sql.Tx, room, actor string) (joined time.Time, e error) {
	e = tx.QueryRowContext(ctx, `SELECT joined_at FROM chat_members WHERE chat_room_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND(owned_timeout_until IS NULL OR owned_timeout_until<=clock_timestamp())`, room, actor).Scan(&joined)
	if errors.Is(e, sql.ErrNoRows) {
		e = bots.ErrForbidden
	}
	return
}
func (r *Repository) Enrollment(ctx context.Context, s apps.Scope, bot, room string, p *bots.EnrollmentRequest, action string) (out bots.Enrollment, e error) {
	if r == nil || r.execution == nil {
		return out, bots.ErrUnsupported
	}
	if !authority.ValidID(bot) || !authority.ValidID(room) {
		return out, bots.ErrInvalid
	}
	if p != nil {
		copy := *p
		copy.ExpiresAt = copy.ExpiresAt.UTC().Truncate(time.Microsecond)
		p = &copy
	}
	permission := ""
	role := 0
	switch action {
	case "read":
	case "set", "delete":
		permission = bots.ExecutionPermission
		role = 50
	default:
		return out, bots.ErrInvalid
	}
	if action == "set" && (p == nil || !bots.ValidAdapterName(p.Adapter)) {
		return out, bots.ErrInvalid
	}
	if action == "set" && (r.execution.adapters[p.Adapter] == nil || r.execution.adapters[p.Adapter].BotID() != bot) {
		return out, bots.ErrUnsupported
	}
	e = r.within(ctx, s, bot, permission, role, func(tx *sql.Tx, scope appScope, now time.Time) error {
		row, x := loadBot(ctx, tx, s.App, bot)
		if x != nil {
			return x
		}
		if row.bot.DeletedAt != nil {
			return bots.ErrNotFound
		}
		owner, x := executionRoom(ctx, tx, room, r.execution.config.NoneEncryptionMode, true)
		if x != nil {
			return x
		}
		// Explicit enrollment needs both developer authority and the room owner's
		// authority; other editors may revoke an existing delivery binding only.
		if action != "delete" && owner != s.Actor {
			return bots.ErrForbidden
		}
		if x = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); x != nil {
			return x
		}
		old, x := enrollment(ctx, tx, bot, room)
		if x != nil && !errors.Is(x, sql.ErrNoRows) {
			return x
		}
		if action == "read" {
			if _, memberErr := executionMember(ctx, tx, room, s.Actor); memberErr != nil {
				return memberErr
			}
			if x != nil {
				return bots.ErrNotFound
			}
			out = old.Enrollment
			return nil
		}
		if action == "delete" {
			if errors.Is(x, sql.ErrNoRows) {
				return bots.ErrNotFound
			}
			out = old.Enrollment
			if !old.Active {
				return nil
			}
			if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_enrollments SET active=false,generation=generation+1,updated_at=$3 WHERE bot_id=$1 AND room_id=$2`, bot, room, now); x != nil {
				return x
			}
			if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state='cancelled',reason='enrollment_revoked',lease_owner=NULL,lease_until=NULL WHERE bot_id=$1 AND room_id=$2 AND state='pending'`, bot, room); x != nil {
				return x
			}
			// Membership belongs to canonical chat. Revoking execution never removes an
			// independently rejoined member or sends an unrequested leave event.
			out.Active = false
			out.Generation++
			out.UpdatedAt = now
			return audit(ctx, tx, s, "bot.execution.revoke", bot, now)
		}
		if !row.active || scope.status != 1 {
			return bots.ErrForbidden
		}
		if x = activeAccount(ctx, tx, row.bot.Account.ID, now); x != nil {
			return x
		}
		cfg, x := chat(ctx, tx, bot, row.bot.CreatedAt)
		if x != nil {
			return x
		}
		if !cfg.SupportChat {
			return bots.ErrForbidden
		}
		issuerRoomJoined, x := executionMember(ctx, tx, room, s.Actor)
		if x != nil {
			return x
		}
		principal, x := r.guard.Authorize(ctx, r.db, tx, s.Actor, bots.ExecutionPermission, "chat.invites.manage")
		if x != nil {
			return x
		}
		// Enrollment is bounded and cannot outlive the original issuing session.
		if !p.ExpiresAt.After(now) || p.ExpiresAt.After(now.Add(24*time.Hour)) || p.ExpiresAt.After(principal.Session.ExpiresAt) {
			return bots.ErrInvalid
		}
		var pubOwner string
		var ownerVersion int64
		var issuerJoined time.Time
		if x = tx.QueryRowContext(ctx, `SELECT pub.account_id::text,account.version,member.joined_at FROM owned_publishers pub JOIN owned_authority_accounts account ON account.id=pub.account_id JOIN owned_publisher_members member ON member.publisher_id=pub.id AND member.account_id=$2 WHERE pub.id=$1`, scope.publisher, s.Actor).Scan(&pubOwner, &ownerVersion, &issuerJoined); x != nil {
			return x
		}
		if old.Active && old.Adapter == p.Adapter && old.binding == r.execution.adapters[p.Adapter].BindingID() && old.ExpiresAt.Equal(p.ExpiresAt) && old.issuer == s.Actor && old.session == principal.Session.ID && old.version == principal.Account.Version && old.epoch == principal.Session.AuthorizationEpoch && old.owner == pubOwner && old.ownerVersion == ownerVersion && old.issuerJoined.Equal(issuerJoined) && old.issuerRoomJoined.Equal(issuerRoomJoined) && old.botVersion == row.version {
			joined, x := executionMember(ctx, tx, room, row.bot.Account.ID)
			if x == nil && joined.Equal(old.joined) {
				out = old.Enrollment
				_, x = r.guard.Authorize(ctx, r.db, tx, s.Actor, bots.ExecutionPermission, "chat.invites.manage")
				return x
			}
			if x != nil && !errors.Is(x, bots.ErrForbidden) {
				return x
			}
		}
		memberID, x := newID()
		if x != nil {
			return x
		}
		// Never lift a ban. An explicitly enrolled ordinary group bot joins using its
		// existing canonical account, with no synthetic login or permission grant.
		result, x := tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at,owned_updated_at)VALUES($1,$2,$3,$4,$4) ON CONFLICT(chat_room_id,account_id)DO UPDATE SET joined_at=EXCLUDED.joined_at,leave_at=NULL,deleted_at=NULL,owned_updated_at=EXCLUDED.owned_updated_at WHERE NOT chat_members.owned_banned AND(chat_members.owned_timeout_until IS NULL OR chat_members.owned_timeout_until<=clock_timestamp())`, memberID, room, row.bot.Account.ID, now)
		if x != nil {
			return x
		}
		n, x := result.RowsAffected()
		if x != nil {
			return x
		}
		if n != 1 {
			return bots.ErrForbidden
		}
		generation := old.Generation + 1
		_, x = tx.ExecContext(ctx, `INSERT INTO owned_bot_enrollments(bot_id,room_id,adapter,generation,active,issuer_id,session_id,account_version,session_epoch,owner_id,bot_version,joined_at,expires_at,updated_at,owner_version,issuer_joined_at,adapter_binding,issuer_room_joined_at)VALUES($1,$2,$3,$4,true,$5,$6,$7,$8,$9,$10,$11,$12,$11,$13,$14,$15,$16) ON CONFLICT(bot_id,room_id)DO UPDATE SET adapter=EXCLUDED.adapter,generation=EXCLUDED.generation,active=true,issuer_id=EXCLUDED.issuer_id,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,session_epoch=EXCLUDED.session_epoch,owner_id=EXCLUDED.owner_id,bot_version=EXCLUDED.bot_version,joined_at=EXCLUDED.joined_at,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at,owner_version=EXCLUDED.owner_version,issuer_joined_at=EXCLUDED.issuer_joined_at,adapter_binding=EXCLUDED.adapter_binding,issuer_room_joined_at=EXCLUDED.issuer_room_joined_at`, bot, room, p.Adapter, generation, s.Actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, pubOwner, row.version, now, p.ExpiresAt, ownerVersion, issuerJoined, r.execution.adapters[p.Adapter].BindingID(), issuerRoomJoined)
		if x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state='cancelled',reason='enrollment_replaced',lease_owner=NULL,lease_until=NULL WHERE bot_id=$1 AND room_id=$2 AND state='pending'`, bot, room); x != nil {
			return x
		}
		out = bots.Enrollment{BotID: bot, RoomID: room, Adapter: p.Adapter, Generation: generation, Active: true, ExpiresAt: p.ExpiresAt, UpdatedAt: now}
		if x = audit(ctx, tx, s, "bot.execution.enroll", bot, now); x != nil {
			return x
		}
		if x = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); x != nil {
			return x
		}
		if x = activeAccount(ctx, tx, row.bot.Account.ID, now); x != nil {
			return x
		}
		_, x = r.guard.Authorize(ctx, r.db, tx, s.Actor, bots.ExecutionPermission, "chat.invites.manage")
		return x
	})
	if e != nil {
		return bots.Enrollment{}, e
	}
	return
}

// QueueMessage is invoked by the canonical text write before commit. The chat
// transaction already holds its actor and room locks; this method never takes
// bot/owner locks in the reverse order. Only a worker with fresh authority may
// transmit the saved message. No external adapter is called in the write path.
func (r *Repository) QueueMessage(ctx context.Context, tx *sql.Tx, actor, room, message string) error {
	if r == nil || r.execution == nil {
		return bots.ErrUnsupported
	}
	if tx == nil || !authority.ValidID(actor) || !authority.ValidID(room) || !authority.ValidID(message) {
		return bots.ErrInvalid
	}
	p, e := r.guard.Authorize(ctx, r.db, tx, actor, bots.MessagePermission)
	if e != nil {
		return e
	}
	if _, e = executionRoom(ctx, tx, room, r.execution.config.NoneEncryptionMode, false); errors.Is(e, bots.ErrUnsupported) {
		return nil
	} else if e != nil {
		return e
	}
	joined, e := executionMember(ctx, tx, room, actor)
	if e != nil {
		return e
	}
	var updated, now time.Time
	e = tx.QueryRowContext(ctx, `SELECT m.updated_at,clock_timestamp() FROM chat_messages m JOIN chat_members sender ON sender.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND sender.account_id=$3 AND m.type='text' AND m.deleted_at IS NULL AND m.edited_at IS NULL AND m.encryption_meta IS NULL AND m.forwarded_message_id IS NULL AND COALESCE(m.meta->'forwarded_message_id','null'::jsonb)='null'::jsonb AND COALESCE(m.meta->'encryption_meta','null'::jsonb)='null'::jsonb`, message, room, actor).Scan(&updated, &now)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT enroll.bot_id::text,enroll.adapter,enroll.generation,enroll.expires_at FROM owned_bot_enrollments enroll JOIN owned_bots b ON b.id=enroll.bot_id JOIN chat_members member ON member.chat_room_id=enroll.room_id AND member.account_id=b.account_id JOIN owned_bot_chat cfg ON cfg.bot_id=b.id WHERE enroll.room_id=$1 AND enroll.active AND enroll.expires_at>$2 AND b.is_active AND b.deleted_at IS NULL AND b.account_id<>$3 AND cfg.support_chat AND cfg.subscribed_events ? 'messages.new' AND member.joined_at=enroll.joined_at AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned AND(member.owned_timeout_until IS NULL OR member.owned_timeout_until<=clock_timestamp()) ORDER BY enroll.bot_id LIMIT 101`, room, now, actor)
	if e != nil {
		return e
	}
	type target struct {
		bot, adapter string
		generation   int64
		expiry       time.Time
	}
	targets := []target{}
	for rows.Next() {
		var v target
		if e = rows.Scan(&v.bot, &v.adapter, &v.generation, &v.expiry); e != nil {
			rows.Close()
			return e
		}
		targets = append(targets, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(targets) > 100 {
		return bots.ErrQuota
	}
	if len(targets) == 0 {
		return nil
	}
	var pending int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_bot_message_outbox WHERE room_id=$1 AND state='pending'`, room).Scan(&pending); e != nil {
		return e
	}
	if pending+len(targets) > r.execution.config.MaxPendingPerRoom {
		return bots.ErrQuota
	}
	for _, v := range targets {
		id, x := newID()
		if x != nil {
			return x
		}
		expiry := now.Add(r.execution.config.EventLifetime)
		if v.expiry.Before(expiry) {
			expiry = v.expiry
		}
		if p.Session.ExpiresAt.Before(expiry) {
			expiry = p.Session.ExpiresAt
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_bot_message_outbox(id,bot_id,room_id,generation,adapter,actor_id,session_id,account_version,session_epoch,message_id,message_updated_at,actor_joined_at,created_at,next_attempt_at,expires_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$14)ON CONFLICT(bot_id,message_id)DO NOTHING`, id, v.bot, room, v.generation, v.adapter, actor, p.Session.ID, p.Account.Version, p.Session.AuthorizationEpoch, message, updated, joined, now, expiry)
		if e != nil {
			return e
		}
	}
	return nil
}

func lockExecutionAccounts(ctx context.Context, tx *sql.Tx, nowait bool, ids ...string) error {
	sort.Strings(ids)
	previous := ""
	suffix := " FOR UPDATE"
	if nowait {
		suffix += " NOWAIT"
	}
	for _, id := range ids {
		if id == previous {
			continue
		}
		previous = id
		var locked string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1`+suffix, id).Scan(&locked); e != nil {
			return mapped(e)
		}
	}
	return nil
}

// RoomCommands preserves the source route and automation-ID dictionary keys.
// This owned service requires current membership, and only supports plain groups.
func (r *Repository) RoomCommands(ctx context.Context, actor, room string) (out map[string][]develop.BotCommand, e error) {
	out = map[string][]develop.BotCommand{}
	if r == nil || r.execution == nil {
		return nil, bots.ErrUnsupported
	}
	if !authority.ValidID(actor) || !authority.ValidID(room) {
		return nil, bots.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return nil, e
	}
	if _, e = executionRoom(ctx, tx, room, r.execution.config.NoneEncryptionMode, true); e != nil {
		return nil, e
	}
	if _, e = executionMember(ctx, tx, room, actor); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT b.id::text FROM owned_bots b JOIN chat_members member ON member.account_id=b.account_id WHERE member.chat_room_id=$1 AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned AND(member.owned_timeout_until IS NULL OR member.owned_timeout_until<=clock_timestamp()) ORDER BY b.id LIMIT 101`, room)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(ids) > 100 {
		return nil, bots.ErrQuota
	}
	for _, id := range ids {
		app, x := publicBotApp(ctx, tx, id)
		if errors.Is(x, bots.ErrNotFound) {
			continue
		}
		if x != nil {
			return nil, x
		}
		b, x := loadBot(ctx, tx, app, id)
		if x != nil {
			return nil, x
		}
		cfg, x := chat(ctx, tx, id, b.bot.CreatedAt)
		if x != nil {
			return nil, x
		}
		if cfg.SupportChat && len(cfg.Commands) > 0 {
			out[id] = cfg.Commands
		}
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return nil, e
	}
	e = tx.Commit()
	if e != nil {
		return nil, e
	}
	return
}

func (r *Repository) ExecutionEnabled() bool { return r != nil && r.execution != nil }
