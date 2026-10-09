// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
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
	statement := `SELECT bot_id,room_id,generation,session_id,issuer_room_joined_at,app_epoch,mls_binding FROM owned_bot_enrollments LIMIT 0;SELECT bot_id,room_id,enrollment_generation,consent_expires_at,active,app_epoch,mls_binding FROM owned_bot_enrollment_renewals LIMIT 0;SELECT id,message_id,state,attempts,lease_owner,lease_until FROM owned_bot_message_outbox LIMIT 0;SELECT id,oauth_epoch FROM owned_custom_apps LIMIT 0;SELECT owned_relation_guard,encryption_mode FROM chat_rooms LIMIT 0;SELECT owned_banned,owned_timeout_until FROM chat_members LIMIT 0;SELECT account_low,account_high FROM owned_chat_direct_pairs LIMIT 0;SELECT room_id,realm_id FROM owned_chat_realm_links LIMIT 0;SELECT role,joined_at,leave_at FROM owned_realm_members LIMIT 0`
	if r.execution.config.EnableMLSOpaque {
		statement += `;SELECT group_id,device_id,member_id,membership_since,state FROM owned_mls_memberships LIMIT 0`
	}
	_, e := r.db.ExecContext(ctx, statement)
	return e
}

type enrollmentRow struct {
	bots.Enrollment
	issuer, session, owner, binding                    string
	version, epoch, botVersion, ownerVersion, appEpoch int64
	mlsBinding                                         string
	joined, issuerJoined, issuerRoomJoined             time.Time
}

func enrollment(ctx context.Context, tx *sql.Tx, bot, room string) (v enrollmentRow, e error) {
	e = tx.QueryRowContext(ctx, `SELECT bot_id::text,room_id::text,adapter,generation,active,expires_at,updated_at,issuer_id::text,session_id::text,owner_id::text,account_version,session_epoch,bot_version,joined_at,owner_version,issuer_joined_at,adapter_binding,issuer_room_joined_at,app_epoch,mls_binding FROM owned_bot_enrollments WHERE bot_id=$1 AND room_id=$2`, bot, room).Scan(&v.BotID, &v.RoomID, &v.Adapter, &v.Generation, &v.Active, &v.ExpiresAt, &v.UpdatedAt, &v.issuer, &v.session, &v.owner, &v.version, &v.epoch, &v.botVersion, &v.joined, &v.ownerVersion, &v.issuerJoined, &v.binding, &v.issuerRoomJoined, &v.appEpoch, &v.mlsBinding)
	return
}

// mlsDeviceBinding snapshots the complete active bot-device membership set.
// Replacing or adding an active membership device, changing membership epochs,
// or rejoining a membership produces a distinct binding and closes old consent.
func (r *Repository) mlsDeviceBinding(ctx context.Context, tx *sql.Tx, room, account string, encrypted bool) (string, error) {
	if !encrypted {
		return "", nil
	}
	devices, e := tx.QueryContext(ctx, `SELECT id,created_at FROM owned_mls_devices WHERE account_id=$1 AND revoked_at IS NULL ORDER BY id FOR SHARE NOWAIT`, account)
	if e != nil {
		return "", e
	}
	created := map[string]time.Time{}
	for devices.Next() {
		var id string
		var at time.Time
		if e = devices.Scan(&id, &at); e != nil {
			devices.Close()
			return "", e
		}
		created[id] = at
	}
	e = devices.Err()
	devices.Close()
	if e != nil {
		return "", e
	}
	var group string
	if e = tx.QueryRowContext(ctx, `SELECT id FROM owned_mls_groups WHERE room_id=$1 FOR SHARE NOWAIT`, room).Scan(&group); e != nil {
		return "", e
	}
	rows, e := tx.QueryContext(ctx, `SELECT m.device_id,m.member_id::text,m.membership_since,m.epoch FROM owned_mls_memberships m JOIN chat_members member ON member.id=m.member_id AND member.joined_at=m.membership_since WHERE m.group_id=$1 AND m.state='active' AND m.device_id=ANY($2::text[]) AND member.chat_room_id=$3 AND member.account_id=$4 AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned ORDER BY m.device_id,m.member_id FOR SHARE OF m NOWAIT`, group, mapKeys(created), room, account)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	h := sha256.New()
	n := 0
	for rows.Next() {
		var device, member string
		var joined time.Time
		var me int64
		if e = rows.Scan(&device, &member, &joined, &me); e != nil {
			return "", e
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%d\n", group, device, created[device].UTC().Format(time.RFC3339Nano), member, joined.UTC().Format(time.RFC3339Nano), me)
		n++
	}
	if e = rows.Err(); e != nil {
		return "", e
	}
	if n == 0 {
		return "", bots.ErrForbidden
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mapKeys(values map[string]time.Time) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

type executionRoomState struct {
	owner        string
	realm        string
	encryption   int
	kind         int
	guarded      bool
	botConfirmed bool
}

// executionRoom applies the canonical shape gates for ordinary, direct and
// realm-linked rooms. MLS content remains opaque; this layer never decrypts it.
func (r *Repository) executionRoom(ctx context.Context, tx *sql.Tx, rid, issuer, botAccount string, lock bool) (state executionRoomState, e error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	e = tx.QueryRowContext(ctx, `SELECT owned_owner_id::text,encryption_mode,owned_type,owned_relation_guard FROM chat_rooms WHERE id=$1 AND deleted_at IS NULL AND owned_owner_id IS NOT NULL`+suffix, rid).Scan(&state.owner, &state.encryption, &state.kind, &state.guarded)
	if e != nil {
		return state, mapped(e)
	}
	if state.encryption != r.execution.config.NoneEncryptionMode && !(r.execution.config.EnableMLSOpaque && state.encryption == r.execution.config.MLSEncryptionMode) {
		return state, bots.ErrUnsupported
	}
	if issuer == "" {
		if state.kind < 0 || state.kind > 1 {
			return state, bots.ErrUnsupported
		}
		return state, nil
	}
	if !authority.ValidID(issuer) || !authority.ValidID(botAccount) {
		return state, bots.ErrInvalid
	}
	finish := func() (executionRoomState, error) {
		if state.encryption != r.execution.config.MLSEncryptionMode {
			return state, nil
		}
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_mls_groups g JOIN owned_mls_memberships membership ON membership.group_id=g.id JOIN owned_mls_devices device ON device.id=membership.device_id JOIN chat_members member ON member.id=membership.member_id AND member.joined_at=membership.membership_since WHERE g.room_id=$1 AND membership.state='active' AND device.account_id=$2 AND device.revoked_at IS NULL AND member.chat_room_id=$1 AND member.account_id=$2 AND member.joined_at IS NOT NULL AND member.leave_at IS NULL AND member.deleted_at IS NULL AND NOT member.owned_banned)`, rid, botAccount).Scan(&active)
		if err != nil {
			return state, err
		}
		if !active {
			return state, bots.ErrForbidden
		}
		return state, nil
	}
	if state.kind == 0 && !state.guarded {
		if state.owner != issuer {
			return state, bots.ErrForbidden
		}
		return finish()
	}
	if state.kind == 1 && state.guarded {
		var low, high string
		if e = tx.QueryRowContext(ctx, `SELECT account_low::text,account_high::text FROM owned_chat_direct_pairs WHERE room_id=$1`, rid).Scan(&low, &high); e != nil {
			return state, mapped(e)
		}
		if !((low == issuer && high == botAccount) || (high == issuer && low == botAccount)) {
			return state, bots.ErrForbidden
		}
		var members, issuerConfirmed int
		e = tx.QueryRowContext(ctx, `SELECT count(*),count(*)FILTER(WHERE account_id=$2 AND owned_confirmed_at IS NOT NULL),bool_or(account_id=$3 AND owned_confirmed_at IS NOT NULL) FROM chat_members WHERE chat_room_id=$1 AND account_id IN($2,$3) AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND(owned_timeout_until IS NULL OR owned_timeout_until<=clock_timestamp())`, rid, issuer, botAccount).Scan(&members, &issuerConfirmed, &state.botConfirmed)
		if e != nil || members != 2 || issuerConfirmed != 1 {
			if e != nil {
				return state, e
			}
			return state, bots.ErrForbidden
		}
		state.owner = issuer
		return finish()
	}
	if state.kind == 0 && state.guarded {
		var role int
		e = tx.QueryRowContext(ctx, `SELECT realm.id::text,member.role FROM owned_chat_realm_links link JOIN owned_realms realm ON realm.id=link.realm_id AND realm.deleted_at IS NULL JOIN owned_realm_members member ON member.realm_id=realm.id AND member.account_id=$2 AND member.joined_at IS NOT NULL AND member.leave_at IS NULL WHERE link.room_id=$1`, rid, issuer).Scan(&state.realm, &role)
		if e != nil {
			return state, mapped(e)
		}
		if role < 50 {
			return state, bots.ErrForbidden
		}
		var botRealm, botRoom bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL),EXISTS(SELECT 1 FROM chat_members WHERE chat_room_id=$3 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND(owned_timeout_until IS NULL OR owned_timeout_until<=clock_timestamp()))`, state.realm, botAccount, rid).Scan(&botRealm, &botRoom); e != nil {
			return state, e
		}
		if !botRealm || !botRoom {
			return state, bots.ErrForbidden
		}
		state.owner = issuer
		return finish()
	}
	return state, bots.ErrUnsupported
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
	case "set", "renew", "delete":
		permission = bots.ExecutionPermission
		role = 50
	default:
		return out, bots.ErrInvalid
	}
	if (action == "set" && (p == nil || !bots.ValidAdapterName(p.Adapter))) || action == "renew" && p == nil {
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
		roomState, x := r.executionRoom(ctx, tx, room, s.Actor, row.bot.Account.ID, true)
		if x != nil {
			return x
		}
		// Explicit enrollment needs both developer authority and the room owner's
		// authority; other editors may revoke an existing delivery binding only.
		if action != "delete" && roomState.owner != s.Actor {
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
			if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_enrollment_renewals SET active=false,revoked_at=$3,reason='enrollment_revoked',updated_at=$3 WHERE bot_id=$1 AND room_id=$2 AND active`, bot, room, now); x != nil {
				return x
			}
			// Membership belongs to canonical chat. Revoking execution never removes an
			// independently rejoined member or sends an unrequested leave event.
			out.Active = false
			out.Generation++
			out.UpdatedAt = now
			return audit(ctx, tx, s, "bot.execution.revoke", bot, now)
		}
		if action == "renew" {
			if errors.Is(x, sql.ErrNoRows) || !old.Active || !old.ExpiresAt.After(now) {
				return bots.ErrNotFound
			}
			p.Adapter = old.Adapter
			adapter := r.execution.adapters[p.Adapter]
			if adapter == nil || adapter.BotID() != bot || adapter.BindingID() != old.binding {
				return bots.ErrUnsupported
			}
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
		if roomState.kind == 1 && !cfg.AutoApproveDM || roomState.guarded && roomState.kind == 0 && !cfg.AutoApproveGroupChat {
			return bots.ErrForbidden
		}
		if roomState.kind == 1 && !roomState.botConfirmed {
			result, confirmErr := tx.ExecContext(ctx, `UPDATE chat_members SET owned_confirmed_at=$3,owned_updated_at=$3 WHERE chat_room_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL AND deleted_at IS NULL AND NOT owned_banned AND owned_confirmed_at IS NULL AND(owned_timeout_until IS NULL OR owned_timeout_until<=clock_timestamp())`, room, row.bot.Account.ID, now)
			if confirmErr != nil {
				return confirmErr
			}
			if n, rowsErr := result.RowsAffected(); rowsErr != nil || n != 1 {
				if rowsErr != nil {
					return rowsErr
				}
				return bots.ErrForbidden
			}
			roomState.botConfirmed = true
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
		mlsBinding, x := r.mlsDeviceBinding(ctx, tx, room, row.bot.Account.ID, roomState.encryption == r.execution.config.MLSEncryptionMode)
		if x != nil {
			return x
		}
		if old.Active && old.Adapter == p.Adapter && old.binding == r.execution.adapters[p.Adapter].BindingID() && old.ExpiresAt.Equal(p.ExpiresAt) && old.issuer == s.Actor && old.session == principal.Session.ID && old.version == principal.Account.Version && old.epoch == principal.Session.AuthorizationEpoch && old.owner == pubOwner && old.ownerVersion == ownerVersion && old.issuerJoined.Equal(issuerJoined) && old.issuerRoomJoined.Equal(issuerRoomJoined) && old.botVersion == row.version && old.appEpoch == scope.epoch && old.mlsBinding == mlsBinding {
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
		botJoined, x := executionMember(ctx, tx, room, row.bot.Account.ID)
		if !roomState.guarded && roomState.kind == 0 && errors.Is(x, bots.ErrForbidden) {
			memberID, idErr := newID()
			if idErr != nil {
				return idErr
			}
			// Only ordinary owner-controlled groups may create the bot membership.
			result, insertErr := tx.ExecContext(ctx, `INSERT INTO chat_members(id,chat_room_id,account_id,joined_at,owned_updated_at)VALUES($1,$2,$3,$4,$4) ON CONFLICT(chat_room_id,account_id)DO UPDATE SET joined_at=EXCLUDED.joined_at,leave_at=NULL,deleted_at=NULL,owned_updated_at=EXCLUDED.owned_updated_at WHERE NOT chat_members.owned_banned AND(chat_members.owned_timeout_until IS NULL OR chat_members.owned_timeout_until<=clock_timestamp())`, memberID, room, row.bot.Account.ID, now)
			if insertErr != nil {
				return insertErr
			}
			if n, rowsErr := result.RowsAffected(); rowsErr != nil || n != 1 {
				if rowsErr != nil {
					return rowsErr
				}
				return bots.ErrForbidden
			}
			botJoined = now
		} else if x != nil {
			return x
		}
		generation := old.Generation + 1
		_, x = tx.ExecContext(ctx, `INSERT INTO owned_bot_enrollments(bot_id,room_id,adapter,generation,active,issuer_id,session_id,account_version,session_epoch,owner_id,bot_version,joined_at,expires_at,updated_at,owner_version,issuer_joined_at,adapter_binding,issuer_room_joined_at,app_epoch,mls_binding)VALUES($1,$2,$3,$4,true,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) ON CONFLICT(bot_id,room_id)DO UPDATE SET adapter=EXCLUDED.adapter,generation=EXCLUDED.generation,active=true,issuer_id=EXCLUDED.issuer_id,session_id=EXCLUDED.session_id,account_version=EXCLUDED.account_version,session_epoch=EXCLUDED.session_epoch,owner_id=EXCLUDED.owner_id,bot_version=EXCLUDED.bot_version,joined_at=EXCLUDED.joined_at,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at,owner_version=EXCLUDED.owner_version,issuer_joined_at=EXCLUDED.issuer_joined_at,adapter_binding=EXCLUDED.adapter_binding,issuer_room_joined_at=EXCLUDED.issuer_room_joined_at,app_epoch=EXCLUDED.app_epoch,mls_binding=EXCLUDED.mls_binding`, bot, room, p.Adapter, generation, s.Actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, pubOwner, row.version, botJoined, p.ExpiresAt, now, ownerVersion, issuerJoined, r.execution.adapters[p.Adapter].BindingID(), issuerRoomJoined, scope.epoch, mlsBinding)
		if x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_message_outbox SET state='cancelled',reason='enrollment_replaced',lease_owner=NULL,lease_until=NULL WHERE bot_id=$1 AND room_id=$2 AND state='pending'`, bot, room); x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `UPDATE owned_bot_enrollment_renewals SET active=false,revoked_at=$3,reason='enrollment_replaced',updated_at=$3 WHERE bot_id=$1 AND room_id=$2 AND active`, bot, room, now); x != nil {
			return x
		}
		out = bots.Enrollment{BotID: bot, RoomID: room, Adapter: p.Adapter, Generation: generation, Active: true, ExpiresAt: p.ExpiresAt, UpdatedAt: now}
		actionName := "bot.execution.enroll"
		if action == "renew" {
			actionName = "bot.execution.renew"
		}
		if x = audit(ctx, tx, s, actionName, bot, now); x != nil {
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
	roomState, e := r.executionRoom(ctx, tx, room, "", "", false)
	if errors.Is(e, bots.ErrUnsupported) {
		return nil
	} else if e != nil {
		return e
	}
	joined, e := executionMember(ctx, tx, room, actor)
	if e != nil {
		return e
	}
	var updated, now time.Time
	encrypted := roomState.encryption == r.execution.config.MLSEncryptionMode
	e = tx.QueryRowContext(ctx, `SELECT m.updated_at,clock_timestamp() FROM chat_messages m JOIN chat_members sender ON sender.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND sender.account_id=$3 AND m.type='text' AND m.deleted_at IS NULL AND m.edited_at IS NULL AND ((NOT $4 AND(m.encryption_meta IS NULL OR m.encryption_meta='null'::jsonb))OR($4 AND m.encryption_meta IS NOT NULL AND m.encryption_meta<>'null'::jsonb)) AND m.forwarded_message_id IS NULL AND COALESCE(m.meta->'forwarded_message_id','null'::jsonb)='null'::jsonb`, message, room, actor, encrypted).Scan(&updated, &now)
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
// This owned service requires current membership in a supported room shape.
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
	if _, e = r.executionRoom(ctx, tx, room, "", "", true); e != nil {
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
