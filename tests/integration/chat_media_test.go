// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatmediapg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type chatMediaFixture struct {
	mediaFixture
	chat   *chatpg.Repository
	reader *messagerpg.Repository
	links  *chatmediapg.Repository
}

func newChatMediaFixture(t *testing.T) chatMediaFixture {
	t.Helper()
	f := chatMediaFixture{mediaFixture: newMediaFixture(t)}
	for _, actor := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	return chatMediaPool(t, f, f.db)
}
func chatMediaPool(t *testing.T, f chatMediaFixture, db *sql.DB) chatMediaFixture {
	t.Helper()
	f.mediaFixture = mediaPool(t, f.mediaFixture, db)
	var e error
	f.chat, e = chatpg.NewWithAuthority(db, chatPolicy, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	f.chat.WithSocial(socialpg.New(db))
	f.links = chatmediapg.New(db, f.chat, chatPolicy.NoneEncryptionMode)
	if e = f.links.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.chat.WithAssociations(f.links)
	f.repo.WithReadVisibility(f.links)
	f.reader, e = messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	f.reader.WithRoomAccess(f.chat).WithMessageDecorator(f.links)
	return f
}
func chatMediaRoom(t *testing.T, f chatMediaFixture, public bool) rooms.Room {
	t.Helper()
	name := "Rich chat"
	v, e := f.chat.Create(f.ctx, f.actor, rooms.RoomPatch{Name: &name, IsPublic: &public})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func chatMediaFile(t *testing.T, f chatMediaFixture, key, body string) media.File {
	t.Helper()
	v, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload(key, body, false), strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func chatMediaJoin(t *testing.T, f chatMediaFixture, rid string) {
	t.Helper()
	if _, e := f.chat.ChangeMember(f.ctx, f.actor, rid, rooms.Invite, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e := f.chat.ChangeMember(f.otherCtx, f.other, rid, rooms.Accept, ""); e != nil {
		t.Fatal(e)
	}
}
func noChatMedia(t *testing.T, f chatMediaFixture, ctx context.Context, actor, id string) {
	t.Helper()
	_, body, e := f.repo.Open(ctx, actor, id)
	if body != nil {
		body.Close()
	}
	if !errors.Is(e, media.ErrNotFound) {
		t.Fatal("private bytes exposed", e)
	}
}
func chatMediaMessage(t *testing.T, f chatMediaFixture, actor, rid, mid string) messager.MessageProjection {
	t.Helper()
	m, e := f.reader.GetMessage(context.Background(), messager.MessageQuery{AccountID: actor, RoomID: rid, MessageID: mid})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestPGChatMediaLifecycleCurrentVisibilityAndIsolation(t *testing.T) {
	f := newChatMediaFixture(t)
	room := chatMediaRoom(t, f, false)
	file := chatMediaFile(t, f, "chat-file", "private chat bytes")
	key := "send-rich"
	p := rooms.TextRequest{AttachmentsID: mediaIDs(file.ID), ClientMessageID: &key, Meta: json.RawMessage(`{"custom":"preserved"}`)}
	m, e := f.chat.Send(f.ctx, f.actor, room.ID, p)
	if e != nil || !bytes.Contains(m.Attachments, []byte(file.ID)) || m.Content != nil {
		t.Fatal(m, e)
	}
	again, e := f.chat.Send(f.ctx, f.actor, room.ID, p)
	if e != nil || again.ID != m.ID {
		t.Fatal(again, e)
	}
	changed := p
	changed.Content = "different"
	if _, e = f.chat.Send(f.ctx, f.actor, room.ID, changed); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal(e)
	}
	noChatMedia(t, f, context.Background(), "", file.ID)
	noChatMedia(t, f, f.otherCtx, f.other, file.ID)
	chatMediaJoin(t, f, room.ID)
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, "private chat bytes")
	second := chatMediaRoom(t, f, false)
	chatMediaJoin(t, f, second.ID)
	if _, e = f.chat.Send(f.otherCtx, f.other, second.ID, rooms.TextRequest{AttachmentsID: mediaIDs(file.ID)}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("redistribution", e)
	}
	if _, e = f.chat.Send(f.ctx, f.actor, second.ID, rooms.TextRequest{Content: "cross room reply", RepliedMessageID: &m.ID}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = f.chat.Edit(f.otherCtx, f.other, room.ID, m.ID, rooms.TextRequest{Content: "forged edit"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if e = f.chat.DeleteMessage(f.otherCtx, f.other, room.ID, m.ID); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = f.chat.Edit(f.ctx, f.actor, room.ID, m.ID, rooms.TextRequest{AttachmentsID: mediaIDs()}); !errors.Is(e, rooms.ErrInvalid) {
		t.Fatal("empty edit", e)
	}
	if !bytes.Contains(chatMediaMessage(t, f, f.actor, room.ID, m.ID).Attachments, []byte(file.ID)) {
		t.Fatal("failed edit lost link")
	}
	edited, e := f.chat.Edit(f.ctx, f.actor, room.ID, m.ID, rooms.TextRequest{Content: "caption"})
	if e != nil || edited.EditedAt == nil {
		t.Fatal(edited, e)
	}
	if _, e = f.chat.Edit(f.ctx, f.actor, room.ID, m.ID, rooms.TextRequest{AttachmentsID: mediaIDs()}); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f, f.otherCtx, f.other, file.ID)
	syncResult, e := f.reader.Sync(context.Background(), messager.SyncQuery{RoomID: room.ID, AccountID: f.actor})
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range syncResult.Messages {
		if bytes.Contains(event.Attachments, []byte(file.ID)) {
			t.Fatal("old edit retained detached media")
		}
	}
	if _, e = f.chat.Edit(f.ctx, f.actor, room.ID, m.ID, rooms.TextRequest{AttachmentsID: mediaIDs(file.ID)}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	noChatMedia(t, f, f.otherCtx, f.other, file.ID)
	yes := true
	if _, e = f.chat.Update(f.ctx, f.actor, room.ID, rooms.RoomPatch{IsPublic: &yes}); e != nil {
		t.Fatal(e)
	}
	mediaRead(t, f.repo, context.Background(), "", file.ID, "private chat bytes")
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	}
	if got := chatMediaMessage(t, f, "", room.ID, m.ID); string(got.Attachments) != "[]" {
		t.Fatal("deleted media metadata", string(got.Attachments))
	}
	noChatMedia(t, f, context.Background(), "", file.ID)
	if e = f.chat.DeleteMessage(f.ctx, f.actor, room.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	var links int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_chat_media WHERE message_id=$1`, m.ID).Scan(&links); e != nil || links != 0 {
		t.Fatal(links, e)
	}
}
func TestPGChatReactionsContractsConcurrencyAndRollback(t *testing.T) {
	f := newChatMediaFixture(t)
	room := chatMediaRoom(t, f, true)
	chatMediaJoin(t, f, room.ID)
	m, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "react here"})
	if e != nil {
		t.Fatal(e)
	}
	first, e := f.chat.React(f.otherCtx, f.other, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart", Attitude: 73})
	if e != nil || first == nil || first.Attitude != 73 {
		t.Fatal(first, e)
	}
	made := chatMediaMessage(t, f, f.other, room.ID, m.ID)
	if !made.ReactionsMade["heart"] || !bytes.Contains(made.ReactionsCount, []byte(`"heart": 1`)) {
		t.Fatal(made)
	}
	outsider := chatMediaRoom(t, f, true)
	if _, e = f.chat.React(f.ctx, f.actor, outsider.ID, m.ID, rooms.ReactionRequest{Symbol: "heart"}); !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	items, total, e := f.chat.ListReactions(context.Background(), "", room.ID, m.ID, rooms.ReactionQuery{Symbol: "heart", Take: 20})
	if e != nil || total != 1 || len(items) != 1 || items[0].SenderID != first.SenderID {
		t.Fatal(items, total, e)
	}
	second := chatMediaPool(t, f, database(t))
	start := make(chan struct{})
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := f.chat
			if i%2 == 1 {
				r = second.chat
			}
			ctx := f.otherCtx
			if i%2 == 1 {
				ctx = second.otherCtx
			}
			_, e := r.React(ctx, f.other, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart", Attitude: 73})
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	_, total, e = f.chat.ListReactions(context.Background(), f.actor, room.ID, m.ID, rooms.ReactionQuery{Take: 20})
	if e != nil || total != 1 {
		t.Fatal("toggle parity", total, e)
	}
	if e = f.chat.RemoveReaction(f.otherCtx, f.other, room.ID, m.ID, "heart"); e != nil {
		t.Fatal(e)
	}
	if e = f.chat.RemoveReaction(f.otherCtx, f.other, room.ID, m.ID, "heart"); e != nil {
		t.Fatal(e)
	}
	// A failed durable event must roll back the reaction and counter together.
	trigger := "reject_chat_reaction_" + strings.ReplaceAll(room.ID, "-", "")
	query(t, f.db, `CREATE FUNCTION `+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.chat_room_id='`+room.ID+`'::uuid AND NEW.type='messages.reaction.added' THEN RAISE EXCEPTION 'synthetic rollback'; END IF; RETURN NEW; END $$`)
	query(t, f.db, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON chat_messages FOR EACH ROW EXECUTE FUNCTION `+trigger+`()`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS ` + trigger + ` ON chat_messages;DROP FUNCTION IF EXISTS ` + trigger + `()`)
	})
	if _, e = f.chat.React(f.otherCtx, f.other, room.ID, m.ID, rooms.ReactionRequest{Symbol: "clap"}); e == nil {
		t.Fatal("missing rollback")
	}
	_, total, e = f.chat.ListReactions(context.Background(), f.actor, room.ID, m.ID, rooms.ReactionQuery{Take: 20})
	if e != nil || total != 0 {
		t.Fatal(total, e)
	}
	if got := chatMediaMessage(t, f, f.other, room.ID, m.ID); string(got.ReactionsCount) != "{}" || len(got.ReactionsMade) != 0 {
		t.Fatal(got)
	}
	if _, e = f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e = f.chat.React(f.otherCtx, f.other, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart"}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal(e)
	}
}
func TestPGChatMediaAuthorityAndFileWaitRevalidation(t *testing.T) {
	f := newChatMediaFixture(t)
	room := chatMediaRoom(t, f, false)
	chatMediaJoin(t, f, room.ID)
	file := chatMediaFile(t, f, "wait", "locked file")
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting' WHERE id=$1`, file.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.chat.Send(ctx, f.actor, room.ID, rooms.TextRequest{AttachmentsID: mediaIDs(file.ID)})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, rooms.ErrNotFound) {
		t.Fatal(e)
	}
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed attach committed message", n, e)
	}
	m, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{Content: "still authorized"})
	if e != nil {
		t.Fatal(e)
	}
	blocker, pid = atomicBlocker(t, locks, "chat_rooms", room.ID)
	if _, e = blocker.ExecContext(ctx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other); e != nil {
		t.Fatal(e)
	}
	otherCtx, cancelOther := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancelOther()
	go func() {
		_, e := f.chat.React(otherCtx, f.other, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart"})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "chat_rooms")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("stale membership", e)
	}
	blocker, pid = atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e = blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	go func() {
		_, e := f.chat.React(ctx, f.actor, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart"})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("stale permission", e)
	}
}
func TestPGBackendChatMediaAndReactionsComposition(t *testing.T) {
	f := newChatMediaFixture(t)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	room := backendDecode[rooms.Room](t, backendCall(t, app, "POST", "/messager/chat", f.token, `{"name":"Rich HTTP","is_public":true}`, 200))
	req := httptest.NewRequest("POST", "/api/media/files", strings.NewReader("http attachment"))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-File-Name", "chat.txt")
	req.Header.Set("X-Content-SHA256", mediaDigest("http attachment"))
	req.Header.Set("Idempotency-Key", "chat-http")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	file := backendDecode[media.File](t, w)
	target := "/messager/chat/" + room.ID + "/messages"
	m := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", target, f.token, fmt.Sprintf(`{"content":"","attachments_id":[%q],"nonce":"rich-http","meta":{"client_label":"kept"},"thread_id":null,"forwarded_message_id":null}`, file.ID), 200))
	if !bytes.Contains(m.Attachments, []byte(file.ID)) {
		t.Fatal(string(m.Attachments))
	}
	backendCall(t, app, "GET", "/api/files/"+file.ID, "", "", 200)
	reaction := target + "/" + m.ID + "/reactions"
	r := backendDecode[rooms.Reaction](t, backendCall(t, app, "POST", reaction, f.token, `{"symbol":"heart","attitude":73}`, 200))
	if r.Attitude != 73 {
		t.Fatal(r)
	}
	w = backendCall(t, app, "GET", reaction+"?symbol=heart&order=created&take=20", "", "", 200)
	if w.Header().Get("X-Total") != "1" {
		t.Fatal(w.Header())
	}
	backendCall(t, app, "POST", reaction, f.token, `{"symbol":"heart","attitude":73}`, 204)
	backendCall(t, app, "DELETE", reaction+"/heart", f.token, "", 204)
	edited := backendDecode[messager.MessageProjection](t, backendCall(t, app, "PATCH", target+"/"+m.ID, f.token, `{"content":"caption","attachments_id":[],"client_message_id":"edit-correlation","meta":{},"replied_message_id":null,"thread_id":null,"forwarded_message_id":null}`, 200))
	if edited.ClientMessageID == nil || *edited.ClientMessageID != "edit-correlation" || !bytes.Contains(edited.Meta, []byte("client_label")) {
		t.Fatal("caller edit contract", edited)
	}
	replay := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", target, f.token, fmt.Sprintf(`{"content":"","attachments_id":[%q],"nonce":"rich-http","meta":{"client_label":"kept"},"thread_id":null,"forwarded_message_id":null}`, file.ID), 200))
	if replay.ID != m.ID || replay.Content == nil || *replay.Content != "caption" {
		t.Fatal("edit broke original send replay", replay)
	}
	backendCall(t, app, "GET", "/api/files/"+file.ID, "", "", 404)
	backendCall(t, app, "POST", target, f.token, `{"encryption_meta":{"ciphertext":"opaque"}}`, 501)
	backendCall(t, app, "DELETE", target+"/"+m.ID, f.token, "", 204)
	backendCall(t, app, "POST", reaction, f.token, `{"symbol":"heart"}`, 404)
}
func TestPGChatMediaReactionsRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	objects := filepath.Join(root, "chat-media-objects")
	record := filepath.Join(root, "chat-media-record.json")
	type saved struct{ Room, Message, File, Actor string }
	if phase == "before" {
		f := newChatMediaFixture(t)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.root = objects
		f = chatMediaPool(t, f, db)
		room := chatMediaRoom(t, f, true)
		file := chatMediaFile(t, f, "persist", "durable chat file")
		m, e := f.chat.Send(f.ctx, f.actor, room.ID, rooms.TextRequest{AttachmentsID: mediaIDs(file.ID)})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.chat.Edit(f.ctx, f.actor, room.ID, m.ID, rooms.TextRequest{Content: "durable edit"}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.chat.React(f.ctx, f.actor, room.ID, m.ID, rooms.ReactionRequest{Symbol: "heart", Attitude: 73}); e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(saved{room.ID, m.ID, file.ID, f.actor})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(record, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if phase != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(record)
	if e != nil {
		t.Fatal(e)
	}
	var state saved
	if e = json.Unmarshal(b, &state); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	chat, e := chatpg.New(db, chatPolicy)
	if e != nil {
		t.Fatal(e)
	}
	chat.WithSocial(socialpg.New(db))
	links := chatmediapg.New(db, chat, 77)
	mediaRepo, e := mediapg.New(db, store, authoritypg.NewTransactionGuard(authoritypg.New(db), nil), mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	mediaRepo.WithReadVisibility(links)
	mediaRead(t, mediaRepo, context.Background(), "", state.File, "durable chat file")
	reader, e := messagerpg.NewWithIndependentPolicy(db, messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30})
	if e != nil {
		t.Fatal(e)
	}
	reader.WithRoomAccess(chat).WithMessageDecorator(links)
	m, e := reader.GetMessage(context.Background(), messager.MessageQuery{RoomID: state.Room, MessageID: state.Message})
	if e != nil || m.Content == nil || *m.Content != "durable edit" || !bytes.Contains(m.Attachments, []byte(state.File)) {
		t.Fatal(m, e)
	}
	items, total, e := chat.ListReactions(context.Background(), "", state.Room, state.Message, rooms.ReactionQuery{Take: 20})
	if e != nil || total != 1 || len(items) != 1 || items[0].Attitude != 73 {
		t.Fatal(items, total, e)
	}
}
