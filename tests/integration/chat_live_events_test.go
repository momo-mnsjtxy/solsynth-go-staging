// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
)

type chatLiveFixture struct {
	chatMediaFixture
	events *notificationpg.Repository
	app    string
}

func newChatLive(t *testing.T) chatLiveFixture {
	t.Helper()
	f := chatLiveFixture{chatMediaFixture: newChatTransient(t), app: "live." + strings.ReplaceAll(authorityUUID(t), "-", "")}
	f.events = notificationpg.New(f.db, f.guard, f.app).WithRoomAccess(f.chat).WithChatEvents(f.chat)
	return f
}
func chatLiveProject(t *testing.T, f chatLiveFixture) {
	t.Helper()
	notificationDrain(t, func() (int, error) {
		return f.events.ProjectChatEvents(context.Background(), 100, int(chatPolicy.NoneEncryptionMode))
	})
}
func chatLivePage(t *testing.T, f chatLiveFixture, ctx context.Context, actor string, after int64) notifications.Page {
	t.Helper()
	p, e := f.events.Replay(ctx, actor, f.app, after, 100)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func chatLivePacket(t *testing.T, p notifications.Page, kind, id string) notifications.Event {
	t.Helper()
	for _, v := range p.Events {
		if v.Type == kind && bytes.Contains(v.Data, []byte(id)) {
			return v
		}
	}
	t.Fatalf("missing %s for %s: %#v", kind, id, p)
	return notifications.Event{}
}
func chatLiveNoPacket(t *testing.T, p notifications.Page, kind, id string) {
	t.Helper()
	for _, v := range p.Events {
		if v.Type == kind && bytes.Contains(v.Data, []byte(id)) {
			t.Fatalf("withdrawn packet leaked: %s", v.Data)
		}
	}
}
func TestPGChatLiveBroadcastRecipientsReplayAndInvalidations(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	msg, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "# source heading"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	notificationDrain(t, func() (int, error) {
		return f.events.ProjectChat(context.Background(), 100, int(chatPolicy.NoneEncryptionMode))
	})
	for _, viewer := range []struct {
		ctx   context.Context
		actor string
	}{{f.ctx, f.actor}, {f.otherCtx, f.other}} {
		p := chatLivePage(t, f, viewer.ctx, viewer.actor, 0)
		event := chatLivePacket(t, p, "messages.new", msg.ID)
		chatLivePacket(t, p, "owned.chat.changed", msg.ID)
		var got chatpg.LiveMessage
		if e = json.Unmarshal(event.Data, &got); e != nil || got.Sender.Account == nil || got.Sender.Account.ID != f.actor || got.Content == nil || *got.Content != "\\# source heading" {
			t.Fatal(got, e, string(event.Data))
		}
		if got.Sender.LastReadAt != nil || bytes.Contains(event.Data, []byte("recipient_member_id")) {
			t.Fatal("private stream state exposed", string(event.Data))
		}
		again := chatLivePage(t, f, viewer.ctx, viewer.actor, 0)
		againEvent := chatLivePacket(t, again, "messages.new", msg.ID)
		if againEvent.Sequence != event.Sequence || !bytes.Equal(againEvent.Data, event.Data) {
			t.Fatal("stable reconnect replay changed", event, againEvent)
		}
		if n := len(chatLivePage(t, f, viewer.ctx, viewer.actor, p.Cursor).Events); n != 0 {
			t.Fatal("cursor repeated delivered page", n)
		}
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.events WHERE app_id=$1 AND source_key=$2`, f.app, "chat-live-v1:"+msg.ID).Scan(&n); e != nil || n != 2 {
		t.Fatal("fanout did not deduplicate both recipients", n, e)
	}
	chatLiveProject(t, f)
	if e = f.db.QueryRow(`SELECT count(*) FROM notification_v1.events WHERE app_id=$1 AND source_key=$2`, f.app, "chat-live-v1:"+msg.ID).Scan(&n); e != nil || n != 2 {
		t.Fatal("repeat projector duplicated fanout", n, e)
	}
}
func TestPGChatLivePlaceholderProgressFinalizeAndExpiry(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	msg := transientPlaceholder(t, f.chatMediaFixture, room.ID, "streaming")
	chunk := "# streamed heading"
	if _, e := f.chat.UpdatePlaceholder(f.ctx, f.actor, rooms.PlaceholderUpdate{MessageID: msg.ID, ContentChunk: &chunk}); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	p := chatLivePage(t, f, f.otherCtx, f.other, 0)
	queued := chatLivePacket(t, p, "messages.placeholder.update", msg.ID)
	if _, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, rooms.PlaceholderFinalize{MessageID: msg.ID}); e != nil {
		t.Fatal(e)
	}
	if _, visible, e := f.events.RevalidateEvent(f.otherCtx, f.other, f.app, queued); e != nil || visible {
		t.Fatal("stale progress resurrected finalized placeholder", visible, e)
	}
	chatLiveProject(t, f)
	p = chatLivePage(t, f, f.otherCtx, f.other, p.Cursor)
	final := chatLivePacket(t, p, "messages.new", msg.ID)
	var got chatpg.LiveMessage
	if e := json.Unmarshal(final.Data, &got); e != nil || got.Type != "messages.sync.finalize" || got.Content == nil || *got.Content != "\\# streamed heading" {
		t.Fatal(got, e)
	}
	expired := transientPlaceholder(t, f.chatMediaFixture, room.ID, "uploading")
	chatLiveProject(t, f)
	p = chatLivePage(t, f, f.otherCtx, f.other, 0)
	old := chatLivePacket(t, p, "messages.new", expired.ID)
	query(t, f.db, `UPDATE chat_messages SET meta=jsonb_set(meta,'{placeholder_expires_at}',to_jsonb(1::bigint)) WHERE id=$1`, expired.ID)
	if _, visible, e := f.events.RevalidateEvent(f.otherCtx, f.other, f.app, old); e != nil || visible {
		t.Fatal("expired placeholder remained live before cleanup", visible, e)
	}
	if _, e := f.chat.ExpirePlaceholders(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	p = chatLivePage(t, f, f.otherCtx, f.other, 0)
	expiry := chatLivePacket(t, p, "messages.placeholder.expired", expired.ID)
	var fields map[string]json.RawMessage
	if json.Unmarshal(expiry.Data, &fields) != nil || len(fields) != 2 || fields["message_id"] == nil || fields["chat_room_id"] == nil {
		t.Fatal("expiry leaked DTO or membership", string(expiry.Data))
	}
}
func TestPGChatLiveRecipientIncarnationAndSessionRevocation(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	msg, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "membership private"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	p := chatLivePage(t, f, f.otherCtx, f.other, 0)
	queued := chatLivePacket(t, p, "messages.new", msg.ID)
	if _, e = f.chat.ChangeMember(f.otherCtx, f.other, room.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := f.events.RevalidateEvent(f.otherCtx, f.other, f.app, queued); e != nil || ok {
		t.Fatal("left member received queued packet", ok, e)
	}
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	after := chatLivePage(t, f, f.otherCtx, f.other, 0)
	chatLiveNoPacket(t, after, "messages.new", msg.ID)
	if after.Cursor != p.Cursor || len(after.Events) != 0 {
		t.Fatal("filtered cursor stalled", after, p)
	}
	fresh, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "new incarnation"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	chatLivePacket(t, chatLivePage(t, f, f.otherCtx, f.other, after.Cursor), "messages.new", fresh.ID)
	if e = f.issuer.Revoke(context.Background(), f.otherToken, "current"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.events.Replay(f.otherCtx, f.other, f.app, 0, 100); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked original proof replayed", e)
	}
}
func TestPGChatLiveDeletionAndVoiceExpiryWithdrawQueuedBodies(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	voice, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("live-voice"))
	if e != nil {
		t.Fatal(e)
	}
	text, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "remove me"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	p := chatLivePage(t, f, f.otherCtx, f.other, 0)
	voiceEvent := chatLivePacket(t, p, "messages.new", voice.ID)
	textEvent := chatLivePacket(t, p, "messages.new", text.ID)
	query(t, f.db, `UPDATE owned_chat_voice_clips SET expires_at=clock_timestamp()-interval '1 microsecond',created_at=clock_timestamp()-interval '1 hour' WHERE message_id=$1`, voice.ID)
	if _, ok, e := f.events.RevalidateEvent(f.otherCtx, f.other, f.app, voiceEvent); e != nil || ok {
		t.Fatal("expired voice clip leaked", ok, e)
	}
	if e = f.chat.DeleteMessage(f.ctx, f.actor, room.ID, text.ID); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := f.events.RevalidateEvent(f.otherCtx, f.other, f.app, textEvent); e != nil || ok {
		t.Fatal("deleted body leaked", ok, e)
	}
	chatLiveProject(t, f)
	deletion := chatLivePacket(t, chatLivePage(t, f, f.otherCtx, f.other, p.Cursor), "messages.new", text.ID)
	if bytes.Contains(deletion.Data, []byte("remove me")) {
		t.Fatal("delete packet retained hidden body", string(deletion.Data))
	}
}
func TestPGChatLiveMLSDeviceGenerationAndOpaquePayload(t *testing.T) {
	f := newChatLive(t)
	f.chat.WithMLSDelivery()
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	d1, d2 := "live-owner-"+f.actor, "live-other-"+f.other
	call := func(ctx context.Context, actor, device, op, target string, p rooms.MLSRequest) any {
		t.Helper()
		v, e := f.chat.MLS(ctx, actor, device, op, target, p)
		if e != nil {
			t.Fatal(op, e)
		}
		return v
	}
	call(f.ctx, f.actor, d1, "publish", "", rooms.MLSRequest{DeviceID: d1, KeyPackage: mlsBytes("live-owner")})
	call(f.otherCtx, f.other, d2, "publish", "", rooms.MLSRequest{DeviceID: d2, KeyPackage: mlsBytes("live-other")})
	call(f.ctx, f.actor, "", "enable", room.ID, rooms.MLSRequest{})
	group := "room:" + room.ID
	call(f.ctx, f.actor, d1, "bootstrap", group, rooms.MLSRequest{Epoch: mlsInt(0), StateVersion: mlsInt(1), Meta: map[string]string{"bootstrap_device_id": d1}})
	call(f.ctx, f.actor, d1, "groupinfo-put", group, rooms.MLSRequest{Epoch: mlsInt(0), GroupInfo: mlsBytes("info"), RatchetTree: mlsBytes("tree")})
	call(f.otherCtx, f.other, d2, "groupinfo", group, rooms.MLSRequest{})
	call(f.otherCtx, f.other, d2, "commit-fanout", group, mlsCommitReq(1, "live-join"))
	call(f.otherCtx, f.other, d2, "joined", room.ID, rooms.MLSRequest{Epoch: mlsInt(1)})
	cipher := mlsBytes("opaque live payload")
	msg, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{EncryptionMeta: &rooms.EncryptionMeta{Scheme: "chat.mls.v2", Ciphertext: cipher, Header: mlsBytes("h"), Epoch: 1}})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	chatLiveNoPacket(t, chatLivePage(t, f, f.otherCtx, f.other, 0), "messages.new", msg.ID)
	ctx := rooms.WithMLSWrite(f.otherCtx, rooms.MLSWriteContext{Device: d2})
	p := chatLivePage(t, f, ctx, f.other, 0)
	event := chatLivePacket(t, p, "messages.new", msg.ID)
	var got chatpg.LiveMessage
	if e = json.Unmarshal(event.Data, &got); e != nil || got.Content != nil || !bytes.Contains(got.EncryptionMeta, []byte(cipher)) || got.ForwardedMessage != nil || string(got.Meta) != "null" {
		t.Fatal("opaque payload was transformed or previewed", got, e)
	}
	wrong := rooms.WithMLSWrite(f.otherCtx, rooms.MLSWriteContext{Device: d1})
	chatLiveNoPacket(t, chatLivePage(t, f, wrong, f.other, 0), "messages.new", msg.ID)
	query(t, f.db, `UPDATE owned_mls_groups SET state_version=state_version+1 WHERE id=$1`, group)
	if _, ok, e := f.events.RevalidateEvent(ctx, f.other, f.app, event); e != nil || ok {
		t.Fatal("MLS reset revived old ciphertext", ok, e)
	}
	query(t, f.db, `UPDATE owned_mls_groups SET state_version=state_version-1 WHERE id=$1`, group)
	call(f.otherCtx, f.other, d2, "revoke", d2, rooms.MLSRequest{})
	if _, ok, e := f.events.RevalidateEvent(ctx, f.other, f.app, event); e != nil || ok {
		t.Fatal("revoked MLS device received ciphertext", ok, e)
	}
	// Account punishments are independently enforced on the original proof.
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.other, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.events.Replay(ctx, f.other, f.app, 0, 100); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("punished reader replayed", e)
	}
}

// The real SQL read is deliberately paused after canonical hydration. Source
// expiry advances independently of row locks; the final observation must remove
// the prepared payload without committing a delivery acknowledgement.
type chatLiveHydrationWait struct {
	chatpg.MessageAssociations
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *chatLiveHydrationWait) Load(ctx context.Context, tx *sql.Tx, actor string, v *messager.MessageProjection) error {
	if e := w.MessageAssociations.Load(ctx, tx, actor, v); e != nil {
		return e
	}
	var e error
	w.once.Do(func() {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			e = ctx.Err()
		}
	})
	return e
}
func TestPGChatLiveExpiryAfterHydrationWait(t *testing.T) {
	for _, kind := range []string{"placeholder", "voice"} {
		t.Run(kind, func(t *testing.T) {
			f := newChatLive(t)
			room := chatMediaRoom(t, f.chatMediaFixture, false)
			chatMediaJoin(t, f.chatMediaFixture, room.ID)
			var msg messager.MessageProjection
			var e error
			if kind == "placeholder" {
				msg = transientPlaceholder(t, f.chatMediaFixture, room.ID, "streaming")
			} else {
				msg, e = f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("wait-expiry"))
				if e != nil {
					t.Fatal(e)
				}
			}
			chatLiveProject(t, f)
			before := chatLivePage(t, f, f.otherCtx, f.other, 0)
			chatLivePacket(t, before, "messages.new", msg.ID)
			var deadline time.Time
			if kind == "placeholder" {
				if e = f.db.QueryRow(`UPDATE chat_messages SET meta=jsonb_set(meta,'{placeholder_expires_at}',to_jsonb(floor(extract(epoch FROM clock_timestamp()+interval '1 second')*1000)::bigint)) WHERE id=$1 RETURNING to_timestamp((meta->>'placeholder_expires_at')::numeric/1000)`, msg.ID).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = f.db.QueryRow(`UPDATE owned_chat_voice_clips SET expires_at=clock_timestamp()+interval '1 second' WHERE message_id=$1 RETURNING expires_at`, msg.ID).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			}
			wait := &chatLiveHydrationWait{MessageAssociations: f.links, entered: make(chan struct{}), release: make(chan struct{})}
			f.chat.WithAssociations(wait)
			ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
			defer cancel()
			type result struct {
				page notifications.Page
				err  error
			}
			done := make(chan result, 1)
			go func() { p, e := f.events.Replay(ctx, f.other, f.app, 0, 100); done <- result{p, e} }()
			select {
			case <-wait.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			organizationWaitExpired(t, ctx, f.db, deadline)
			close(wait.release)
			got := <-done
			if got.err != nil || len(got.page.Events) != 0 || got.page.Cursor != before.Cursor {
				t.Fatal("expired payload survived hydration wait", kind, got.page, got.err)
			}
		})
	}
}

func TestPGChatLiveWebSocketMultipleRecipientsAndReconnect(t *testing.T) {
	// This database guard executes before any socket. Actual transport execution
	// belongs only to the disposable process/PostgreSQL CI coordinator.
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	secondToken := pgIssue(t, f.issuer, f.other).Token
	nf := notificationFixture{db: f.db, repo: f.events, guard: f.guard, v: f.verifier, issuer: f.issuer, actor: f.actor, other: f.other, token: f.token, otherToken: f.otherToken, app: f.app, ctx: f.ctx, otherCtx: f.otherCtx}
	h, e := realtime.New(f.events, notificationIdentity{f.verifier}, realtime.Config{App: f.app, Connections: chatpg.NewOrganization(f.chat, f.reader, nil), PollInterval: 20 * time.Millisecond, WriteTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(nf.middleware(h))
	defer server.Close()
	dial := func(token string, after int64) *websocket.Conn {
		t.Helper()
		c, response, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?namespace="+f.app+"&after="+strconv.FormatInt(after, 10), http.Header{"Authorization": []string{"Bearer " + token}})
		if e != nil {
			t.Fatal(response, e)
		}
		return c
	}
	first, second, otherDevice := dial(f.token, 0), dial(f.otherToken, 0), dial(secondToken, 0)
	defer first.Close()
	defer second.Close()
	defer otherDevice.Close()
	read := func(c *websocket.Conn, id string) int64 {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var p struct {
			Type     string          `json:"type"`
			Data     json.RawMessage `json:"data"`
			Sequence int64           `json:"sequence"`
		}
		if e := c.ReadJSON(&p); e != nil || p.Type != "messages.new" || !bytes.Contains(p.Data, []byte(id)) || p.Sequence <= 0 {
			t.Fatal("recipient live packet", p, e)
		}
		return p.Sequence
	}
	msg, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "three live connections"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	read(first, msg.ID)
	cursor := read(second, msg.ID)
	if otherCursor := read(otherDevice, msg.ID); otherCursor != cursor {
		t.Fatal("same account has inconsistent durable cursor", cursor, otherCursor)
	}
	_ = second.Close()
	next, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "reconnect replay"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	second = dial(f.otherToken, cursor)
	defer second.Close()
	read(second, next.ID)
	read(first, next.ID)
	read(otherDevice, next.ID)
	if e = f.issuer.Revoke(context.Background(), f.otherToken, "current"); e != nil {
		t.Fatal(e)
	}
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, e = second.ReadMessage(); e == nil {
		t.Fatal("revoked recipient connection stayed open")
	}
	final, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "independent session continues"})
	if e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	read(first, final.ID)
	read(otherDevice, final.ID)
}

func TestPGChatReadReceiptDurableFanoutAndSubscriptionWithdrawal(t *testing.T) {
	f := newChatLive(t)
	name, publicReceipts := "Public receipts", true
	room, e := f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name, ReadReceiptsPublic: &publicReceipts})
	if e != nil {
		t.Fatal(e)
	}
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	org := chatpg.NewOrganization(f.chat, f.reader, nil)
	ownerConnection, e := org.OpenConnection(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	if e = org.SetSubscription(f.ctx, f.actor, ownerConnection, room.ID, true); e != nil {
		t.Fatal(e)
	}
	message, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "read this"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.MarkRead(f.otherCtx, f.other, room.ID, message.ID); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	ownerPage := chatLivePage(t, f, f.ctx, f.actor, 0)
	receipt := chatLivePacket(t, ownerPage, "messages.read", f.other)
	if bytes.Contains(receipt.Data, []byte("excluded_device_id")) || !bytes.Contains(receipt.Data, []byte(room.ID)) {
		t.Fatal("private receipt reference leaked", string(receipt.Data))
	}
	chatLivePacket(t, chatLivePage(t, f, f.otherCtx, f.other, 0), "messages.read", f.other)
	if e = org.SetSubscription(f.ctx, f.actor, ownerConnection, room.ID, false); e != nil {
		t.Fatal(e)
	}
	next, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "withdraw fanout"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.MarkRead(f.otherCtx, f.other, room.ID, next.ID); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	chatLiveNoPacket(t, chatLivePage(t, f, f.ctx, f.actor, ownerPage.Cursor), "messages.read", f.other)
}

func TestPGChatReadReceiptIsNotStarvedByNewerMessageBacklog(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	first, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "read boundary"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.MarkRead(f.otherCtx, f.other, room.ID, first.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "newer backlog"}); e != nil {
		t.Fatal(e)
	}
	if n, e := f.events.ProjectChatEvents(context.Background(), 2, int(chatPolicy.NoneEncryptionMode)); e != nil || n != 2 {
		t.Fatal("bounded projection", n, e)
	}
	state, e := f.events.InspectChatEvents(context.Background(), int(chatPolicy.NoneEncryptionMode))
	if e != nil || state.PendingReads != 0 || state.PendingMessages != 1 {
		t.Fatal("read event starved behind a newer message", state, e)
	}
}

func TestPGChatLiveInspectionRedriveAndContiguousRetentionFloor(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	message, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "redrive me"})
	if e != nil {
		t.Fatal(e)
	}
	before, e := f.events.InspectChatEvents(context.Background(), int(chatPolicy.NoneEncryptionMode))
	if e != nil || before.PendingMessages == 0 {
		t.Fatal(before, e)
	}
	chatLiveProject(t, f)
	projected, e := f.events.InspectChatEvents(context.Background(), int(chatPolicy.NoneEncryptionMode))
	if e != nil || projected.PendingMessages != 0 || projected.StreamReferences < 2 {
		t.Fatal(projected, e)
	}
	old := chatLivePacket(t, chatLivePage(t, f, f.otherCtx, f.other, 0), "messages.new", message.ID)
	if e = f.events.RedriveChatEvent(context.Background(), "message", message.ID); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	fresh := chatLivePacket(t, chatLivePage(t, f, f.otherCtx, f.other, old.Sequence), "messages.new", message.ID)
	if fresh.Sequence <= old.Sequence {
		t.Fatal("redrive did not allocate new sequence", old.Sequence, fresh.Sequence)
	}
	purged := 0
	for range 10 {
		n, e := f.events.PurgeStreamEvents(context.Background(), time.Now().UTC().Add(time.Hour), 100)
		if e != nil {
			t.Fatal(e)
		}
		purged += n
		if n == 0 {
			break
		}
	}
	if purged == 0 {
		t.Fatal("no durable events purged")
	}
	if _, e = f.events.Replay(f.otherCtx, f.other, f.app, 0, 100); !errors.Is(e, notifications.ErrCursor) {
		t.Fatal("old cursor did not fail after floor advance", e)
	} else if cursor, ok := notifications.RecoverableCursor(e); !ok || cursor <= 0 {
		t.Fatal("stale cursor omitted recoverable floor", cursor, ok, e)
	}
	head, e := f.events.Replay(f.otherCtx, f.other, f.app, notifications.HeadCursor, 100)
	if e != nil || len(head.Events) != 0 || head.Cursor != head.HighWater {
		t.Fatal("omitted-cursor head reset failed after retention", head, e)
	}
}

func TestPGChatLiveRetentionLocksAccountBeforeStreamAndRechecksPrefix(t *testing.T) {
	f := newChatLive(t)
	room := chatMediaRoom(t, f.chatMediaFixture, false)
	chatMediaJoin(t, f.chatMediaFixture, room.ID)
	if _, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "retention lock order"}); e != nil {
		t.Fatal(e)
	}
	chatLiveProject(t, f)
	before := time.Now().UTC()
	var account string
	if e := f.db.QueryRow(`SELECT s.account_id::text FROM notification_v1.streams s WHERE s.app_id=$1 AND EXISTS(SELECT 1 FROM notification_v1.events e WHERE e.account_id=s.account_id AND e.app_id=s.app_id AND e.sequence>s.floor AND e.created_at<$2) ORDER BY s.account_id LIMIT 1`, f.app, before).Scan(&account); e != nil {
		t.Fatal(e)
	}
	blocker, e := f.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var blockerPID int
	if e = blocker.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, account); e != nil {
		t.Fatal(e)
	}
	type purgeResult struct {
		n   int
		err error
	}
	done := make(chan purgeResult, 1)
	go func() {
		n, err := f.events.PurgeStreamEvents(context.Background(), before, 100)
		done <- purgeResult{n: n, err: err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var waiting bool
		if e = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position('owned_authority_accounts' in query)>0)`, blockerPID).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case got := <-done:
			t.Fatal("retention did not wait for canonical account lock", got)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Change the discovered prefix while retention is waiting. A stale prefix
	// decision would delete this now-ineligible first sequence after the wait.
	if _, e = f.db.ExecContext(ctx, `UPDATE notification_v1.events SET created_at=$3 WHERE account_id=$1 AND app_id=$2 AND sequence=(SELECT min(sequence) FROM notification_v1.events WHERE account_id=$1 AND app_id=$2)`, account, f.app, before.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case got := <-done:
		if got.err != nil || got.n != 0 {
			t.Fatal("stale retention prefix committed", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var floor int64
	var retained int
	if e = f.db.QueryRow(`SELECT s.floor,(SELECT count(*) FROM notification_v1.events e WHERE e.account_id=s.account_id AND e.app_id=s.app_id) FROM notification_v1.streams s WHERE s.account_id=$1 AND s.app_id=$2`, account, f.app).Scan(&floor, &retained); e != nil || floor != 0 || retained == 0 {
		t.Fatal("retention advanced across changed prefix", floor, retained, e)
	}
}
