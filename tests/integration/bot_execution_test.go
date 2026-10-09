// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

// This adapter is synthetic. It opens no socket and makes no external request.
type syntheticBotDelivery struct {
	bot     string
	binding string
	mu      sync.Mutex
	values  []bots.MessageDelivery
	fail    bool
	entered chan struct{}
	release chan struct{}
	hook    func(context.Context, bots.MessageDelivery) error
}

func (s *syntheticBotDelivery) Name() string  { return "synthetic-" + s.bot }
func (s *syntheticBotDelivery) BotID() string { return s.bot }
func (s *syntheticBotDelivery) BindingID() string {
	if s.binding != "" {
		return s.binding
	}
	return "synthetic-v1"
}
func (s *syntheticBotDelivery) Deliver(ctx context.Context, v bots.MessageDelivery) error {
	s.mu.Lock()
	s.values = append(s.values, v)
	fail := s.fail
	s.mu.Unlock()
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.hook != nil {
		return s.hook(ctx, v)
	}
	if fail {
		return errors.New("synthetic provider failure")
	}
	return nil
}
func (s *syntheticBotDelivery) delivered() []bots.MessageDelivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bots.MessageDelivery{}, s.values...)
}
func botExecutionConfig() bots.ExecutionConfig {
	return bots.ExecutionConfig{NoneEncryptionMode: int(chatPolicy.NoneEncryptionMode), SendTimeout: 5 * time.Second, EventLifetime: 45 * time.Minute, MaxAttempts: 3, MaxPendingPerRoom: 100}
}

type botExecutionFixture struct {
	botFixture
	chat     *chatpg.Repository
	bot      bots.Bot
	room     rooms.Room
	adapter  *syntheticBotDelivery
	otherCtx context.Context
}

func newBotExecutionFixture(t *testing.T) botExecutionFixture {
	t.Helper()
	f := botExecutionFixture{botFixture: newBotFixture(t)}
	f.bot = botCreate(t, f.botFixture, "execution")
	query(t, f.db, chatpg.Schema)
	query(t, f.db, botpg.ExecutionSchema)
	var e error
	f.chat, e = chatpg.NewWithAuthority(f.db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	name := "Synthetic bot execution room"
	f.room, e = f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name})
	if e != nil {
		t.Fatal(e)
	}
	f.adapter = &syntheticBotDelivery{bot: f.bot.ID}
	if e = f.repo.WithExecution(botExecutionConfig(), f.adapter); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.ExecutionReady(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.chat.WithTextMessageObserver(f.repo)
	p, e := f.v.Authenticate(f.ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	f.otherCtx = f.guard.Bind(f.ctx, p)
	if _, e = f.chat.ChangeMember(f.ctx, f.actor, f.room.ID, rooms.Invite, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.ChangeMember(f.otherCtx, f.other, f.room.ID, rooms.Accept, ""); e != nil {
		t.Fatal(e)
	}
	commands := []develop.BotCommand{{Name: "ping", Description: "Synthetic test", Parameters: []develop.BotCommandParameter{}}}
	if _, e = f.repo.Chat(f.ctx, f.scope, f.bot.ID, &bots.ManifestPatch{Commands: &commands}, false); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(40 * time.Minute)}, "set"); e != nil {
		t.Fatal(e)
	}
	return f
}
func botExecutionSend(t *testing.T, f botExecutionFixture, key, content string) messager.MessageProjection {
	t.Helper()
	v, e := f.chat.Send(f.otherCtx, f.other, f.room.ID, rooms.TextRequest{Content: content, ClientMessageID: &key})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func botExecutionState(t *testing.T, f botExecutionFixture, mid string) (string, int) {
	t.Helper()
	var state string
	var attempts int
	if e := f.db.QueryRowContext(f.ctx, `SELECT state,attempts FROM owned_bot_message_outbox WHERE bot_id=$1 AND message_id=$2`, f.bot.ID, mid).Scan(&state, &attempts); e != nil {
		t.Fatal(e)
	}
	return state, attempts
}
func botExecutionDue(t *testing.T, f botExecutionFixture) {
	t.Helper()
	query(t, f.db, `UPDATE owned_bot_message_outbox SET next_attempt_at=clock_timestamp() WHERE bot_id=$1 AND room_id=$2 AND state='pending'`, f.bot.ID, f.room.ID)
}
func TestPGBotExecutionCanonicalSendAndReplay(t *testing.T) {
	f := newBotExecutionFixture(t)
	v, e := f.repo.RoomCommands(f.otherCtx, f.other, f.room.ID)
	if e != nil || len(v) != 1 || len(v[f.bot.ID]) != 1 {
		t.Fatal(v, e)
	}
	backendCall(t, f.http, "GET", "/api/chat/"+f.room.ID+"/bots/commands", f.otherToken, "", 200)
	backendCall(t, f.http, "GET", "/api/chat/"+f.room.ID+"/bots/commands", "", "", 401)
	first := botExecutionSend(t, f, "same", "/ping synthetic")
	old, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, nil, "read")
	if e != nil {
		t.Fatal(e)
	}
	replay, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: old.Adapter, ExpiresAt: old.ExpiresAt}, "set")
	if e != nil || replay.Generation != old.Generation {
		t.Fatal("enrollment replay", replay, e)
	}

	again := botExecutionSend(t, f, "same", "/ping synthetic")
	if first.ID != again.ID {
		t.Fatal("chat retry changed message")
	}
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM owned_bot_message_outbox WHERE bot_id=$1 AND room_id=$2`, f.bot.ID, f.room.ID); n != 1 {
		t.Fatal("duplicate outbox", n)
	}
	if len(f.adapter.delivered()) != 0 {
		t.Fatal("request invoked provider")
	}
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	deliveries := f.adapter.delivered()
	if len(deliveries) != 1 || deliveries[0].MessageID != first.ID || deliveries[0].BotID != f.bot.ID || deliveries[0].BotAccountID != f.bot.Account.ID || deliveries[0].SenderAccountID != f.other || deliveries[0].Content != "/ping synthetic" {
		t.Fatal("canonical payload", deliveries)
	}
	if s, n := botExecutionState(t, f, first.ID); s != "accepted" || n != 1 {
		t.Fatal(s, n)
	}
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 0 || len(f.adapter.delivered()) != 1 {
		t.Fatal("accepted replay", n, e)
	}
	if n := botCount(t, f.botFixture, `SELECT(SELECT count(*)FROM owned_authority_sessions WHERE account_id=$1)+(SELECT count(*)FROM owned_authority_grants WHERE account_id=$1)`, f.bot.Account.ID); n != 0 {
		t.Fatal("bot credentials created")
	}
}
func TestPGBotExecutionEnrollmentScopesAndHTTP(t *testing.T) {
	f := newBotExecutionFixture(t)
	root := "/api/private/bots/" + f.bot.ID + "/execution/" + f.room.ID + f.query
	backendCall(t, f.http, "GET", root, f.token, "", 200)
	backendCall(t, f.http, "GET", root, f.otherToken, "", 403)
	backendCall(t, f.http, "PUT", root, f.token, `{"adapter":"unconfigured","expires_at":"2099-01-01T00:00:00Z"}`, 501)
	backendCall(t, f.http, "PUT", root, f.token, `{"adapter":"synthetic","account_id":"spoofed"}`, 400)
	// The original proof and exact policy names are mandatory, no wildcard grant
	// is created to make a bot or newly requested operation appear authorized.
	if _, e := f.repo.Enrollment(context.Background(), f.scope, f.bot.ID, f.room.ID, nil, "read"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("missing proof", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: bots.ExecutionPermission}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(10 * time.Minute)}, "set"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("missing invite scope", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "chat.invites.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(10 * time.Minute)}, "set"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("missing enrollment scope", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	second := botCreate(t, f.botFixture, "bound-adapter")
	if _, e := f.repo.Enrollment(f.ctx, f.scope, second.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(10 * time.Minute)}, "set"); !errors.Is(e, bots.ErrUnsupported) {
		t.Fatal("cross-bot adapter", e)
	}
	query(t, f.db, `UPDATE chat_rooms SET owned_relation_guard=true WHERE id=$1`, f.room.ID)
	if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, nil, "read"); !errors.Is(e, bots.ErrUnsupported) {
		t.Fatal("guarded room", e)
	}
	query(t, f.db, `UPDATE chat_rooms SET owned_relation_guard=false,encryption_mode=3 WHERE id=$1`, f.room.ID)
	if _, e := f.repo.RoomCommands(f.otherCtx, f.other, f.room.ID); !errors.Is(e, bots.ErrUnsupported) {
		t.Fatal("encrypted room", e)
	}
}
func TestPGBotExecutionFreshRevocationAndMembership(t *testing.T) {
	modes := []string{"sender_session", "issuer_session", "issuer_permission", "issuer_membership", "publisher_owner", "owner_disabled", "owner_punishment", "issuer_membership_rejoined", "sender_scopes", "adapter_changed", "bot_disabled", "bot_reactivated", "app_suspended", "subscription", "sender_leave", "sender_rejoin", "bot_leave", "bot_rejoin", "room_owner", "message_edit", "message_delete", "enrollment", "enrollment_replace", "issuer_room_rejoin", "sender_timeout", "bot_timeout", "forwarded_reference", "encryption_metadata", "app_draft", "issuer_session_scopes", "sender_session_scopes"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			f := newBotExecutionFixture(t)
			m := botExecutionSend(t, f, "pending", "/ping before revocation")
			switch mode {
			case "issuer_room_rejoin":
				query(t, f.db, `UPDATE chat_members SET joined_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, f.actor)
			case "sender_timeout", "bot_timeout":
				actor := f.other
				if mode == "bot_timeout" {
					actor = f.bot.Account.ID
				}
				query(t, f.db, `UPDATE chat_members SET owned_timeout_until=clock_timestamp()+interval '1 hour' WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, actor)
			case "forwarded_reference":
				query(t, f.db, `UPDATE chat_messages SET forwarded_message_id=id WHERE id=$1`, m.ID)
			case "encryption_metadata":
				query(t, f.db, `UPDATE chat_messages SET meta='{"encryption_meta":{"ciphertext":"synthetic opaque"}}'::jsonb WHERE id=$1`, m.ID)
			case "app_draft":
				query(t, f.db, `UPDATE owned_custom_apps SET status=0 WHERE id=$1`, f.app.ID)
			case "issuer_session_scopes", "sender_session_scopes":
				actor := f.actor
				if mode == "sender_session_scopes" {
					actor = f.other
				}
				query(t, f.db, `UPDATE owned_authority_sessions SET scopes='["chat.rooms.view"]'::jsonb WHERE account_id=$1`, actor)
			case "sender_session":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			case "issuer_session":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
			case "issuer_permission":
				if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "chat.invites.manage"}}, nil); e != nil {
					t.Fatal(e)
				}
			case "issuer_membership":
				query(t, f.db, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.actor)
			case "publisher_owner":
				query(t, f.db, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, f.app.PublisherID, f.other)
			case "adapter_changed":
				if e := f.repo.WithExecution(botExecutionConfig(), &syntheticBotDelivery{bot: f.bot.ID, binding: "synthetic-v2"}); e != nil {
					t.Fatal(e)
				}
			case "owner_disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor)
			case "owner_punishment":
				if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
					t.Fatal(e)
				}
			case "issuer_membership_rejoined":
				query(t, f.db, `UPDATE owned_publisher_members SET joined_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.actor)
			case "sender_scopes":
				if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "chat.rooms.view"}}, nil); e != nil {
					t.Fatal(e)
				}
			case "bot_disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.bot.Account.ID)
			case "bot_reactivated":
				off, on := false, true
				if _, e := f.repo.Bots(f.ctx, f.scope, f.bot.ID, &bots.Patch{IsActive: &off}, "update"); e != nil {
					t.Fatal(e)
				}
				if _, e := f.repo.Bots(f.ctx, f.scope, f.bot.ID, &bots.Patch{IsActive: &on}, "update"); e != nil {
					t.Fatal(e)
				}
			case "app_suspended":
				query(t, f.db, `UPDATE owned_custom_apps SET status=2 WHERE id=$1`, f.app.ID)
			case "subscription":
				events := []string{}
				if _, e := f.repo.Chat(f.ctx, f.scope, f.bot.ID, &bots.ManifestPatch{SubscribedEvents: &events}, false); e != nil {
					t.Fatal(e)
				}
			case "sender_leave":
				query(t, f.db, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, f.other)
			case "sender_rejoin":
				query(t, f.db, `UPDATE chat_members SET joined_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, f.other)
			case "bot_leave":
				query(t, f.db, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, f.bot.Account.ID)
			case "bot_rejoin":
				query(t, f.db, `UPDATE chat_members SET joined_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, f.room.ID, f.bot.Account.ID)
			case "room_owner":
				query(t, f.db, `UPDATE chat_rooms SET owned_owner_id=$2 WHERE id=$1`, f.room.ID, f.other)
			case "message_edit":
				if _, e := f.chat.Edit(f.otherCtx, f.other, f.room.ID, m.ID, rooms.TextRequest{Content: "/ping changed"}); e != nil {
					t.Fatal(e)
				}
			case "message_delete":
				query(t, f.db, `UPDATE chat_messages SET deleted_at=clock_timestamp() WHERE id=$1`, m.ID)
			case "enrollment":
				if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, nil, "delete"); e != nil {
					t.Fatal(e)
				}
			case "enrollment_replace":
				if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(10 * time.Minute)}, "set"); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.repo.ProcessMessagesDue(context.Background(), 100); e != nil {
				t.Fatal(e)
			}
			if len(f.adapter.delivered()) != 0 {
				t.Fatal("revoked message transmitted")
			}
			if state, _ := botExecutionState(t, f, m.ID); state != "cancelled" {
				t.Fatal(state)
			}
		})
	}
}
func TestPGBotExecutionRetryOrderingAndAtomicRollback(t *testing.T) {
	f := newBotExecutionFixture(t)
	first := botExecutionSend(t, f, "one", "/ping first")
	second := botExecutionSend(t, f, "two", "/ping second")
	f.adapter.fail = true
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); !errors.Is(e, bots.ErrUnavailable) {
		t.Fatal(e)
	}
	if s, n := botExecutionState(t, f, first.ID); s != "pending" || n != 1 {
		t.Fatal(s, n)
	}
	if s, n := botExecutionState(t, f, second.ID); s != "pending" || n != 0 {
		t.Fatal("ordering", s, n)
	}
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if len(f.adapter.delivered()) != 1 {
		t.Fatal("backoff bypass")
	}
	f.adapter.fail = false
	botExecutionDue(t, f)
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	delivered := f.adapter.delivered()
	if len(delivered) != 3 || delivered[0].EventID != delivered[1].EventID || delivered[2].MessageID != second.ID {
		t.Fatal("unstable retries", delivered)
	}
	// Observer failure rolls back the canonical message and sequence allocation.
	constraint := "synthetic_bot_" + strings.ReplaceAll(authorityUUID(t), "-", "")
	query(t, f.db, `ALTER TABLE owned_bot_message_outbox ADD CONSTRAINT `+constraint+` CHECK(bot_id<>'`+f.bot.ID+`'::uuid) NOT VALID`)
	defer query(t, f.db, `ALTER TABLE owned_bot_message_outbox DROP CONSTRAINT `+constraint)
	before := botCount(t, f.botFixture, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, f.room.ID)
	var beforeSequence int64
	if e := f.db.QueryRowContext(f.ctx, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, f.room.ID).Scan(&beforeSequence); e != nil {
		t.Fatal(e)
	}
	key := "rolled-back"
	if _, e := f.chat.Send(f.otherCtx, f.other, f.room.ID, rooms.TextRequest{Content: "/ping rollback", ClientMessageID: &key}); e == nil {
		t.Fatal("failed queue accepted message")
	}
	if n := botCount(t, f.botFixture, `SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, f.room.ID); n != before {
		t.Fatal("orphan canonical message")
	}
	var afterSequence int64
	if e := f.db.QueryRowContext(f.ctx, `SELECT owned_next_sequence FROM chat_rooms WHERE id=$1`, f.room.ID).Scan(&afterSequence); e != nil || beforeSequence != afterSequence {
		t.Fatal("sequence allocation survived rollback", beforeSequence, afterSequence, e)
	}
}
func TestPGBotExecutionConcurrentWorkersAndRevocationWait(t *testing.T) {
	f := newBotExecutionFixture(t)
	m := botExecutionSend(t, f, "parallel", "/ping locks")
	f.adapter.entered = make(chan struct{}, 1)
	f.adapter.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := f.repo.ProcessMessagesDue(f.ctx, 100); done <- e }()
	select {
	case <-f.adapter.entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 0 {
		t.Fatal("competing worker", n, e)
	}
	// These same rows are unlocked while the adapter is blocked. A separate
	// transaction can acquire them NOWAIT and an owner revocation can commit.
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		q    string
		args []any
	}{
		{`SELECT id FROM owned_authority_accounts WHERE id IN($1,$2,$3) FOR UPDATE NOWAIT`, []any{f.actor, f.other, f.bot.Account.ID}},
		{`SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, []any{f.app.PublisherID}},
		{`SELECT id FROM owned_custom_apps WHERE id=$1 FOR UPDATE NOWAIT`, []any{f.app.ID}},
		{`SELECT id FROM owned_bots WHERE id=$1 FOR UPDATE NOWAIT`, []any{f.bot.ID}},
		{`SELECT id FROM chat_rooms WHERE id=$1 FOR UPDATE NOWAIT`, []any{f.room.ID}},
		{`SELECT bot_id FROM owned_bot_enrollments WHERE bot_id=$1 FOR UPDATE NOWAIT`, []any{f.bot.ID}},
		{`SELECT id FROM chat_messages WHERE id=$1 FOR UPDATE NOWAIT`, []any{m.ID}},
		{`SELECT id FROM owned_bot_message_outbox WHERE message_id=$1 FOR UPDATE NOWAIT`, []any{m.ID}},
	} {
		if _, e = tx.ExecContext(f.ctx, statement.q, statement.args...); e != nil {
			t.Fatal("provider retained SQL locks", e)
		}
	}
	if _, e = tx.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	close(f.adapter.release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	// Revocation after dispatch observation does not retract an in-flight call.
	if state, n := botExecutionState(t, f, m.ID); state != "accepted" || n != 1 || len(f.adapter.delivered()) != 1 {
		t.Fatal(state, n)
	}

}
func TestPGBotExecutionRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "bot-execution-restart.json"
	var saved struct {
		Bot, Account, Actor, Room, Message, Event string
		Issuer, Audience, KeyID                   string
		PublicKey                                 []byte
		AccessTTL                                 time.Duration
		Process                                   int
		ServerStart                               time.Time
	}
	if phase == "before" {
		f := newBotExecutionFixture(t)
		m := botExecutionSend(t, f, "restart", "/ping persisted pending")
		saved.Bot, saved.Account, saved.Actor, saved.Room, saved.Message = f.bot.ID, f.bot.Account.ID, f.other, f.room.ID, m.ID
		if e := f.db.QueryRowContext(f.ctx, `SELECT id::text FROM owned_bot_message_outbox WHERE bot_id=$1 AND message_id=$2 AND state='pending'`, f.bot.ID, m.ID).Scan(&saved.Event); e != nil {
			t.Fatal(e)
		}
		saved.Process = os.Getpid()
		if e := f.db.QueryRowContext(f.ctx, `SELECT pg_postmaster_start_time()`).Scan(&saved.ServerStart); e != nil {
			t.Fatal(e)
		}
		saved.Issuer, saved.Audience, saved.KeyID, saved.AccessTTL = f.config.Issuer, f.config.Audience, f.config.SigningKeyID, f.config.AccessTTL
		var e error
		saved.PublicKey, e = x509.MarshalPKIXPublicKey(f.config.TrustedKeys[saved.KeyID])
		if e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, saved)
		return
	}
	if phase != "after" {
		t.Fatal("restart phase required")
	}
	loadRestartFixture(t, marker, &saved)
	var restarted time.Time
	if e := db.QueryRowContext(context.Background(), `SELECT pg_postmaster_start_time()`).Scan(&restarted); e != nil {
		t.Fatal(e)
	}
	if saved.Process <= 0 || saved.ServerStart.IsZero() || saved.Process == os.Getpid() || !restarted.After(saved.ServerStart) {
		t.Fatal("a genuine process and PostgreSQL server restart is required")
	}
	// A fresh verifier only reconciles persisted original sessions; no login,
	// token issuance, enrollment replacement or before-phase data reconstruction.
	key, e := x509.ParsePKIXPublicKey(saved.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	publicKey, ok := key.(*rsa.PublicKey)
	if !ok {
		t.Fatal("saved verification key type")
	}
	config := authority.Config{Issuer: saved.Issuer, Audience: saved.Audience, TrustedKeys: map[string]*rsa.PublicKey{saved.KeyID: publicKey}, AccessTTL: saved.AccessTTL, Now: time.Now}
	v, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	r := botpg.NewWithAuthority(db, guard)
	adapter := &syntheticBotDelivery{bot: saved.Bot}
	if e = r.WithExecution(botExecutionConfig(), adapter); e != nil {
		t.Fatal(e)
	}
	if e = r.ExecutionReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = r.ProcessMessagesDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	delivered := adapter.delivered()
	if len(delivered) != 1 || delivered[0].EventID != saved.Event || delivered[0].MessageID != saved.Message || delivered[0].Content != "/ping persisted pending" || delivered[0].BotAccountID != saved.Account || delivered[0].SenderAccountID != saved.Actor {
		t.Fatal("restart lost original delivery", delivered)
	}
	if n, e := r.ProcessMessagesDue(context.Background(), 100); e != nil || n != 0 || len(adapter.delivered()) != 1 {
		t.Fatal("restart acceptance replay", n, e)
	}
}

func TestPGBotExecutionFailureExpiryAndLateCommit(t *testing.T) {
	f := newBotExecutionFixture(t)
	m := botExecutionSend(t, f, "terminal", "/ping terminal failure")
	f.adapter.fail = true
	for i := 0; i < 3; i++ {
		botExecutionDue(t, f)
		if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); !errors.Is(e, bots.ErrUnavailable) {
			t.Fatal(e)
		}
	}
	if state, attempts := botExecutionState(t, f, m.ID); state != "failed" || attempts != 3 {
		t.Fatal("invented provider success", state, attempts)
	}
	f.adapter.fail = false
	expired := botExecutionSend(t, f, "expired", "/ping expired")
	query(t, f.db, `UPDATE owned_bot_message_outbox SET expires_at=clock_timestamp()-interval '1 second' WHERE message_id=$1`, expired.ID)
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if state, n := botExecutionState(t, f, expired.ID); state != "cancelled" || n != 0 {
		t.Fatal("expired provider attempt", state, n)
	}
	pending := botExecutionSend(t, f, "commit-retry", "/ping accepted but commit lost")
	// Simulate a durable adapter acknowledgement followed by a SQL acceptance
	// failure. Retry must carry the same event ID for adapter deduplication.
	constraint := "synthetic_commit_" + strings.ReplaceAll(authorityUUID(t), "-", "")
	query(t, f.db, `ALTER TABLE owned_bot_message_outbox ADD CONSTRAINT `+constraint+` CHECK(NOT(bot_id='`+f.bot.ID+`'::uuid AND state='accepted')) NOT VALID`)
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e == nil {
		t.Fatal("late SQL failure hidden")
	}
	query(t, f.db, `ALTER TABLE owned_bot_message_outbox DROP CONSTRAINT `+constraint)
	if state, n := botExecutionState(t, f, pending.ID); state != "pending" || n != 1 {
		t.Fatal("failed commit lost durable claim or retained acknowledgement", state, n)
	}
	// Failed acknowledgement must await lease expiry before reclaim.
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 0 {
		t.Fatal("live lease reclaimed", n, e)
	}
	query(t, f.db, `UPDATE owned_bot_message_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE message_id=$1`, pending.ID)
	if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	deliveries := f.adapter.delivered()
	if len(deliveries) != 5 || deliveries[3].EventID != deliveries[4].EventID {
		t.Fatal("late commit retry identity", deliveries)
	}
	// A missing deployment adapter keeps pending data and makes no send call.
	unresolved := botExecutionSend(t, f, "unconfigured", "/ping await deployment")
	if e := f.repo.WithExecution(botExecutionConfig()); e != nil {
		t.Fatal(e)
	}
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if state, n := botExecutionState(t, f, unresolved.ID); state != "pending" || n != 0 {
		t.Fatal("unconfigured delivery accepted", state, n)
	}
	// Cleanly revoke this synthetic pending binding for the remaining tests.
	if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, nil, "delete"); e != nil {
		t.Fatal(e)
	}
}

func TestPGBotExecutionMetadataAndCanonicalRollback(t *testing.T) {
	f := newBotExecutionFixture(t)
	key := "private-meta"
	message, e := f.chat.Send(f.otherCtx, f.other, f.room.ID, rooms.TextRequest{Content: "/ping public text", ClientMessageID: &key, Meta: json.RawMessage(`{"private":{"token":"synthetic secret"},"embeds":[{"url":"https://private.invalid"}],"replied_message_id":"synthetic private reference"}`)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	values := f.adapter.delivered()
	if len(values) != 1 || values[0].MessageID != message.ID || string(values[0].Meta) != "null" {
		t.Fatal("private metadata escaped", values)
	}
	// The observer cannot be invoked with an absent actor or unbound proof.
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = f.repo.QueueMessage(f.ctx, tx, "", f.room.ID, message.ID); !errors.Is(e, bots.ErrInvalid) {
		t.Fatal("absent actor", e)
	}
	if e = f.repo.QueueMessage(context.Background(), tx, f.other, f.room.ID, message.ID); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("absent proof", e)
	}
}

func TestPGBotExecutionLeaseReclaimAndStaleAcknowledgement(t *testing.T) {
	f := newBotExecutionFixture(t)
	message := botExecutionSend(t, f, "stale-worker", "/ping stable event")
	f.adapter.entered = make(chan struct{}, 1)
	f.adapter.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, e := f.repo.ProcessMessagesDue(f.ctx, 100); done <- e }()
	select {
	case <-f.adapter.entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	// Simulate a lost worker whose lease expires. A new repository uses the same
	// immutable adapter binding and recovers the original persisted event.
	query(t, f.db, `UPDATE owned_bot_message_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE message_id=$1`, message.ID)
	second := botpg.NewWithAuthority(f.db, f.guard)
	adapter := &syntheticBotDelivery{bot: f.bot.ID}
	if e := second.WithExecution(botExecutionConfig(), adapter); e != nil {
		t.Fatal(e)
	}
	if n, e := second.ProcessMessagesDue(f.ctx, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	close(f.adapter.release)
	if e := <-done; !errors.Is(e, bots.ErrUnavailable) {
		t.Fatal("stale acknowledgement accepted", e)
	}
	if state, attempts := botExecutionState(t, f, message.ID); state != "accepted" || attempts != 2 {
		t.Fatal(state, attempts)
	}
	one, two := f.adapter.delivered(), adapter.delivered()
	if len(one) != 1 || len(two) != 1 || one[0].EventID != two[0].EventID {
		t.Fatal("reclaim changed event identity")
	}
	a, _ := json.Marshal(one[0])
	b, _ := json.Marshal(two[0])
	if string(a) != string(b) {
		t.Fatal("reclaim changed payload")
	}
}

func TestPGBotExecutionTimeoutAndRevocationDuringDelivery(t *testing.T) {
	t.Run("late nil is ambiguous", func(t *testing.T) {
		f := newBotExecutionFixture(t)
		config := botExecutionConfig()
		config.SendTimeout = 20 * time.Millisecond
		f.adapter.hook = func(ctx context.Context, _ bots.MessageDelivery) error { <-ctx.Done(); return nil }
		if e := f.repo.WithExecution(config, f.adapter); e != nil {
			t.Fatal(e)
		}
		message := botExecutionSend(t, f, "timeout", "/ping timeout")
		if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); !errors.Is(e, bots.ErrUnavailable) {
			t.Fatal(e)
		}
		if state, n := botExecutionState(t, f, message.ID); state != "pending" || n != 1 {
			t.Fatal(state, n)
		}
	})
	t.Run("adapter callback revokes without locks", func(t *testing.T) {
		f := newBotExecutionFixture(t)
		f.adapter.hook = func(ctx context.Context, _ bots.MessageDelivery) error {
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, e := f.repo.Enrollment(bounded, f.scope, f.bot.ID, f.room.ID, nil, "delete")
			return e
		}
		message := botExecutionSend(t, f, "revoke", "/ping revocation")
		if _, e := f.repo.ProcessMessagesDue(f.ctx, 100); !errors.Is(e, bots.ErrUnavailable) {
			t.Fatal(e)
		}
		if state, n := botExecutionState(t, f, message.ID); state != "cancelled" || n != 1 {
			t.Fatal("acknowledgement overwrote revocation", state, n)
		}
	})
}

func TestPGBotExecutionEnrollmentRoleAndSessionScope(t *testing.T) {
	f := newBotExecutionFixture(t)
	// A developer editor who is not the room owner cannot enroll the bot.
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), f.app.PublisherID, f.other)
	other := f.scope
	other.Actor = f.other
	request := &bots.EnrollmentRequest{Adapter: f.adapter.Name(), ExpiresAt: time.Now().Add(10 * time.Minute)}
	if _, e := f.repo.Enrollment(f.otherCtx, other, f.bot.ID, f.room.ID, request, "set"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("editor bypassed room ownership", e)
	}
	// The owner is not allowed to enroll after losing developer editor status.
	query(t, f.db, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.actor)
	if _, e := f.repo.Enrollment(f.ctx, f.scope, f.bot.ID, f.room.ID, request, "set"); !errors.Is(e, bots.ErrForbidden) {
		t.Fatal("viewer bypassed editor role", e)
	}
	query(t, f.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1 AND account_id=$2`, f.app.PublisherID, f.actor)
	original, e := f.v.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(original.Session.CreatedAt.Unix())
	issuer, e := authority.New(authoritypg.New(f.db), authorityConfig(t, authorityKeys(t), now))
	if e != nil {
		t.Fatal(e)
	}
	for _, scopes := range [][]string{{bots.ExecutionPermission}, {bots.ExecutionPermission, "chat.invites.manage"}} {
		pair, e := issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "synthetic exact bot scope", Scopes: scopes})
		if e != nil {
			t.Fatal(e)
		}
		principal, e := f.v.Authenticate(f.ctx, pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		bound := f.guard.Bind(f.ctx, principal)
		_, e = f.repo.Enrollment(bound, f.scope, f.bot.ID, f.room.ID, request, "set")
		if len(scopes) == 1 && !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("scoped session lacks invite authority", e)
		}
		if len(scopes) == 2 && e != nil {
			t.Fatal("exact ordinary scoped session rejected", e)
		}
	}
}

func TestPGBotExecutionRechecksAfterContention(t *testing.T) {
	f := newBotExecutionFixture(t)
	message := botExecutionSend(t, f, "lock-contention", "/ping fresh proof")
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(f.ctx, `SELECT id FROM chat_rooms WHERE id=$1 FOR UPDATE`, f.room.ID); e != nil {
		t.Fatal(e)
	}
	if n, e := f.repo.ProcessMessagesDue(f.ctx, 100); e != nil || n != 0 || len(f.adapter.delivered()) != 0 {
		t.Fatal("contention sent message", n, e)
	}
	if _, e = tx.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProcessMessagesDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if state, n := botExecutionState(t, f, message.ID); state != "cancelled" || n != 0 || len(f.adapter.delivered()) != 0 {
		t.Fatal("cached proof survived contention", state, n)
	}
}
