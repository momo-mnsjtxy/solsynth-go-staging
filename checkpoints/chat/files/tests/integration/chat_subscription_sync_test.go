// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"reflect"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPGChatGlobalSyncStableMillisecondCursor(t *testing.T) {
	f := newOrganizationFixture(t)
	room := threadRoom(t, f.chatRelationsFixture, false, f.other)
	member, e := f.chats.Me(f.contexts[f.owner], f.owner, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	// Reversed microsecond offsets force timestamp+UUID ordering to differ from
	// the required millisecond+UUID client cursor ordering.
	query(t, f.db, `INSERT INTO chat_messages(id,chat_room_id,sender_id,type,room_sequence,content,created_at,updated_at)SELECT md5($1::text||g::text)::uuid,$1,$2,'text',g,'synthetic tie',$3::timestamptz+(600-g)*interval '1 microsecond',$3 FROM generate_series(1,503)g`, room.ID, member.ID, at)
	page, e := f.org.GlobalSync(f.contexts[f.other], f.other, rooms.GlobalSyncRequest{LastSyncTimestamp: at.Add(-time.Millisecond).UnixMilli()})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Messages) != 500 || page.TotalCount != 500 || page.CurrentMessageID == nil || page.CurrentTimestamp.UnixMilli() != at.UnixMilli() {
		t.Fatal("first page", len(page.Messages), page, e)
	}
	next, e := f.org.GlobalSync(f.contexts[f.other], f.other, rooms.GlobalSyncRequest{LastSyncTimestamp: page.CurrentTimestamp.UnixMilli(), LastSyncMessageID: page.CurrentMessageID})
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Messages) != 3 {
		t.Fatal("tie page", len(next.Messages))
	}
	ids := []string{}
	for _, m := range append(page.Messages, next.Messages...) {
		if m.ChatRoomID != room.ID {
			t.Fatal("private leak", m)
		}
		ids = append(ids, m.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("cursor order differs")
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatal("duplicate", ids[i])
		}
	}
	outsider, e := f.org.GlobalSync(f.contexts[f.outsider], f.outsider, rooms.GlobalSyncRequest{})
	if e != nil || len(outsider.Messages) != 0 {
		t.Fatal("outsider", outsider, e)
	}
	if _, e = f.org.GlobalSync(context.Background(), f.other, rooms.GlobalSyncRequest{}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("missing original proof", e)
	}
}
func TestPGChatJoinedRoomSyncBucketsAndRemovedPrivacy(t *testing.T) {
	f := newOrganizationFixture(t)
	actor := f.outsider
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	// 501 rooms in one timestamp bucket must be delivered together.
	query(t, f.db, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name,owned_created_at,owned_updated_at)SELECT md5($1::text||'room'||g::text)::uuid,false,77,$1,'synthetic sync room',$2,$2 FROM generate_series(1,501)g`, actor, at)
	query(t, f.db, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,owned_updated_at)SELECT md5($1::text||'member'||g::text)::uuid,md5($1::text||'room'||g::text)::uuid,$1,10,$2,$2 FROM generate_series(1,501)g`, actor, at)
	v, e := f.org.JoinedRoomSync(f.contexts[actor], actor, rooms.JoinedRoomSyncRequest{LastSyncTimestamp: at.Add(-time.Millisecond).UnixMilli()})
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Changes) != 501 || len(v.Summaries) != 501 || v.CurrentTimestamp.UnixMilli() != at.UnixMilli() {
		t.Fatal("bucket lost", len(v.Changes), len(v.Summaries), v.CurrentTimestamp)
	}
	removed := v.Changes[0].RoomID
	query(t, f.db, `UPDATE chat_members SET leave_at=$3,owned_updated_at=$3 WHERE chat_room_id=$1 AND account_id=$2`, removed, actor, at.Add(time.Second))
	next, e := f.org.JoinedRoomSync(f.contexts[actor], actor, rooms.JoinedRoomSyncRequest{LastSyncTimestamp: v.CurrentTimestamp.UnixMilli()})
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Changes) != 1 || next.Changes[0].Type != "removed" || next.Changes[0].RoomID != removed || next.Changes[0].Room != nil || next.Changes[0].Member != nil {
		t.Fatal("removed metadata", next)
	}
}
func chatSyncProof(t *testing.T, f organizationFixture, token string) context.Context {
	t.Helper()
	p, e := f.verifier.Authenticate(f.base, token)
	if e != nil {
		t.Fatal(e)
	}
	return f.guard.Bind(f.base, p)
}
func TestPGChatSubscriptionsSessionContinuityAndPrivacy(t *testing.T) {
	f := newOrganizationFixture(t)
	room := threadRoom(t, f.chatRelationsFixture, false, f.other)
	pair := pgIssue(t, f.issuer, f.owner)
	ctx := chatSyncProof(t, f, pair.Token)
	conn, e := f.org.OpenConnection(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.org.SetSubscription(ctx, f.owner, conn, room.ID, true); e != nil {
		t.Fatal(e)
	}
	otherConn, e := f.org.OpenConnection(f.contexts[f.other], f.other)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.org.SetSubscription(f.contexts[f.other], f.other, otherConn, room.ID, true); e != nil {
		t.Fatal(e)
	}
	v, e := f.org.RoomSubscriptions(f.contexts[f.other], f.other, room.ID)
	if e != nil || len(v) != 2 {
		t.Fatal(v, e)
	}
	for _, s := range v {
		if s.AccountID == f.owner && (len(s.Devices) != 0 || s.Member.LastReadAt != nil) {
			t.Fatal("other-device leak", s)
		}
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), conn) {
		t.Fatal("connection handle leaked", string(b))
	}
	if _, e = f.org.RoomSubscriptions(f.contexts[f.outsider], f.outsider, room.ID); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("outsider", e)
	}
	if e = f.org.RenewConnection(f.contexts[f.owner], f.owner, conn); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("cross-session renewal", e)
	}
	rotated, e := f.issuer.Refresh(f.base, pair.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	fresh := chatSyncProof(t, f, rotated.Token)
	if e = f.org.RenewConnection(fresh, f.owner, conn); e != nil {
		t.Fatal("refresh lost lease", e)
	}
	status, e := f.org.ChatStatus(f.contexts[f.owner], f.owner)
	if e != nil || !status.HasAnyWebSocketConnection || !status.HasActiveSubscriptions || len(status.Subscriptions) != 1 {
		t.Fatal(status, e)
	}
	if e = f.org.CloseConnection(fresh, f.owner, conn); e != nil {
		t.Fatal(e)
	}
	status, e = f.org.ChatStatus(f.contexts[f.owner], f.owner)
	if e != nil || status.HasAnyWebSocketConnection || !status.HasActiveSubscriptions || status.Subscriptions[0].Devices[0].IsWebSocketConnected {
		t.Fatal("close conflated subscription", status, e)
	}
	if e = f.issuer.Revoke(f.base, rotated.Token, "current"); e != nil {
		t.Fatal(e)
	}
	status, e = f.org.ChatStatus(f.contexts[f.owner], f.owner)
	if e != nil || status.HasActiveSubscriptions {
		t.Fatal("revoked subscription survived", status, e)
	}
	if e = f.org.RenewConnection(fresh, f.owner, conn); e == nil {
		t.Fatal("revoked proof renewed")
	}
	// Member incarnation prevents old leases reviving when the same row rejoins.
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, room.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.owner], f.owner, room.ID, rooms.Invite, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chats.ChangeMember(f.contexts[f.other], f.other, room.ID, rooms.Accept, ""); e != nil {
		t.Fatal(e)
	}
	subs, e := f.org.AccountSubscriptions(f.contexts[f.other], f.other)
	if e != nil || len(subs) != 0 {
		t.Fatal("old incarnation revived", subs, e)
	}
}
func TestPGChatSubscriptionAuthorizationGenerationAndExpiry(t *testing.T) {
	f := newOrganizationFixture(t)
	room := threadRoom(t, f.chatRelationsFixture, false)
	pair := pgIssue(t, f.issuer, f.owner)
	ctx := chatSyncProof(t, f, pair.Token)
	conn, e := f.org.OpenConnection(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.org.SetSubscription(ctx, f.owner, conn, room.ID, true); e != nil {
		t.Fatal(e)
	}
	p, e := f.verifier.Authenticate(f.base, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET epoch=epoch+2,authorization_epoch=authorization_epoch+2 WHERE id=$1`, p.Session.ID)
	v, e := f.org.AccountSubscriptions(f.contexts[f.owner], f.owner)
	if e != nil || len(v) != 0 {
		t.Fatal("restored authorization resurrected admission", v, e)
	}
	conn, e = f.org.OpenConnection(f.contexts[f.owner], f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.org.SetSubscription(f.contexts[f.owner], f.owner, conn, room.ID, true); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_chat_subscriptions SET expires_at=clock_timestamp()-interval '1 second' WHERE connection_id=$1`, conn)
	v, e = f.org.AccountSubscriptions(f.contexts[f.owner], f.owner)
	if e != nil || len(v) != 0 {
		t.Fatal("expired lease", v, e)
	}
	query(t, f.db, `UPDATE owned_chat_connections SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, conn)
	if e = f.org.RenewConnection(f.contexts[f.owner], f.owner, conn); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("expired connection revived", e)
	}
}
func TestPGChatSubscriptionSyncRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Room, Connection, Token string
		Authority                      restartAuthoritySnapshot
		Subscriptions                  []rooms.AccountSubscription
		Global                         rooms.GlobalSyncResponse
	}
	var saved record
	if phase == "before" {
		f, c, token := organizationRestartAuthority(t, newOrganizationFixture(t))
		room := threadRoom(t, f.chatRelationsFixture, false)
		relationSend(t, f.chatRelationsFixture, f.owner, room.ID, "Persisted sync message")
		query(t, f.db, `UPDATE chat_messages SET created_at=clock_timestamp()-interval '1 second',updated_at=clock_timestamp()-interval '1 second' WHERE chat_room_id=$1`, room.ID)
		conn, e := f.org.OpenConnection(f.contexts[f.owner], f.owner)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.org.SetSubscription(f.contexts[f.owner], f.owner, conn, room.ID, true); e != nil {
			t.Fatal(e)
		}
		// Explicit synthetic extension keeps this fixture meaningful even when the
		// coordinator's full pre-restart suite exceeds ordinary 60s/5m lease TTLs.
		query(t, f.db, `UPDATE owned_chat_connections SET expires_at=clock_timestamp()+interval '30 minutes' WHERE id=$1`, conn)
		query(t, f.db, `UPDATE owned_chat_subscriptions SET expires_at=clock_timestamp()+interval '30 minutes' WHERE connection_id=$1`, conn)
		subs, e := f.org.AccountSubscriptions(f.contexts[f.owner], f.owner)
		if e != nil {
			t.Fatal(e)
		}
		global, e := f.org.GlobalSync(f.contexts[f.owner], f.owner, rooms.GlobalSyncRequest{})
		if e != nil {
			t.Fatal(e)
		}
		saved = record{Actor: f.owner, Room: room.ID, Connection: conn, Token: token, Authority: saveAuthoritySnapshot(t, c), Subscriptions: subs, Global: global}
		saveRestartFixture(t, "chat-subscription-sync.json", saved)
		return
	}
	loadRestartFixture(t, "chat-subscription-sync.json", &saved)
	c := restoreAuthoritySnapshot(t, saved.Authority)
	store := authoritypg.New(db)
	verifier, e := authority.NewVerifier(store, c)
	if e != nil {
		t.Fatal(e)
	}
	base, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proof, e := verifier.Authenticate(base, saved.Token)
	if e != nil {
		t.Fatal("original saved proof", e)
	}
	guard := authoritypg.NewTransactionGuard(store, verifier)
	ctx := guard.Bind(base, proof)
	chat, e := chatpg.NewWithAuthority(db, chatPolicy, guard)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.NewWithAuthority(db, guard))
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat)
	org := chatpg.NewOrganization(chat, reader, nil)
	subs, e := org.AccountSubscriptions(ctx, saved.Actor)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(subs)
	b, _ := json.Marshal(saved.Subscriptions)
	if string(a) != string(b) {
		t.Fatal("saved subscriptions changed", string(a), string(b))
	}
	global, e := org.GlobalSync(ctx, saved.Actor, rooms.GlobalSyncRequest{})
	if e != nil || !reflect.DeepEqual(global, saved.Global) {
		t.Fatal("saved global page", global, e)
	}
	device, e := org.CurrentConnectionDevice(ctx, saved.Actor, saved.Connection)
	if e != nil || device != "" {
		t.Fatal(device, e)
	}
	if e = org.RenewConnection(ctx, saved.Actor, saved.Connection); e != nil {
		t.Fatal("same pre-restart connection proof", e)
	}
	if _, e = org.AccountSubscriptions(context.Background(), saved.Actor); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("proof-free restart access", e)
	}
	expired := c
	expired.Now = func() time.Time { return saved.Authority.Now.Add(2 * time.Hour) }
	expiredVerifier, e := authority.NewVerifier(store, expired)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = expiredVerifier.Authenticate(base, saved.Token); e == nil {
		t.Fatal("expired original proof accepted")
	}
}
func TestPGBackendChatSubscriptionSyncRoutes(t *testing.T) {
	f := newBackendFixture(t)
	for _, p := range []string{"/api/chat/accounts/me/subscriptions", "/messager/chat/accounts/me/subscriptions", "/messager/chat/accounts/me/status"} {
		backendCall(t, f.app, "GET", p, f.token, "", 200)
		backendCall(t, f.app, "GET", p, "", "", 401)
	}
	for _, p := range []string{"/api/chat/sync", "/messager/chat/sync", "/api/chat/rooms/sync", "/messager/chat/rooms/sync"} {
		backendCall(t, f.app, "POST", p, f.token, `{"last_sync_timestamp":0}`, 200)
	}
}

func TestPGBackendWebSocketSubscriptionCommandsPersistPerConnection(t *testing.T) {
	f := newBackendFixture(t)
	room := backendDecode[rooms.Room](t, backendCall(t, f.app, "POST", "/messager/chat", f.token, `{"name":"WebSocket subscriptions"}`, 200))
	server := httptest.NewServer(f.app)
	defer server.Close()
	header := http.Header{"Authorization": []string{"Bearer " + f.token}}
	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
		if e != nil {
			t.Fatal(e)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		return conn
	}
	first, second := dial(), dial()
	defer first.Close()
	defer second.Close()
	command := func(conn *websocket.Conn, kind string) {
		t.Helper()
		if e := conn.WriteJSON(map[string]any{"type": kind, "endpoint": "DysonNetwork.Messager", "data": map[string]any{"chat_room_id": room.ID}}); e != nil {
			t.Fatal(e)
		}
		var reply struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if e := conn.ReadJSON(&reply); e != nil || reply.Type != kind || string(reply.Data) != "null" {
			t.Fatal(reply, e)
		}
	}
	command(first, "messages.subscribe")
	command(second, "messages.subscribe")
	var subscriptions int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_chat_subscriptions s JOIN owned_chat_connections c ON c.id=s.connection_id WHERE c.account_id=$1 AND s.member_id=(SELECT id FROM chat_members WHERE account_id=$1 AND chat_room_id=$2)`, f.actor, room.ID).Scan(&subscriptions); e != nil || subscriptions != 2 {
		t.Fatal("two persisted connections", subscriptions, e)
	}
	status := backendCall(t, f.app, "GET", "/messager/chat/accounts/me/status", f.token, "", 200)
	if !strings.Contains(status.Body.String(), fmt.Sprintf(`"room_id":"%s"`, room.ID)) {
		t.Fatal("status omitted subscription", status.Body.String())
	}
	command(first, "messages.unsubscribe")
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_chat_subscriptions s JOIN owned_chat_connections c ON c.id=s.connection_id WHERE c.account_id=$1 AND s.member_id=(SELECT id FROM chat_members WHERE account_id=$1 AND chat_room_id=$2)`, f.actor, room.ID).Scan(&subscriptions); e != nil || subscriptions != 1 {
		t.Fatal("unsubscribe removed other device", subscriptions, e)
	}
	status = backendCall(t, f.app, "GET", "/messager/chat/accounts/me/status", f.token, "", 200)
	if !strings.Contains(status.Body.String(), fmt.Sprintf(`"room_id":"%s"`, room.ID)) {
		t.Fatal("other device subscription vanished", status.Body.String())
	}
}

func TestPGChatSyncWaitRechecksOriginalProof(t *testing.T) {
	for _, mode := range []string{"global", "joined", "status", "renew"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrganizationFixture(t)
			room := threadRoom(t, f.chatRelationsFixture, false)
			relationSend(t, f.chatRelationsFixture, f.owner, room.ID, "Private wait payload")
			conn, e := f.org.OpenConnection(f.contexts[f.owner], f.owner)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.contexts[f.owner], 12*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.owner)
			proof, e := f.verifier.Authenticate(f.base, f.token)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1,authorization_epoch=authorization_epoch+1 WHERE id=$1`, proof.Session.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				var e error
				switch mode {
				case "global":
					_, e = f.org.GlobalSync(ctx, f.owner, rooms.GlobalSyncRequest{})
				case "joined":
					_, e = f.org.JoinedRoomSync(ctx, f.owner, rooms.JoinedRoomSyncRequest{})
				case "status":
					_, e = f.org.ChatStatus(ctx, f.owner)
				case "renew":
					e = f.org.RenewConnection(ctx, f.owner, conn)
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, authority.ErrRevoked) {
				t.Fatal("stale proof after account wait", mode, e)
			}
		})
	}
}
func TestPGChatJoinedRoomSyncExplicitOverflow(t *testing.T) {
	f := newOrganizationFixture(t)
	actor := f.outsider
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	query(t, f.db, `INSERT INTO chat_rooms(id,is_public,encryption_mode,owned_owner_id,owned_name,owned_created_at,owned_updated_at)SELECT md5($1::text||'history'||g::text)::uuid,false,77,$1,'synthetic removed room',$2,$2 FROM generate_series(1,10001)g`, actor, at)
	query(t, f.db, `INSERT INTO chat_members(id,chat_room_id,account_id,notify,joined_at,leave_at,owned_updated_at)SELECT md5($1::text||'historical member'||g::text)::uuid,md5($1::text||'history'||g::text)::uuid,$1,10,$2::timestamptz-interval '1 second',$2,$2 FROM generate_series(1,10001)g`, actor, at)
	out, e := f.org.JoinedRoomSync(f.contexts[actor], actor, rooms.JoinedRoomSyncRequest{LastSyncTimestamp: at.Add(-time.Millisecond).UnixMilli()})
	if !errors.Is(e, rooms.ErrSyncBucketOverflow) || !out.CurrentTimestamp.IsZero() || len(out.Changes) != 0 {
		t.Fatal("overflow advanced", out, e)
	}
}

func TestPGChatConnectionDelegatedBindingIsolation(t *testing.T) {
	f := newOAuthFixture(t, false)
	query(t, f.db, chatpg.Schema)
	query(t, f.db, chatpg.OrganizationSchema)
	chat, e := chatpg.NewWithAuthority(f.db, chatPolicy, f.transactionGuard)
	if e != nil {
		t.Fatal(e)
	}
	org := chatpg.NewOrganization(chat, nil, nil)
	tokens := oauthExchange(t, f)
	delegated := oauthBound(t, f, tokens.AccessToken)
	conn, e := org.OpenConnection(delegated, f.subject)
	if e != nil {
		t.Fatal("bound delegated connection", e)
	}
	if e = org.RenewConnection(delegated, f.subject, conn); e != nil {
		t.Fatal(e)
	}
	if _, e = org.CurrentConnectionDevice(f.ctx, f.subject, conn); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("root reused delegated lease", e)
	}
	status, e := org.ChatStatus(f.ctx, f.subject)
	if e != nil || !status.HasAnyWebSocketConnection {
		t.Fatal(status, e)
	}
	// Existing resource admission remains Type-0 only and the delegated token
	// still lacks permission to turn this connection into a chat-sync grant.
	proof, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	allowed, e := f.transactionGuard.StoredAuthenticationEligible(f.ctx, f.db, tx, f.subject, proof.Session.ID, proof.Account.Version, proof.Session.AuthorizationEpoch)
	_ = tx.Rollback()
	if e != nil || allowed {
		t.Fatal("transport expanded resource eligibility", allowed, e)
	}
	query(t, f.db, `UPDATE owned_app_consents SET revoked_at=clock_timestamp(),version=version+1 WHERE account_id=$1 AND app_id=$2`, f.subject, f.app.ID)
	if e = org.RenewConnection(delegated, f.subject, conn); e == nil {
		t.Fatal("withdrawn consent kept connection")
	}
	status, e = org.ChatStatus(f.ctx, f.subject)
	if e != nil || status.HasAnyWebSocketConnection {
		t.Fatal("withdrawn binding leaked status", status, e)
	}
}
