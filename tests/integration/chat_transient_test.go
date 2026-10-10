// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func newChatTransient(t *testing.T) chatMediaFixture {
	t.Helper()
	f := newChatMediaFixture(t)
	if e := f.chat.TransientReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.VoiceReady(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := f.chat.WithVoiceStorage(f.repo, time.Hour); e != nil {
		t.Fatal(e)
	}
	return f
}
func transientVoice(key string) rooms.VoiceRequest {
	d := int32(1250)
	return rooms.VoiceRequest{Name: "synthetic.m4a", MIMEType: "audio/m4a", Data: []byte("synthetic voice bytes"), DurationMS: &d, ClientMessageID: &key}
}
func transientClip(t *testing.T, m messager.MessageProjection) string {
	t.Helper()
	var meta struct {
		ID string `json:"voice_clip_id"`
	}
	if json.Unmarshal(m.Meta, &meta) != nil || !rooms.ValidID(meta.ID) {
		t.Fatal(string(m.Meta))
	}
	return meta.ID
}
func transientRead(t *testing.T, f chatMediaFixture, ctx context.Context, actor, rid, vid string) {
	t.Helper()
	clip, body, e := f.chat.OpenVoice(ctx, actor, rid, vid)
	if e != nil {
		t.Fatal(e)
	}
	defer body.Close()
	b, e := io.ReadAll(body)
	if e != nil || string(b) != "synthetic voice bytes" || clip.MIMEType != "audio/m4a" {
		t.Fatal(string(b), clip, e)
	}
}
func transientPlaceholder(t *testing.T, f chatMediaFixture, rid, kind string) messager.MessageProjection {
	t.Helper()
	m, e := f.chat.SendPlaceholder(f.ctx, f.actor, rid, rooms.PlaceholderRequest{Kind: kind})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func transientApp(t *testing.T, f chatMediaFixture) *application.Backend {
	t.Helper()
	visibility := int32(0)
	p := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &p, MediaStore: f.store, MaxMediaBytes: 65536, ChatVoiceRetention: time.Hour})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app
}
func transientUpload(t *testing.T, app http.Handler, token, rid, key string) *httptest.ResponseRecorder {
	t.Helper()
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="synthetic.m4a"`)
	header.Set("Content-Type", "audio/m4a")
	part, e := w.CreatePart(header)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = part.Write([]byte("synthetic voice bytes"))
	_ = w.WriteField("client_message_id", key)
	_ = w.WriteField("durationMs", "1250")
	w.Close()
	r := httptest.NewRequest("POST", "/messager/chat/"+rid+"/messages/voice", buf)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	app.ServeHTTP(out, r)
	return out
}

func TestPGChatPlaceholderLifecycleReplacementAndFinalization(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	first := transientPlaceholder(t, f, room.ID, "streaming")
	chunk := "preserved chunk"
	if _, e := f.chat.UpdatePlaceholder(f.ctx, f.actor, rooms.PlaceholderUpdate{MessageID: first.ID, ContentChunk: &chunk}); e != nil {
		t.Fatal(e)
	}
	second := transientPlaceholder(t, f, room.ID, "uploading")
	prior := chatMediaMessage(t, f, f.actor, room.ID, first.ID)
	if prior.Type != "text" || prior.Content == nil || *prior.Content != chunk || bytes.Contains(prior.Meta, []byte("placeholder_")) {
		t.Fatal(prior)
	}
	file := chatMediaFile(t, f, "placeholder-attachment", "attachment")
	p := rooms.PlaceholderFinalize{MessageID: second.ID, AttachmentsID: mediaIDs(file.ID)}
	out, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, p)
	if e != nil || out.Type != "text" || !bytes.Contains(out.Attachments, []byte(file.ID)) {
		t.Fatal(out, e)
	}
	again, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, p)
	if e != nil || again.ID != out.ID {
		t.Fatal(again, e)
	}
	p.Content = &chunk
	if _, e = f.chat.FinalizePlaceholder(f.ctx, f.actor, p); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("changed retry", e)
	}
	if _, e = f.chat.UpdatePlaceholder(f.ctx, f.actor, rooms.PlaceholderUpdate{MessageID: second.ID, ContentChunk: &chunk}); !errors.Is(e, rooms.ErrConflict) {
		t.Fatal("late update", e)
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='messages.sync.finalize'`, room.ID).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	third := transientPlaceholder(t, f, room.ID, "uploading")
	fourth := transientPlaceholder(t, f, room.ID, "uploading")
	if third.ID == fourth.ID {
		t.Fatal("creation reused an expired ID")
	}
	var deleted bool
	if e = f.db.QueryRow(`SELECT deleted_at IS NOT NULL FROM chat_messages WHERE id=$1`, third.ID).Scan(&deleted); e != nil || !deleted {
		t.Fatal(deleted, e)
	}
}
func TestPGChatPlaceholderExpiryRaceAndAtomicRollback(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	p := transientPlaceholder(t, f, room.ID, "streaming")
	query(t, f.db, `UPDATE chat_messages SET meta=jsonb_set(meta,'{placeholder_expires_at}','0') WHERE id=$1`, p.ID)
	// Source updates can revive an elapsed but not yet deleted placeholder.
	chunk := "revived"
	if _, e := f.chat.UpdatePlaceholder(f.ctx, f.actor, rooms.PlaceholderUpdate{MessageID: p.ID, ContentChunk: &chunk}); e != nil {
		t.Fatal(e)
	}
	if n, e := f.chat.ExpirePlaceholders(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.chat.SendPlaceholder(f.ctx, f.actor, room.ID, rooms.PlaceholderRequest{Kind: "uploading"})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1 AND type='placeholder' AND deleted_at IS NULL`, room.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	query(t, f.db, `UPDATE chat_messages SET meta=jsonb_set(meta,'{placeholder_expires_at}','0') WHERE chat_room_id=$1 AND type='placeholder' AND deleted_at IS NULL`, room.ID)
	if n, e := f.chat.ExpirePlaceholders(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := f.chat.ExpirePlaceholders(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	p = transientPlaceholder(t, f, room.ID, "uploading")
	missing := authorityUUID(t)
	if _, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, rooms.PlaceholderFinalize{MessageID: p.ID, AttachmentsID: mediaIDs(missing)}); e == nil {
		t.Fatal("missing attachment accepted")
	}
	if got := chatMediaMessage(t, f, f.actor, room.ID, p.ID); got.Type != "placeholder" {
		t.Fatal("failed finalize committed", got)
	}
}
func TestPGChatTransientOriginalProofMembershipAndEncryptedBoundaries(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	chatMediaJoin(t, f, room.ID)
	p := transientPlaceholder(t, f, room.ID, "streaming")
	if _, e := f.chat.FinalizePlaceholder(f.otherCtx, f.other, rooms.PlaceholderFinalize{MessageID: p.ID}); !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("foreign owner", e)
	}
	if _, e := f.chat.UpdatePlaceholder(context.Background(), f.actor, rooms.PlaceholderUpdate{MessageID: p.ID}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("forged proof", e)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "chat_rooms", room.ID)
	if _, e := blocker.ExecContext(ctx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.chat.UpdatePlaceholder(ctx, f.actor, rooms.PlaceholderUpdate{MessageID: p.ID})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "chat_rooms")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, rooms.ErrForbidden) {
		t.Fatal("stale membership", e)
	}
	encrypted := chatMediaRoom(t, f, false)
	query(t, f.db, `UPDATE chat_rooms SET encryption_mode=3 WHERE id=$1`, encrypted.ID)
	for _, send := range []func() error{func() error {
		_, e := f.chat.SendPlaceholder(f.ctx, f.actor, encrypted.ID, rooms.PlaceholderRequest{Kind: "streaming"})
		return e
	}, func() error {
		_, e := f.chat.SendVoice(f.ctx, f.actor, encrypted.ID, transientVoice("encrypted"))
		return e
	}} {
		if e := send(); !errors.Is(e, rooms.ErrUnsupported) {
			t.Fatal("plaintext downgrade", e)
		}
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
}
func TestPGChatVoiceStorageQuotaPrivacyAndExpiry(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	p := transientVoice("canonical-voice")
	m, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, p)
	if e != nil {
		t.Fatal(e)
	}
	vid := transientClip(t, m)
	if m.Type != "voice" || m.Content != nil {
		t.Fatal(m)
	}
	mediaUsage(t, f.mediaFixture, int64(len(p.Data)), 0)
	again, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, p)
	if e != nil || again.ID != m.ID {
		t.Fatal(again, e)
	}
	bad := p
	bad.Data = []byte("changed")
	if _, e = f.chat.SendVoice(f.ctx, f.actor, room.ID, bad); !errors.Is(e, media.ErrConflict) {
		t.Fatal("different bytes retry", e)
	}
	for _, actor := range []string{"", f.other} {
		ctx := context.Background()
		if actor != "" {
			ctx = f.otherCtx
		}
		_, b, e := f.chat.OpenVoice(ctx, actor, room.ID, vid)
		if b != nil {
			b.Close()
		}
		if !errors.Is(e, rooms.ErrForbidden) {
			t.Fatal("private voice", e)
		}
	}
	chatMediaJoin(t, f, room.ID)
	transientRead(t, f, f.otherCtx, f.other, room.ID, vid)
	if _, e = f.chat.ChangeMember(f.ctx, f.actor, room.ID, rooms.Remove, f.other); e != nil {
		t.Fatal(e)
	}
	if _, b, e := f.chat.OpenVoice(f.otherCtx, f.other, room.ID, vid); e == nil {
		b.Close()
		t.Fatal("removed member voice")
	}
	query(t, f.db, `UPDATE owned_chat_voice_clips SET expires_at=created_at+interval '1 microsecond',created_at=created_at-interval '1 hour' WHERE id=$1`, vid)
	// Set expiry in the past while preserving the owned schema invariant.
	query(t, f.db, `UPDATE owned_chat_voice_clips SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, vid)
	if _, b, e := f.chat.OpenVoice(f.ctx, f.actor, room.ID, vid); e == nil {
		b.Close()
		t.Fatal("expired bytes")
	}
	if n, e := f.repo.CleanupVoices(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := f.repo.CleanupVoices(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
}
func TestPGChatVoiceStagingRollbackAndCleanupRecovery(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=1 WHERE account_id=$1`, f.actor)
	if _, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("quota")); !errors.Is(e, media.ErrQuota) {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=65536 WHERE account_id=$1`, f.actor)
	// A later canonical message failure must compensate its durable media staging.
	query(t, f.db, `CREATE FUNCTION transient_reject_voice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='voice' THEN RAISE EXCEPTION 'synthetic voice rollback'; END IF; RETURN NEW; END $$;CREATE TRIGGER transient_reject_voice BEFORE INSERT ON chat_messages FOR EACH ROW EXECUTE FUNCTION transient_reject_voice()`)
	if _, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("rollback")); e == nil {
		t.Fatal("expected atomic message failure")
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	query(t, f.db, `DROP TRIGGER transient_reject_voice ON chat_messages;DROP FUNCTION transient_reject_voice()`)
	m, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("committed"))
	if e != nil {
		t.Fatal(e)
	}
	vid := transientClip(t, m)
	if e = f.repo.AbandonVoice(context.Background(), vid); e != nil {
		t.Fatal(e)
	}
	transientRead(t, f, f.ctx, f.actor, room.ID, vid)
	// Deletion revokes download immediately; cleanup owns physical removal/quota.
	if e = f.chat.DeleteMessage(f.ctx, f.actor, room.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	if _, b, e := f.chat.OpenVoice(f.ctx, f.actor, room.ID, vid); e == nil {
		b.Close()
		t.Fatal("deleted message bytes")
	}
	if n, e := f.repo.CleanupVoices(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
}
func TestPGBackendChatTransientMultipartAndWebSocketComposition(t *testing.T) {
	f := newChatTransient(t)
	app := transientApp(t, f)
	room := chatMediaRoom(t, f, false)
	out := transientUpload(t, app, f.token, room.ID, "multipart-client")
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	m := backendDecode[messager.MessageProjection](t, out)
	vid := transientClip(t, m)
	r := httptest.NewRequest("GET", "/messager/chat/"+room.ID+"/voice/"+vid, nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Range", "bytes=0-8")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "synthetic" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	backendCall(t, app, "GET", "/api/chat/"+room.ID+"/voice/"+vid, "", "", 403)
	placeholder := backendDecode[messager.MessageProjection](t, backendCall(t, app, "POST", "/messager/chat/"+room.ID+"/messages/placeholder", f.token, `{"kind":"streaming"}`, 200))
	// Network use exists only in the guarded disposable actual-PG case.
	server := httptest.NewServer(app)
	defer server.Close()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+f.token)
	conn, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", header)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for _, packet := range []map[string]any{{"type": "messages.placeholder.update", "endpoint": "DysonNetwork.Messager", "data": map[string]any{"message_id": placeholder.ID, "content_chunk": "from packet"}}, {"type": "messages.placeholder.finalize", "endpoint": "DysonNetwork.Messager", "data": map[string]any{"message_id": placeholder.ID}}} {
		if e = conn.WriteJSON(packet); e != nil {
			t.Fatal(e)
		}
		var reply struct {
			Type string                     `json:"type"`
			Data messager.MessageProjection `json:"data"`
		}
		if e = conn.ReadJSON(&reply); e != nil || reply.Type != packet["type"] || reply.Data.ID != placeholder.ID {
			t.Fatal(reply, e)
		}
	}
	if got := chatMediaMessage(t, f, f.actor, room.ID, placeholder.ID); got.Type != "text" || got.Content == nil || *got.Content != "from packet" {
		t.Fatal(got)
	}
	// Revoking the same connection's session cannot authorize another packet.
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
	_ = conn.WriteJSON(map[string]any{"type": "messages.placeholder.finalize", "endpoint": "DysonNetwork.Messager", "data": map[string]any{"message_id": placeholder.ID}})
	if _, _, e = conn.ReadMessage(); e == nil {
		t.Fatal("revoked connection accepted packet")
	}
}
func TestPGChatTransientSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type saved struct {
		Actor, Other, Token, OtherToken, Room, Voice, Clip, Placeholder, Finalized, Objects string
		Authority                                                                           restartAuthoritySnapshot
	}
	var state saved
	if phase == "before" {
		f := newChatTransient(t)
		f.root = filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "chat-transient-objects")
		if e := os.Mkdir(f.root, 0700); e != nil {
			t.Fatal(e)
		}
		now := f.config.Now().UTC()
		wall := time.Now()
		f.config.Now = func() time.Time { return now.Add(time.Since(wall)) }
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		var e error
		f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, f.issuer, f.actor).Token
		f.otherToken = pgIssue(t, f.issuer, f.other).Token
		f = chatMediaPool(t, f, f.db)
		if e = f.chat.WithVoiceStorage(f.repo, time.Hour); e != nil {
			t.Fatal(e)
		}
		room := chatMediaRoom(t, f, false)
		chatMediaJoin(t, f, room.ID)
		v, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("saved-voice"))
		if e != nil {
			t.Fatal(e)
		}
		p := transientPlaceholder(t, f, room.ID, "streaming")
		chunk := "saved placeholder"
		if _, e = f.chat.UpdatePlaceholder(f.ctx, f.actor, rooms.PlaceholderUpdate{MessageID: p.ID, ContentChunk: &chunk}); e != nil {
			t.Fatal(e)
		}
		final, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, rooms.PlaceholderFinalize{MessageID: p.ID})
		if e != nil {
			t.Fatal(e)
		}
		p = transientPlaceholder(t, f, room.ID, "uploading")
		state = saved{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Room: room.ID, Voice: v.ID, Clip: transientClip(t, v), Placeholder: p.ID, Finalized: final.ID, Objects: f.root, Authority: saveAuthoritySnapshot(t, f.config)}
		saveRestartFixture(t, "chat-transient-state.json", state)
		return
	}
	loadRestartFixture(t, "chat-transient-state.json", &state)
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	if cfg.SigningKey != nil {
		t.Fatal("private signer restored")
	}
	f := chatMediaPool(t, chatMediaFixture{mediaFixture: mediaFixture{db: db, actor: state.Actor, other: state.Other, token: state.Token, otherToken: state.OtherToken, root: state.Objects, config: cfg}}, db)
	if e := f.chat.WithVoiceStorage(f.repo, time.Hour); e != nil {
		t.Fatal(e)
	}
	transientRead(t, f, f.ctx, f.actor, state.Room, state.Clip)
	transientRead(t, f, f.otherCtx, f.other, state.Room, state.Clip)
	if got := chatMediaMessage(t, f, f.actor, state.Room, state.Placeholder); got.Type != "placeholder" {
		t.Fatal(got)
	}
	if got := chatMediaMessage(t, f, f.actor, state.Room, state.Finalized); got.Type != "text" || got.Content == nil || *got.Content != "saved placeholder" {
		t.Fatal(got)
	}
	again, e := f.chat.FinalizePlaceholder(f.ctx, f.actor, rooms.PlaceholderFinalize{MessageID: state.Finalized})
	if e != nil || again.ID != state.Finalized {
		t.Fatal(again, e)
	}
	v, e := f.chat.SendVoice(f.ctx, f.actor, state.Room, transientVoice("saved-voice"))
	if e != nil || v.ID != state.Voice {
		t.Fatal(v, e)
	}
	mediaUsage(t, f.mediaFixture, int64(len(transientVoice("").Data)), 0)
}

func TestPGChatVoiceReadWaitRechecksOriginalProofAudienceAndExpiry(t *testing.T) {
	for _, scenario := range []string{"session", "membership", "expiry", "credential_expiry"} {
		t.Run(scenario, func(t *testing.T) {
			f := newChatTransient(t)
			room := chatMediaRoom(t, f, false)
			chatMediaJoin(t, f, room.ID)
			m, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("read-wait"))
			if e != nil {
				t.Fatal(e)
			}
			vid := transientClip(t, m)
			var credentialExpiry time.Time
			if scenario == "credential_expiry" {
				// Issue an originally matching short token/session; never mutate a signed
				// credential's durable expiry behind the verifier while it is blocked.
				f.config.Now = time.Now
				f.config.AccessTTL = 3 * time.Second
				f.config.SessionTTL = 3 * time.Second
				f.issuer, e = authority.New(authoritypg.New(f.db), f.config)
				if e != nil {
					t.Fatal(e)
				}
				f.token = pgIssue(t, f.issuer, f.actor).Token
				f.otherToken = pgIssue(t, f.issuer, f.other).Token
				f = chatMediaPool(t, f, f.db)
				if e = f.chat.WithVoiceStorage(f.repo, time.Hour); e != nil {
					t.Fatal(e)
				}
				original, e := f.verifier.Authenticate(context.Background(), f.otherToken)
				if e != nil {
					t.Fatal("short original proof failed entry", e)
				}
				credentialExpiry = original.Session.ExpiresAt
			}
			ctx, cancel := context.WithTimeout(f.otherCtx, 12*time.Second)
			defer cancel()
			blockCtx, blockCancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer blockCancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: blockCtx}
			table, target := "owned_media_files", vid
			if scenario == "session" {
				table, target = "owned_authority_accounts", f.other
			}
			blocker, pid := atomicBlocker(t, locks, table, target)
			switch scenario {
			case "session":
				_, e = blocker.ExecContext(blockCtx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			case "membership":
				_, e = blocker.ExecContext(blockCtx, `UPDATE chat_members SET leave_at=clock_timestamp() WHERE account_id=$1 AND chat_room_id=$2`, f.other, room.ID)
			case "expiry":
				_, e = blocker.ExecContext(blockCtx, `UPDATE owned_chat_voice_clips SET created_at=clock_timestamp()-interval '1 hour',expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, vid)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, body, e := f.chat.OpenVoice(ctx, f.other, room.ID, vid)
				if body != nil {
					body.Close()
				}
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			if scenario == "credential_expiry" {
				organizationWaitExpired(t, blockCtx, f.db, credentialExpiry)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale voice read", scenario)
			}
		})
	}
}
func TestPGChatVoiceDurableDeletionFailureAndUnboundExpiry(t *testing.T) {
	f := newChatTransient(t)
	room := chatMediaRoom(t, f, false)
	store := &mediaFailingStore{ObjectStore: f.store, failDelete: true}
	repo, e := mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.chat.WithVoiceStorage(repo, time.Hour); e != nil {
		t.Fatal(e)
	}
	m, e := f.chat.SendVoice(f.ctx, f.actor, room.ID, transientVoice("durable-deletion"))
	if e != nil {
		t.Fatal(e)
	}
	vid := transientClip(t, m)
	if e = f.chat.DeleteMessage(f.ctx, f.actor, room.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CleanupVoices(context.Background(), 100); e == nil {
		t.Fatal("deletion failure hidden")
	}
	mediaUsage(t, f.mediaFixture, int64(len(transientVoice("").Data)), 0)
	if _, b, e := f.chat.OpenVoice(f.ctx, f.actor, room.ID, vid); e == nil {
		b.Close()
		t.Fatal("deleting clip exposed")
	}
	store.failDelete = false
	if n, e := repo.CleanupOwnedDeletions(f.ctx, f.actor, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
	sender, e := f.chat.Me(f.ctx, f.actor, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	staged, e := repo.PrepareVoice(f.ctx, f.actor, room.ID, sender, transientVoice("abandoned-after-upload"), bytes.Repeat([]byte{1}, 32), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_chat_voice_clips SET pending_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, staged.ID)
	if n, e := repo.CleanupVoices(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	mediaUsage(t, f.mediaFixture, 0, 0)
}

type transientAfterStorage struct {
	*mediapg.Repository
	after func()
}

func (s transientAfterStorage) PrepareVoice(ctx context.Context, actor, rid string, m rooms.Member, p rooms.VoiceRequest, hash []byte, ttl time.Duration) (rooms.VoiceClip, error) {
	c, e := s.Repository.PrepareVoice(ctx, actor, rid, m, p, hash, ttl)
	if e == nil {
		s.after()
	}
	return c, e
}
func TestPGChatVoiceSendRevalidatesAfterStorageAndMembershipABA(t *testing.T) {
	for _, scenario := range []string{"session", "membership_aba", "encrypted", "source_deleted", "media_grant"} {
		t.Run(scenario, func(t *testing.T) {
			f := newChatTransient(t)
			room := chatMediaRoom(t, f, false)
			sourceRoom := chatMediaRoom(t, f, false)
			source, e := f.chat.Send(f.ctx, f.actor, sourceRoom.ID, rooms.TextRequest{Content: "source"})
			if e != nil {
				t.Fatal(e)
			}
			p := transientVoice("stage-revalidate")
			p.ForwardedMessageID = &source.ID
			after := func() {
				switch scenario {
				case "session":
					query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
				case "membership_aba":
					query(t, f.db, `UPDATE chat_members SET joined_at=joined_at+interval '1 second' WHERE account_id=$1 AND chat_room_id=$2`, f.actor, room.ID)
				case "encrypted":
					query(t, f.db, `UPDATE chat_rooms SET encryption_mode=3 WHERE id=$1`, room.ID)
				case "source_deleted":
					query(t, f.db, `UPDATE chat_messages SET deleted_at=clock_timestamp() WHERE id=$1`, source.ID)
				case "media_grant":
					if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "chat.*"}}, nil); e != nil {
						t.Fatal(e)
					}
				}
			}
			if e = f.chat.WithVoiceStorage(transientAfterStorage{f.repo, after}, time.Hour); e != nil {
				t.Fatal(e)
			}
			if _, e = f.chat.SendVoice(f.ctx, f.actor, room.ID, p); e == nil {
				t.Fatal("stale send", scenario)
			}
			var messages, used, reserved int
			if e = f.db.QueryRow(`SELECT count(*) FROM chat_messages WHERE chat_room_id=$1`, room.ID).Scan(&messages); e != nil || messages != 0 {
				t.Fatal(messages, e)
			}
			if e = f.db.QueryRow(`SELECT used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, f.actor).Scan(&used, &reserved); e != nil || used != 0 || reserved != 0 {
				t.Fatal("failed bind leaked storage charge", used, reserved, e)
			}
		})
	}
}
