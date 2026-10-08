// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var chatPolicy = rooms.Policy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}

type chatFixture struct {
	db                                                 *sql.DB
	repo                                               *chatpg.Repository
	reader                                             *messagerpg.Repository
	handler                                            http.Handler
	issuer                                             *authority.Service
	owner, other, third, token, otherToken, thirdToken string
}

func chatRepo(t *testing.T, db *sql.DB) *chatpg.Repository {
	t.Helper()
	r, e := chatpg.NewForCompatibilityTests(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func newChatFixture(t *testing.T) chatFixture {
	t.Helper()
	db := database(t)
	query(t, db, authoritypg.Schema)
	query(t, db, chatpg.Schema)
	f := chatFixture{db: db, repo: chatRepo(t, db), owner: authorityUUID(t), other: authorityUUID(t), third: authorityUUID(t)}
	ctx := context.Background()
	for _, a := range []string{f.owner, f.other, f.third} {
		if e := authoritypg.SeedSynthetic(ctx, db, a, "synthetic_chat_"+strings.ReplaceAll(a, "-", ""), time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
		if e := authoritypg.New(db).ReplacePolicy(ctx, a, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	c := authorityConfig(t, authorityKeys(t), now)
	var e error
	f.issuer, e = authority.New(authoritypg.New(db), c)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(db), c)
	if e != nil {
		t.Fatal(e)
	}
	f.reader, e = messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	f.handler = (&rooms.Service{Repository: f.repo, Authority: v, Reader: f.reader}).Handler()
	f.token = pgIssue(t, f.issuer, f.owner).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	f.thirdToken = pgIssue(t, f.issuer, f.third).Token
	return f
}
func createChat(t *testing.T, f chatFixture, public bool) rooms.Room {
	t.Helper()
	name := "owned chat"
	v, e := f.repo.Create(context.Background(), f.owner, rooms.RoomPatch{Name: &name, IsPublic: &public, IsCommunity: &public})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func chatJoin(t *testing.T, f chatFixture, rid, actor string) {
	t.Helper()
	if _, e := f.repo.ChangeMember(context.Background(), actor, rid, rooms.Join, ""); e != nil {
		t.Fatal(e)
	}
}
func chatSend(t *testing.T, f chatFixture, actor, rid, content, key string) messager.MessageProjection {
	t.Helper()
	v, e := f.repo.Send(context.Background(), actor, rid, rooms.TextRequest{Content: content, ClientMessageID: &key})
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func chatCall(t *testing.T, h http.Handler, method, path, token, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s status %d: %s", method, path, w.Code, w.Body.String())
	}
	return w
}

func TestPGChatRoomTextLifecycle(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	w := chatCall(t, f.handler, "POST", "/api/chat", f.token, `{"name":"private","description":"owned text"}`, 200)
	var room rooms.Room
	if e := json.Unmarshal(w.Body.Bytes(), &room); e != nil {
		t.Fatal(e)
	}
	if room.AccountID != f.owner {
		t.Fatal(room)
	}
	base := "/api/chat/" + room.ID
	chatCall(t, f.handler, "GET", base, f.otherToken, "", 403)
	chatCall(t, f.handler, "GET", base+"/members", f.otherToken, "", 403)
	chatCall(t, f.handler, "POST", base+"/members/me", f.otherToken, "{}", 403)
	chatCall(t, f.handler, "POST", "/api/chat/invites/"+room.ID, f.token, fmt.Sprintf(`{"related_user_id":%q}`, f.other), 200)
	chatCall(t, f.handler, "GET", base+"/messages", f.otherToken, "", 403)
	chatCall(t, f.handler, "POST", base+"/messages", f.otherToken, `{"content":"pending cannot send"}`, 403)
	w = chatCall(t, f.handler, "GET", "/api/chat/invites", f.otherToken, "", 200)
	if !strings.Contains(w.Body.String(), room.ID) {
		t.Fatal("invite missing")
	}
	chatCall(t, f.handler, "POST", "/api/chat/invites/"+room.ID+"/accept", f.thirdToken, "{}", 404)
	chatCall(t, f.handler, "POST", "/api/chat/invites/"+room.ID+"/accept", f.otherToken, "{}", 200)
	w = chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"# original","client_message_id":"one"}`, 200)
	var m messager.MessageProjection
	if e := json.Unmarshal(w.Body.Bytes(), &m); e != nil {
		t.Fatal(e)
	}
	if m.RoomSequence != 1 || m.Content == nil || *m.Content != "\\# original" {
		t.Fatal(m)
	}
	w = chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"# original","client_message_id":"one"}`, 200)
	var same messager.MessageProjection
	_ = json.Unmarshal(w.Body.Bytes(), &same)
	if same.ID != m.ID {
		t.Fatal("retry duplicated", m, same)
	}
	chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"different","client_message_id":"one"}`, 409)
	w = chatCall(t, f.handler, "POST", base+"/sync", f.otherToken, `{"last_sync_timestamp":0}`, 200)
	if !strings.Contains(w.Body.String(), m.ID) {
		t.Fatal("canonical sync did not see write")
	}
	u, e := f.reader.Unread(ctx, f.other)
	if e != nil || u[room.ID].UnreadCount != 1 {
		t.Fatal(u, e)
	}
	chatCall(t, f.handler, "POST", base+"/members/me/read", f.otherToken, fmt.Sprintf(`{"message_id":%q}`, m.ID), 200)
	u, e = f.reader.Unread(ctx, f.other)
	if e != nil || u[room.ID].HasUnread {
		t.Fatal(u, e)
	}
	chatCall(t, f.handler, "PATCH", base+"/messages/"+m.ID, f.otherToken, `{"content":"hijacked"}`, 403)
	chatCall(t, f.handler, "PATCH", base+"/messages/"+m.ID, f.token, `{"content":"edited"}`, 200)
	w = chatCall(t, f.handler, "POST", base+"/sync", f.otherToken, `{"last_sync_timestamp":0,"missing_sequences":[2]}`, 200)
	var syncResult messager.SyncResponse
	_ = json.Unmarshal(w.Body.Bytes(), &syncResult)
	if len(syncResult.Messages) != 1 || syncResult.Messages[0].Type != "messages.update" || syncResult.Messages[0].ClientMessageID != nil || !strings.Contains(string(syncResult.Messages[0].Meta), m.ID) {
		t.Fatal(syncResult)
	}
	chatCall(t, f.handler, "DELETE", base+"/messages/"+m.ID, f.otherToken, "", 403)
	chatCall(t, f.handler, "DELETE", base+"/messages/"+m.ID, f.token, "", 204)
	chatCall(t, f.handler, "DELETE", base+"/messages/"+m.ID, f.token, "", 204)
	w = chatCall(t, f.handler, "POST", base+"/sync", f.otherToken, `{"last_sync_timestamp":0}`, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &syncResult)
	if len(syncResult.Messages) != 1 || syncResult.Messages[0].Type != "messages.delete" || strings.Contains(w.Body.String(), "original") || strings.Contains(w.Body.String(), `"content":"edited"`) {
		t.Fatal(w.Body.String())
	}
	chatCall(t, f.handler, "GET", base+"/messages/"+m.ID, f.otherToken, "", 404)
	chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"# original","client_message_id":"one"}`, 409)
	chatCall(t, f.handler, "DELETE", base+"/members/me", f.token, "", 403)
	chatCall(t, f.handler, "DELETE", base, f.otherToken, "", 403)
	chatCall(t, f.handler, "DELETE", base+"/members/me", f.otherToken, "", 204)
	chatCall(t, f.handler, "GET", base+"/messages", f.otherToken, "", 403)
	chatCall(t, f.handler, "DELETE", base, f.token, "", 204)
	chatCall(t, f.handler, "GET", base+"/messages", f.token, "", 404)
}

func TestPGChatMembershipPrivacyAndPolicy(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	room := createChat(t, f, true)
	base := "/api/chat/" + room.ID
	chatJoin(t, f, room.ID, f.other)
	chatJoin(t, f, room.ID, f.third)
	m := chatSend(t, f, f.owner, room.ID, "public text", "privacy")
	chatCall(t, f.handler, "GET", base+"/messages", "", "", 200)
	chatCall(t, f.handler, "GET", base+"/members", "", "", 401)
	if _, e := f.repo.MarkRead(ctx, f.other, room.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	members, e := f.repo.Members(ctx, f.owner, room.ID, 100, 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range members {
		if v.AccountID != f.owner && v.LastReadAt != nil {
			t.Fatal("private receipt exposed", v)
		}
	}
	otherRoom := createChat(t, f, true)
	otherMessage := chatSend(t, f, f.owner, otherRoom.ID, "elsewhere", "elsewhere")
	if _, e = f.repo.MarkRead(ctx, f.other, room.ID, otherMessage.ID); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal("cross-room read", e)
	}
	if _, e = f.repo.Send(ctx, f.other, room.ID, rooms.TextRequest{Content: "cross-room reply", RepliedMessageID: &otherMessage.ID}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = f.repo.Edit(ctx, f.owner, room.ID, otherMessage.ID, rooms.TextRequest{Content: "cross-room edit"}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = f.repo.ChangeMember(ctx, f.other, room.ID, rooms.Remove, f.third); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("member kicked other", e)
	}
	if _, e = f.repo.ChangeMember(ctx, f.owner, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ChangeMember(ctx, f.other, room.ID, rooms.Join, ""); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("kick bypass", e)
	}
	if _, e = f.repo.Send(ctx, f.other, room.ID, rooms.TextRequest{Content: "removed"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = f.repo.ChangeMember(ctx, f.owner, room.ID, rooms.Invite, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ChangeMember(ctx, f.other, room.ID, rooms.Accept, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.SetNotify(ctx, f.other, room.ID, chatPolicy.NotifyNone); e != nil {
		t.Fatal(e)
	}
	chatSend(t, f, f.owner, room.ID, "muted unread", "muted")
	u, e := f.reader.Unread(ctx, f.other)
	if e != nil || u[room.ID].UnreadCount != 0 || !u[room.ID].HasUnread {
		t.Fatal(u, e)
	}
	if _, e = f.repo.SetNotify(ctx, f.other, room.ID, 999); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE chat_rooms SET encryption_mode=88 WHERE id=$1`, room.ID)
	if _, e = f.repo.Send(ctx, f.owner, room.ID, rooms.TextRequest{Content: "never plaintext into encrypted room"}); !errors.Is(e, rooms.ErrUnsupported) {
		t.Fatal(e)
	}
}

func TestPGChatConcurrentSequencesAndRollback(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	room := createChat(t, f, true)
	chatJoin(t, f, room.ID, f.other)
	otherRepo := chatRepo(t, database(t))
	const n = 24
	start := make(chan struct{})
	errCh := make(chan error, n)
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := f.repo
			if i%2 != 0 {
				r = otherRepo
			}
			key := "duplicate"
			m, e := r.Send(ctx, f.owner, room.ID, rooms.TextRequest{Content: "same", ClientMessageID: &key})
			if e == nil {
				ids <- m.ID
			}
			errCh <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	close(ids)
	for e := range errCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for v := range ids {
		if id != "" && v != id {
			t.Fatal("duplicate request committed twice")
		}
		id = v
	}
	start = make(chan struct{})
	errCh = make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := f.repo
			if i%2 != 0 {
				r = otherRepo
			}
			key := fmt.Sprintf("sequence-%d", i)
			_, e := r.Send(ctx, f.other, room.ID, rooms.TextRequest{Content: key, ClientMessageID: &key})
			errCh <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count, unique, min, max int
	if e := f.db.QueryRow(`SELECT count(*),count(DISTINCT room_sequence),min(room_sequence),max(room_sequence) FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&count, &unique, &min, &max); e != nil || count != n+1 || unique != count || min != 1 || max != count {
		t.Fatal(count, unique, min, max, e)
	}
	query(t, f.db, `UPDATE chat_rooms SET owned_next_sequence=$2 WHERE id=$1`, room.ID, int64(math.MaxInt64))
	if _, e := f.repo.Edit(ctx, f.owner, room.ID, id, rooms.TextRequest{Content: "must roll back"}); e == nil {
		t.Fatal("overflow should fail")
	}
	var content string
	if e := f.db.QueryRow(`SELECT content FROM chat_messages WHERE id=$1`, id).Scan(&content); e != nil || content != "same" {
		t.Fatal(content, e)
	}
	query(t, f.db, `UPDATE chat_rooms SET owned_next_sequence=$2 WHERE id=$1`, room.ID, max)
	if e := otherRepo.DeleteMessage(ctx, f.owner, room.ID, id); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Edit(ctx, f.owner, room.ID, id, rooms.TextRequest{Content: "resurrect"}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestPGChatAuthorityAndUnsupportedBoundaries(t *testing.T) {
	f := newChatFixture(t)
	room := createChat(t, f, true)
	base := "/api/chat/" + room.ID
	for _, body := range []string{`{"content":"hi","attachments_id":[]}`, `{"content":"hi","encryption_meta":{}}`, `{"content":"hi","fund_id":null}`, `{"content":"hi","thread_id":null}`} {
		chatCall(t, f.handler, "POST", base+"/messages", f.token, body, 501)
	}
	chatCall(t, f.handler, "POST", base+"/messages?identity="+f.other, f.token, `{"content":"hi"}`, 501)
	chatCall(t, f.handler, "POST", "/api/chat/direct", f.token, `{}`, 501)
	chatCall(t, f.handler, "POST", "/api/chat", f.token, `{"name":"room","encryption_mode":88}`, 501)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.owner, []authority.Rule{{Pattern: "*"}, {Pattern: "chat.messages.create", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"denied"}`, 403)
	p, e := f.issuer.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.issuer.Revoke(context.Background(), f.token, p.Session.ID); e != nil {
		t.Fatal(e)
	}
	chatCall(t, f.handler, "POST", base+"/messages", f.token, `{"content":"revoked"}`, 401)
	chatCall(t, f.handler, "GET", base+"/messages", f.token, "", 401)
}

func TestPGChatConcurrentRemovalAndSend(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	room := createChat(t, f, true)
	chatJoin(t, f, room.ID, f.other)
	second := chatRepo(t, database(t))
	start := make(chan struct{})
	errs := make(chan error, 17)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("removal-race-%d", i)
			_, e := second.Send(ctx, f.other, room.ID, rooms.TextRequest{Content: "racing send", ClientMessageID: &key})
			if errors.Is(e, rooms.ErrForbidden) {
				e = nil
			}
			errs <- e
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_, e := f.repo.ChangeMember(ctx, f.owner, room.ID, rooms.Remove, f.other)
		errs <- e
	}()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e := second.Send(ctx, f.other, room.ID, rooms.TextRequest{Content: "after removal"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	var leaked int
	e := f.db.QueryRow(`SELECT count(*) FROM chat_messages msg JOIN chat_members m ON m.id=msg.sender_id WHERE msg.chat_room_id=$1 AND m.account_id=$2 AND msg.created_at>m.leave_at`, room.ID, f.other).Scan(&leaked)
	if e != nil || leaked != 0 {
		t.Fatal("send committed after membership removal", leaked, e)
	}
}

func TestPGChatRestartPersistence(t *testing.T) {
	db := database(t)
	file := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "owned-chat-restart.json")
	var saved struct{ Owner, Other, Room, Message string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newChatFixture(t)
		room := createChat(t, f, true)
		chatJoin(t, f, room.ID, f.other)
		m := chatSend(t, f, f.owner, room.ID, "survives database restart", "restart")
		if _, e := f.repo.Edit(context.Background(), f.owner, room.ID, m.ID, rooms.TextRequest{Content: "edited before restart"}); e != nil {
			t.Fatal(e)
		}
		saved.Owner = f.owner
		saved.Other = f.other
		saved.Room = room.ID
		saved.Message = m.ID
		b, _ := json.Marshal(saved)
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("restart test requires before/after phase")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &saved); e != nil {
		t.Fatal(e)
	}
	repo := chatRepo(t, db)
	if _, e = repo.Me(context.Background(), saved.Other, saved.Room); e != nil {
		t.Fatal(e)
	}
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	sync, e := reader.Sync(context.Background(), messager.SyncQuery{RoomID: saved.Room, AccountID: saved.Other, Request: messager.SyncRequest{LastSyncTimestamp: 0}})
	if e != nil || len(sync.Messages) != 2 || *sync.Messages[0].Content != "edited before restart" || sync.Messages[1].Type != "messages.update" {
		t.Fatal(sync, e)
	}
	key := "post-restart"
	m, e := repo.Send(context.Background(), saved.Other, saved.Room, rooms.TextRequest{Content: "after restart", ClientMessageID: &key})
	if e != nil || m.RoomSequence != 3 {
		t.Fatal(m, e)
	}
}
