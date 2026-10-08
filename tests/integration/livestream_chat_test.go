// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type streamPacket struct {
	Room       string
	Recipients []string
	Payload    []byte
}
type streamChatProvider struct {
	*streamProvider
	dataMu      sync.Mutex
	packets     []streamPacket
	fail        bool
	afterAccept func()
}

func (p *streamChatProvider) SendStreamData(_ context.Context, room string, recipients []string, payload []byte) error {
	p.dataMu.Lock()
	defer p.dataMu.Unlock()
	if p.fail {
		return live.ErrProvider
	}
	if len(recipients) == 0 {
		return live.ErrInvalid
	}
	p.packets = append(p.packets, streamPacket{room, append([]string(nil), recipients...), append([]byte(nil), payload...)})
	if p.afterAccept != nil {
		p.afterAccept()
	}
	return nil
}
func (p *streamChatProvider) snapshot() []streamPacket {
	p.dataMu.Lock()
	defer p.dataMu.Unlock()
	return append([]streamPacket(nil), p.packets...)
}

type streamChatFixture struct {
	streamFixture
	data *streamChatProvider
}

func chatStreamConfig() livestreampg.ChatConfig {
	return livestreampg.ChatConfig{MaxMessagesPerStream: 100, MaxEventsPerStream: 200, MaxAttempts: 3, EventTTL: time.Hour, SendTimeout: time.Second}
}
func newStreamChatFixture(t *testing.T) streamChatFixture {
	t.Helper()
	f := streamChatFixture{streamFixture: newStreamFixture(t)}
	query(t, f.db, livestreampg.ChatSchema)
	f.data = &streamChatProvider{streamProvider: f.provider}
	var e error
	f.repo, e = livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.data, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.WithChat(chatStreamConfig()); e != nil {
		t.Fatal(e)
	}
	f.handler = (&live.Service{Repository: f.repo, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	if e = f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func streamChatStart(t *testing.T, f streamChatFixture, key string) live.Stream {
	t.Helper()
	s := streamCreate(t, f.streamFixture, key)
	streamNative(t, f.streamFixture, s.ID)
	return s
}
func streamChatSend(t *testing.T, f streamChatFixture, ctx context.Context, actor, sid, key string) live.ChatMessage {
	t.Helper()
	m, e := f.repo.SendChat(ctx, actor, sid, "Synthetic chat message", key)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func chatEventState(t *testing.T, f streamChatFixture, id string) string {
	t.Helper()
	var state string
	if e := f.db.QueryRow(`SELECT state FROM owned_livestream_chat_outbox WHERE id=$1`, id).Scan(&state); e != nil {
		t.Fatal(e)
	}
	return state
}
func chatForceDue(t *testing.T, f streamChatFixture, sid string) {
	t.Helper()
	query(t, f.db, `UPDATE owned_livestream_chat_outbox SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1 AND state='pending'`, sid)
}
func chatProcess(t *testing.T, f streamChatFixture) {
	t.Helper()
	if _, e := f.repo.ProcessChatDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
}

func TestPGLiveStreamChatHTTPHistoryAndIdempotency(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-http")
	base := "/api/livestreams/" + s.ID + "/chat"
	streamGetCall(t, f.streamFixture, base, "", 401)
	req := fixtureJSONRequest(t, "POST", base, f.thirdToken, map[string]any{"content": "Synthetic HTTP chat"})
	req.Header.Set("Idempotency-Key", "synthetic-http-message")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var m live.ChatMessage
	if e := json.Unmarshal(w.Body.Bytes(), &m); e != nil {
		t.Fatal(e)
	}
	if m.Sender == nil || m.Sender.ID != f.third || m.SenderID != f.third || m.LiveStreamID != s.ID || m.DeliveryState != "pending" || m.TimeoutUntil != nil || m.DeletedAt != nil {
		t.Fatal(m)
	}
	rows, e := f.repo.ListChat(f.ownerCtx, f.owner, s.ID, 50, 0)
	if e != nil || len(rows) != 1 || rows[0].ID != m.ID || !rows[0].CreatedAt.Equal(m.CreatedAt) {
		t.Fatal(rows, e)
	}
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, x := f.repo.SendChat(f.viewerCtx, f.third, s.ID, "Synthetic HTTP chat", "synthetic-http-message")
			ids <- v.ID
			errs <- x
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for id := range ids {
		if id != m.ID {
			t.Fatal("duplicate message", id)
		}
	}
	if _, e = f.repo.SendChat(f.viewerCtx, f.third, s.ID, "Different payload", "synthetic-http-message"); !errors.Is(e, live.ErrConflict) {
		t.Fatal(e)
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_chat_messages WHERE stream_id=$1`, s.ID); n != 1 {
		t.Fatal(n)
	}
	for _, path := range []string{base + "?limit=101", base + "?sender_id=" + f.owner} {
		streamGetCall(t, f.streamFixture, path, f.token, 400)
	}
	backendJSONCall(t, f.handler, "POST", base, f.thirdToken, map[string]any{"sender_id": f.owner, "content": "forged"}, 400)
	if _, e = f.repo.SendChat(f.ctx, f.third, s.ID, "unbound", "unbound"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("caller string accepted without original proof", e)
	}
}

func TestPGLiveStreamChatTimeoutDeleteAndExpiry(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-moderation")
	m := streamChatSend(t, f, f.viewerCtx, f.third, s.ID, "first")
	if e := f.repo.TimeoutChat(f.viewerCtx, f.third, s.ID, m.ID, "not-editor", live.ChatTimeout{}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal(e)
	}
	if e := f.repo.TimeoutChat(f.editorCtx, f.other, s.ID, m.ID, "timeout", live.ChatTimeout{}); e != nil {
		t.Fatal(e)
	}
	var deadline time.Time
	if e := f.db.QueryRow(`SELECT timeout_until FROM owned_livestream_chat_messages WHERE id=$1`, m.ID).Scan(&deadline); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.TimeoutChat(f.editorCtx, f.other, s.ID, m.ID, "timeout", live.ChatTimeout{}); e != nil {
		t.Fatal(e)
	}
	var replay time.Time
	if e := f.db.QueryRow(`SELECT timeout_until FROM owned_livestream_chat_messages WHERE id=$1`, m.ID).Scan(&replay); e != nil || !replay.Equal(deadline) {
		t.Fatal("retry extended timeout", replay, deadline, e)
	}
	if _, e := f.repo.SendChat(f.viewerCtx, f.third, s.ID, "muted", "muted"); !errors.Is(e, live.ErrChatMuted) {
		t.Fatal(e)
	}
	if e := f.repo.DeleteChat(f.editorCtx, f.other, s.ID, m.ID); e != nil {
		t.Fatal(e)
	}
	rows, e := f.repo.ListChat(f.ownerCtx, f.owner, s.ID, 20, 0)
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
	if chatEventState(t, f, m.ID) != "cancelled" {
		t.Fatal("deleted message queued")
	}
	if _, e = f.repo.SendChat(f.viewerCtx, f.third, s.ID, "still muted", "muted-again"); !errors.Is(e, live.ErrChatMuted) {
		t.Fatal("deleted timeout anchor stopped muting", e)
	}
	zero := 0
	if e = f.repo.TimeoutChat(f.editorCtx, f.other, s.ID, m.ID, "release", live.ChatTimeout{DurationMinutes: &zero}); e != nil {
		t.Fatal(e)
	}
	streamChatSend(t, f, f.viewerCtx, f.third, s.ID, "after-expiry")
	other := streamChatStart(t, f, "chat-isolation")
	if e = f.repo.DeleteChat(f.ownerCtx, f.owner, other.ID, m.ID); !errors.Is(e, live.ErrNotFound) {
		t.Fatal("cross-stream message changed", e)
	}
	if _, e = f.accounts.ChangeMember(f.ownerCtx, f.owner, f.pub.ID, accounts.MemberChange{Action: accounts.RemoveMember, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.TimeoutChat(f.editorCtx, f.other, s.ID, m.ID, "revoked-editor", live.ChatTimeout{}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal(e)
	}
}

func TestPGLiveStreamChatOutboxRetryRecipientsAndRecordedRevocation(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-outbox")
	owner, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false)
	if e != nil {
		t.Fatal(e)
	}
	viewer, e := f.repo.Token(f.viewerCtx, f.third, s.ID, false, false)
	if e != nil {
		t.Fatal(e)
	}
	m := streamChatSend(t, f, f.editorCtx, f.other, s.ID, "outbox-first")
	f.data.fail = true
	if _, e = f.repo.ProcessChatDue(f.ctx, 100); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if chatEventState(t, f, m.ID) != "pending" || len(f.data.snapshot()) != 0 {
		t.Fatal("failed call reported accepted")
	}
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.third, []authority.Rule{{Pattern: "*"}, {Pattern: "livestreams.view", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	f.data.fail = false
	chatForceDue(t, f, s.ID)
	chatProcess(t, f)
	packets := f.data.snapshot()
	if len(packets) != 1 || len(packets[0].Recipients) != 1 || packets[0].Recipients[0] != owner.Identity || packets[0].Recipients[0] == viewer.Identity {
		t.Fatal(packets)
	}
	if chatEventState(t, f, m.ID) != "accepted" {
		t.Fatal("ack missing")
	}
	chatProcess(t, f)
	if len(f.data.snapshot()) != 1 {
		t.Fatal("accepted packet retried")
	}
	var wire live.ChatMessage
	if e = json.Unmarshal(packets[0].Payload, &wire); e != nil || wire.ID != m.ID || wire.DeliveryState != "" {
		t.Fatal(wire, e)
	}
	cancelled := streamChatSend(t, f, f.editorCtx, f.other, s.ID, "revoked-sender")
	if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}, {Pattern: "livestreams.chat.send", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	chatProcess(t, f)
	if chatEventState(t, f, cancelled.ID) != "cancelled" || len(f.data.snapshot()) != 1 {
		t.Fatal("withdrawn actor permission broadcast")
	}
	if _, e = f.repo.SendChat(f.editorCtx, f.other, s.ID, "denied", "denied"); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal(e)
	}
}

func TestPGLiveStreamChatRollbackPacketBoundsAndGeneration(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-rollback")
	constraint := "synthetic_chat_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(actor_id='`+f.owner+`'::uuid AND action='livestreams.chat.send'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.SendChat(f.ownerCtx, f.owner, s.ID, "must roll back", "rollback"); e == nil {
		t.Fatal("injected audit failure passed")
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_chat_messages WHERE stream_id=$1`, s.ID); n != 0 {
		t.Fatal(n)
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_chat_outbox WHERE stream_id=$1`, s.ID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	if _, e := f.repo.SendChat(f.ownerCtx, f.owner, s.ID, strings.Repeat("😀", 4096), "oversize"); !errors.Is(e, live.ErrChatPacket) {
		t.Fatal("serialized packet bound", e)
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_chat_messages WHERE stream_id=$1`, s.ID); n != 0 {
		t.Fatal(n)
	}
	m := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "generation")
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	streamNative(t, f.streamFixture, s.ID)
	if _, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false); e != nil {
		t.Fatal(e)
	}
	chatProcess(t, f)
	if chatEventState(t, f, m.ID) != "cancelled" || len(f.data.snapshot()) != 0 {
		t.Fatal("old generation packet leaked")
	}
}

func TestPGLiveStreamChatVisibilityAndDisableDuringLockWait(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-visibility")
	m := streamChatSend(t, f, f.viewerCtx, f.third, s.ID, "visibility")
	if _, e := f.social.ChangeRelationship(f.ownerCtx, f.owner, f.third, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.ListChat(f.viewerCtx, f.third, s.ID, 10, 0); !errors.Is(e, live.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := f.repo.SendChat(f.viewerCtx, f.third, s.ID, "blocked", "blocked"); !errors.Is(e, live.ErrForbidden) {
		t.Fatal(e)
	}
	chatProcess(t, f)
	if chatEventState(t, f, m.ID) != "cancelled" {
		t.Fatal("blocked sender queued")
	}
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.other}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.other)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.other); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.SendChat(f.editorCtx, f.other, s.ID, "waited", "waited"); done <- e }()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrDisabled) && !errors.Is(e, authority.ErrStale) {
		t.Fatal(e)
	}
	if n := countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_chat_messages WHERE stream_id=$1 AND sender_id=$2`, s.ID, f.other); n != 0 {
		t.Fatal(n)
	}
}

func TestPGLiveStreamChatRetryLimitExpiryAndUnconfigured(t *testing.T) {
	base := newStreamFixture(t)
	if _, e := base.repo.ListChat(base.ownerCtx, base.owner, "00000000-0000-4000-8000-000000000001", 10, 0); !errors.Is(e, live.ErrUnsupported) {
		t.Fatal(e)
	}
	if e := base.repo.WithChat(chatStreamConfig()); !errors.Is(e, live.ErrUnconfigured) {
		t.Fatal("non-data provider enabled chat", e)
	}
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-bounds")
	if _, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false); e != nil {
		t.Fatal(e)
	}
	m := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "retry-limit")
	f.data.fail = true
	for i := 0; i < 3; i++ {
		chatForceDue(t, f, s.ID)
		if _, e := f.repo.ProcessChatDue(f.ctx, 100); !errors.Is(e, live.ErrProvider) {
			t.Fatal(i, e)
		}
	}
	if chatEventState(t, f, m.ID) != "failed" {
		t.Fatal("unbounded retries")
	}
	f.data.fail = false
	expired := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "expired")
	query(t, f.db, `UPDATE owned_livestream_chat_outbox SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, expired.ID)
	chatProcess(t, f)
	if chatEventState(t, f, expired.ID) != "cancelled" || len(f.data.snapshot()) != 0 {
		t.Fatal("expired event reached provider")
	}
	if _, e := f.repo.ProcessChatDue(f.ctx, 101); !errors.Is(e, live.ErrInvalid) {
		t.Fatal(e)
	}
}

func TestPGLiveStreamChatOutboxRestartPersistence(t *testing.T) {
	database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-livestream-chat-restart.json")
	type state struct{ Stream, Message, Endpoint, Identity string }
	var saved state
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newStreamChatFixture(t)
		s := streamChatStart(t, f, "chat-restart")
		admission, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false)
		if e != nil {
			t.Fatal(e)
		}
		m := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "restart-pending")
		saved = state{s.ID, m.ID, f.provider.Endpoint(), admission.Identity}
		raw, _ := json.Marshal(saved)
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("missing synthetic chat state", e)
	}
	f := newStreamChatFixture(t)
	f.provider.endpoint = saved.Endpoint
	chatForceDue(t, f, saved.Stream)
	// ProcessDue is the installed host hook; enabled chat is not an orphan callback.
	if _, e = f.repo.ProcessDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	packets := f.data.snapshot()
	if len(packets) != 1 || len(packets[0].Recipients) != 1 || packets[0].Recipients[0] != saved.Identity || chatEventState(t, f, saved.Message) != "accepted" {
		t.Fatal(packets)
	}
	var m live.ChatMessage
	if e = json.Unmarshal(packets[0].Payload, &m); e != nil || m.ID != saved.Message {
		t.Fatal(m, e)
	}
	if _, e = f.repo.ProcessDue(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	if len(f.data.snapshot()) != 1 {
		t.Fatal("accepted event replayed after repository reconstruction")
	}
}

func TestPGLiveStreamChatProviderAcceptanceRollbackAndFIFO(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-ack-rollback")
	if _, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false); e != nil {
		t.Fatal(e)
	}
	first := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "first")
	second := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "second")
	constraint := "synthetic_chat_ack_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_chat_outbox ADD CONSTRAINT `+constraint+` CHECK(NOT(id='`+first.ID+`'::uuid AND state='accepted'))NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_livestream_chat_outbox DROP CONSTRAINT IF EXISTS ` + constraint)
	})
	if _, e := f.repo.ProcessChatDue(f.ctx, 100); e == nil {
		t.Fatal("provider acknowledgement DB rollback not injected")
	}
	if len(f.data.snapshot()) != 1 || chatEventState(t, f, first.ID) != "pending" || chatEventState(t, f, second.ID) != "pending" {
		t.Fatal("rollback states")
	}
	chatProcess(t, f)
	if len(f.data.snapshot()) != 1 {
		t.Fatal("later event bypassed first backoff")
	}
	query(t, f.db, `ALTER TABLE owned_livestream_chat_outbox DROP CONSTRAINT `+constraint)
	chatForceDue(t, f, s.ID)
	chatProcess(t, f)
	chatProcess(t, f)
	packets := f.data.snapshot()
	if len(packets) != 3 {
		t.Fatal(len(packets))
	}
	var ids []string
	for _, p := range packets {
		var m live.ChatMessage
		if e := json.Unmarshal(p.Payload, &m); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, m.ID)
	}
	if ids[0] != first.ID || ids[1] != first.ID || ids[2] != second.ID || chatEventState(t, f, second.ID) != "accepted" {
		t.Fatal("stable retries or FIFO lost", ids)
	}
}

func TestPGLiveStreamChatOpposingModeratorsAndParentRevocation(t *testing.T) {
	f := newStreamChatFixture(t)
	s := streamChatStart(t, f, "chat-opposing")
	a := streamChatSend(t, f, f.ownerCtx, f.owner, s.ID, "owner")
	b := streamChatSend(t, f, f.editorCtx, f.other, s.ID, "editor")
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		<-start
		done <- f.repo.TimeoutChat(f.ownerCtx, f.owner, s.ID, b.ID, "owner-moderates", live.ChatTimeout{})
	}()
	go func() {
		<-start
		done <- f.repo.TimeoutChat(f.editorCtx, f.other, s.ID, a.ID, "editor-moderates", live.ChatTimeout{})
	}()
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-f.ctx.Done():
			t.Fatal("opposing moderator account lock inversion")
		}
	}
	if _, e := f.repo.Token(f.ownerCtx, f.owner, s.ID, true, false); e != nil {
		t.Fatal(e)
	}
	// A previously recorded viewer admission may have a trusted historic parent;
	// revocation checks must traverse it without granting any new child capability.
	viewer, e := f.repo.Token(f.viewerCtx, f.third, s.ID, false, false)
	if e != nil {
		t.Fatal(e)
	}
	parent := pgIssue(t, f.issuer, f.third)
	pp, e := f.verifier.Authenticate(f.ctx, parent.Token)
	if e != nil {
		t.Fatal(e)
	}
	child, e := f.verifier.Authenticate(f.ctx, f.thirdToken)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$2 WHERE id=$1`, child.Session.ID, pp.Session.ID)
	query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, pp.Session.ID)
	// Drain the four stable events; every packet must exclude the expired parent.
	for i := 0; i < 4; i++ {
		chatProcess(t, f)
	}
	packets := f.data.snapshot()
	if len(packets) != 4 {
		t.Fatal("queued message/timeout events missing", len(packets))
	}
	for _, packet := range packets {
		for _, id := range packet.Recipients {
			if id == viewer.Identity {
				t.Fatal("expired parent received queued data")
			}
		}
	}
}
