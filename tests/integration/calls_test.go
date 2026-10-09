// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/callpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/messager/rooms"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic provider only: no network, media, sockets, device access or live credentials.
type callProvider struct {
	mu                                 sync.Mutex
	items                              map[string][]calls.Participant
	failCreate, failRemove, failDelete bool
	created, removed, deleted, muted   int
}

func (*callProvider) Name() string           { return "synthetic" }
func (*callProvider) Endpoint() string       { return "wss://synthetic.invalid" }
func (*callProvider) StrictRevocation() bool { return true }
func (p *callProvider) EnsureRoom(_ context.Context, room string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failCreate {
		return calls.ErrProvider
	}
	p.created++
	if p.items == nil {
		p.items = map[string][]calls.Participant{}
	}
	if _, ok := p.items[room]; !ok {
		p.items[room] = []calls.Participant{}
	}
	return nil
}
func (p *callProvider) DeleteRoom(_ context.Context, room string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failDelete {
		return calls.ErrProvider
	}
	p.deleted++
	delete(p.items, room)
	return nil
}
func (p *callProvider) Participants(_ context.Context, room string) ([]calls.Participant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]calls.Participant{}, p.items[room]...), nil
}
func (p *callProvider) Remove(_ context.Context, room, id string, cutoff time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failRemove {
		return calls.ErrProvider
	}
	if cutoff.IsZero() {
		return calls.ErrInvalid
	}
	p.removed++
	out := []calls.Participant{}
	for _, v := range p.items[room] {
		if v.Identity != id {
			out = append(out, v)
		}
	}
	p.items[room] = out
	return nil
}
func (p *callProvider) Mute(_ context.Context, _, _, _ string, _ bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.muted++
	return nil
}
func (*callProvider) Token(_ context.Context, g calls.Grant) (string, error) {
	return "synthetic-only:" + g.Identity, nil
}

type callFixture struct {
	chatFixture
	repo     *callpg.Repository
	provider *callProvider
	verifier *authority.Verifier
	guard    *authoritypg.TransactionGuard
	handler  http.Handler
}

func newCallFixture(t *testing.T) callFixture {
	t.Helper()
	f := callFixture{chatFixture: newChatFixture(t), provider: &callProvider{}}
	query(t, f.db, notificationpg.Schema)
	query(t, f.db, callpg.Schema)
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), clock)
	store := authoritypg.New(f.db)
	var e error
	f.issuer, e = authority.New(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.verifier, e = authority.NewVerifier(store, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
	f.token = pgIssue(t, f.issuer, f.owner).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	f.thirdToken = pgIssue(t, f.issuer, f.third).Token
	f.repo, e = callpg.New(f.db, f.guard, f.provider, callpg.Config{NoneEncryptionMode: 77, TokenTTL: time.Minute, CallTTL: time.Hour, NotificationApp: "dev.solsynth.solian"})
	if e != nil {
		t.Fatal(e)
	}
	handler := (&calls.Service{Repository: f.repo, Authority: f.verifier}).Handler()
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, e := f.verifier.AuthenticateRequest(r); e == nil {
			r = r.WithContext(f.guard.Bind(r.Context(), p))
		}
		handler.ServeHTTP(w, r)
	})
	return f
}
func (f callFixture) ctx(t *testing.T, token string) context.Context {
	t.Helper()
	p, e := f.verifier.Authenticate(context.Background(), token)
	if e != nil {
		t.Fatal(e)
	}
	return f.guard.Bind(context.Background(), p)
}
func (f callFixture) connect(t *testing.T, c calls.Call, actor, tool string) {
	t.Helper()
	var identity string
	if e := f.db.QueryRow(`SELECT identity::text FROM call_v1.members WHERE call_id=$1 AND account_id=$2 AND tool=$3`, c.ID, actor, tool).Scan(&identity); e != nil {
		t.Fatal(e)
	}
	f.provider.mu.Lock()
	defer f.provider.mu.Unlock()
	f.provider.items[c.SessionID] = append(f.provider.items[c.SessionID], calls.Participant{Identity: identity, JoinedAt: time.Now().UTC().Truncate(time.Microsecond), TrackSID: "synthetic-audio"})
}
func countCalls(t *testing.T, f callFixture, q string, args ...any) int {
	t.Helper()
	var n int
	if e := f.db.QueryRow(q, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestPGCallsLifecycleAuthorityAndReplay(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	owner := f.ctx(t, f.token)
	other := f.ctx(t, f.otherToken)
	base := "/api/chat/realtime/" + room.ID
	chatCall(t, f.handler, "POST", base, f.otherToken, "{}", 403)
	chatJoin(t, f.chatFixture, room.ID, f.other)
	w := chatCall(t, f.handler, "POST", base, f.token, "{}", 200)
	var c calls.Call
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	if c.State != "active" || c.ID == "" || c.SessionID == "" || c.Sender == nil || c.Sender.AccountID != f.owner {
		t.Fatal(c)
	}
	w = chatCall(t, f.handler, "GET", base, f.otherToken, "", 200)
	var same calls.Call
	json.Unmarshal(w.Body.Bytes(), &same)
	if same.ID != c.ID || same.Sender.AccountID != f.owner {
		t.Fatal("call or sender changed", same)
	}
	if _, e := f.repo.Start(context.Background(), f.owner, room.ID); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound proof accepted", e)
	}
	joined, e := f.repo.Join(other, f.other, room.ID, "")
	if e != nil || joined.IsAdmin || len(joined.Participants) != 0 || joined.Token == "" {
		t.Fatal(joined, e)
	}
	if _, e = f.repo.Join(other, f.other, room.ID, ""); e != nil {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.events WHERE call_id=$1 AND type='owned.call.admitted'`, c.ID); n != 1 {
		t.Fatal("duplicate admission", n)
	}
	f.connect(t, c, f.other, "")
	participants, e := f.repo.Participants(owner, f.owner, room.ID)
	if e != nil || len(participants) != 1 || participants[0].AccountID != f.other || participants[0].Profile == nil {
		t.Fatal(participants, e)
	}
	if e = f.repo.Mute(other, f.other, room.ID, f.owner, true); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal(e)
	}
	if e = f.repo.Mute(owner, f.owner, room.ID, f.other, true); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Invite(owner, f.owner, room.ID, f.other); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Invite(owner, f.owner, room.ID, f.other); e != nil {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.events WHERE call_id=$1 AND type='owned.call.invited'`, c.ID); n != 1 {
		t.Fatal("invite duplicated", n)
	}
	if _, e = f.repo.ProjectEvents(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM notification_v1.events WHERE audience_id=$1 AND account_id=$2 AND type='owned.call.invited'`, room.ID, f.other); n != 1 {
		t.Fatal(n)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM notification_v1.events WHERE audience_id=$1 AND account_id=$2 AND type='owned.call.invited'`, room.ID, f.owner); n != 0 {
		t.Fatal("invitation leaked", n)
	}
	if e = f.repo.Leave(other, f.other, room.ID, ""); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Leave(other, f.other, room.ID, ""); e != nil {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.events WHERE call_id=$1 AND type='owned.call.member.left'`, c.ID); n != 1 {
		t.Fatal("duplicate leave", n)
	}
	if e = f.repo.End(other, f.other, room.ID); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal(e)
	}
	chatCall(t, f.handler, "POST", base+"/end", f.token, "{}", 204)
	chatCall(t, f.handler, "POST", base+"/end", f.token, "{}", 204)
	next, e := f.repo.Start(owner, f.owner, room.ID)
	if e != nil || next.ID == c.ID || next.SessionID == c.SessionID {
		t.Fatal("session reused", next, e)
	}
}
func TestPGCallsRemovalFailureBanAndRetry(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	chatJoin(t, f.chatFixture, room.ID, f.other)
	owner := f.ctx(t, f.token)
	other := f.ctx(t, f.otherToken)
	c, e := f.repo.Start(owner, f.owner, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Join(other, f.other, room.ID, ""); e != nil {
		t.Fatal(e)
	}
	f.connect(t, c, f.other, "")
	f.provider.failRemove = true
	if e = f.repo.Kick(owner, f.owner, room.ID, f.other, calls.KickRequest{BanDurationMinutes: 10}); !errors.Is(e, calls.ErrProvider) {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.members WHERE call_id=$1 AND state='removing' AND revoked_at IS NULL`, c.ID); n != 1 {
		t.Fatal("premature completion", n)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.events WHERE call_id=$1 AND type='owned.call.member.left'`, c.ID); n != 0 {
		t.Fatal("premature event", n)
	}
	pending, pe := f.repo.Participants(owner, f.owner, room.ID)
	if pe != nil || len(pending) != 1 || pending[0].State != "revocation_pending" {
		t.Fatal("pending provider presence hidden", pending, pe)
	}
	if _, e = f.repo.Join(other, f.other, room.ID, ""); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal("ban bypass", e)
	}
	var until time.Time
	f.db.QueryRow(`SELECT ban_until FROM call_v1.members WHERE call_id=$1`, c.ID).Scan(&until)
	f.provider.failRemove = false
	if e = f.repo.Kick(owner, f.owner, room.ID, f.other, calls.KickRequest{BanDurationMinutes: 10}); e != nil {
		t.Fatal(e)
	}
	var after time.Time
	f.db.QueryRow(`SELECT ban_until FROM call_v1.members WHERE call_id=$1`, c.ID).Scan(&after)
	if !until.Equal(after) {
		t.Fatal("retry extended ban")
	}
	if _, e = f.repo.Join(other, f.other, room.ID, "another-tool"); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal("tool bypassed timeout", e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.members WHERE call_id=$1 AND state='left' AND revoked_at IS NOT NULL`, c.ID); n != 1 {
		t.Fatal(n)
	}
	f.provider.failDelete = true
	if e = f.repo.End(owner, f.owner, room.ID); !errors.Is(e, calls.ErrProvider) {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE id=$1 AND state='ending' AND ended_at IS NULL`, c.ID); n != 1 {
		t.Fatal(n)
	}
	f.provider.failDelete = false
	if _, e = f.repo.ProcessDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE id=$1 AND state='ended' AND ended_at IS NOT NULL`, c.ID); n != 1 {
		t.Fatal(n)
	}
	if _, e = f.repo.Start(owner, f.owner, room.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Join(other, f.other, room.ID, ""); !errors.Is(e, calls.ErrForbidden) {
		t.Fatal("new call bypassed active timeout", e)
	}
}
func TestPGCallsConcurrentStartJoinAndRestart(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	ctx := f.ctx(t, f.token)
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c, e := f.repo.Start(ctx, f.owner, room.ID); ids <- c.ID; errs <- e }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for got := range ids {
		if id == "" {
			id = got
		}
		if got != id {
			t.Fatal("split call", id, got)
		}
	}
	joinErrors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.repo.Join(ctx, f.owner, room.ID, ""); joinErrors <- e }()
	}
	wg.Wait()
	close(joinErrors)
	for e := range joinErrors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.events WHERE call_id=$1 AND type='owned.call.admitted'`, id); n != 1 {
		t.Fatal("concurrent join duplicated admission", n)
	}
	repo, e := callpg.New(f.db, f.guard, f.provider, callpg.Config{NoneEncryptionMode: 77, TokenTTL: time.Minute, CallTTL: time.Hour, NotificationApp: "dev.solsynth.solian"})
	if e != nil {
		t.Fatal(e)
	}
	resumed, e := repo.Join(ctx, f.owner, room.ID, "")
	if e != nil || resumed.CallID != id {
		t.Fatal(resumed, e)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE room_id=$1 AND ended_at IS NULL`, room.ID); n != 1 {
		t.Fatal(n)
	}
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.members WHERE call_id=$1`, id); n != 1 {
		t.Fatal(n)
	}
}
func TestPGCallsCanonicalRemovalSessionRevocationAndExpiry(t *testing.T) {
	for _, kind := range []string{"member", "session", "account", "expiry", "room"} {
		t.Run(kind, func(t *testing.T) {
			f := newCallFixture(t)
			room := createChat(t, f.chatFixture, true)
			chatJoin(t, f.chatFixture, room.ID, f.other)
			owner := f.ctx(t, f.token)
			other := f.ctx(t, f.otherToken)
			c, e := f.repo.Start(owner, f.owner, room.ID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.repo.Join(other, f.other, room.ID, ""); e != nil {
				t.Fatal(e)
			}
			f.connect(t, c, f.other, "")
			switch kind {
			case "member":
				_, e = f.chatFixture.repo.ChangeMember(context.Background(), f.owner, room.ID, rooms.Remove, f.other)
			case "session":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			case "account":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			case "expiry":
				query(t, f.db, `UPDATE call_v1.calls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, c.ID)
			case "room":
				e = f.chatFixture.repo.Delete(context.Background(), f.owner, room.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind == "member" && countCalls(t, f, `SELECT count(*) FROM call_v1.members WHERE call_id=$1 AND state='removing'`, c.ID) != 1 {
				t.Fatal("canonical mutation did not queue removal")
			}
			if _, e = f.repo.ProcessDue(context.Background(), 100); e != nil {
				t.Fatal(e)
			}
			if n := countCalls(t, f, `SELECT count(*) FROM call_v1.members WHERE call_id=$1 AND state='left' AND revoked_at IS NOT NULL`, c.ID); n != 1 {
				t.Fatal(kind, n)
			}
			if kind == "expiry" || kind == "room" {
				if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE id=$1 AND state='ended'`, c.ID); n != 1 {
					t.Fatal(kind, n)
				}
			}
		})
	}
}
func TestPGCallsProviderPreparationRecoveryAndHTTPBoundaries(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	ctx := f.ctx(t, f.token)
	base := "/api/chat/realtime/" + room.ID
	f.provider.failCreate = true
	chatCall(t, f.handler, "POST", base, f.token, "{}", 503)
	if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE room_id=$1 AND state='preparing'`, room.ID); n != 1 {
		t.Fatal(n)
	}
	f.provider.failCreate = false
	chatCall(t, f.handler, "POST", base, f.token, "{}", 200)
	for _, path := range []string{base + "/join?token=bad", base + "/join?tool=a&tool=b", base + "/join?tool=a%2Fb", base + "/participants?tool=a"} {
		chatCall(t, f.handler, "GET", path, f.token, "", 400)
	}
	chatCall(t, f.handler, "POST", base+"/kick/"+f.other, f.token, `{"ban_duration_minutes":1,"ban_duration_minutes":2}`, 400)
	chatCall(t, f.handler, "POST", base, f.token, `{"provider":"client-selected"}`, 400)
	chatCall(t, f.handler, "HEAD", base, f.token, "", 405)
	chatCall(t, f.handler, "GET", base, "", "", 401)
	nilRepo, e := callpg.New(f.db, f.guard, nil, callpg.Config{NoneEncryptionMode: 77, TokenTTL: time.Minute, CallTTL: time.Hour, NotificationApp: "dev.solsynth.solian"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = nilRepo.Join(ctx, f.owner, room.ID, ""); !errors.Is(e, calls.ErrUnconfigured) {
		t.Fatal(e)
	}
	// Forging identity headers is denied even alongside a valid bearer credential.
	r, _ := http.NewRequest("GET", "https://synthetic.invalid"+base, nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("X-Account-ID", f.other)
	recorder := chatCallRecorder(f.handler, r)
	if recorder != 403 {
		t.Fatal(recorder)
	}
}

// No listener is opened; requests execute entirely through ResponseRecorder.
func chatCallRecorder(h http.Handler, r *http.Request) int {
	w := &statusRecorder{header: http.Header{}}
	h.ServeHTTP(w, r)
	return w.status
}

type statusRecorder struct {
	header http.Header
	status int
}

func (w *statusRecorder) Header() http.Header { return w.header }
func (w *statusRecorder) WriteHeader(n int)   { w.status = n }
func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return len(b), nil
}

func TestPGCallsWaiterObservesCommittedMembershipAndSessionRevocation(t *testing.T) {
	for _, kind := range []string{"membership", "session"} {
		t.Run(kind, func(t *testing.T) {
			f := newCallFixture(t)
			room := createChat(t, f.chatFixture, true)
			chatJoin(t, f.chatFixture, room.ID, f.other)
			original := f.ctx(t, f.otherToken)
			tx, e := f.db.BeginTx(context.Background(), nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if kind == "membership" {
				_, e = tx.Exec(`SELECT 1 FROM chat_rooms WHERE id=$1 FOR UPDATE`, room.ID)
			} else {
				_, e = tx.Exec(`SELECT 1 FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.other)
			}
			if e != nil {
				t.Fatal(e)
			}
			ready := make(chan struct{})
			done := make(chan error, 1)
			go func() { close(ready); _, e := f.repo.Join(original, f.other, room.ID, ""); done <- e }()
			<-ready
			select {
			case e := <-done:
				t.Fatal("operation escaped held lock", e)
			case <-time.After(25 * time.Millisecond):
			}
			if kind == "membership" {
				_, e = tx.Exec(`UPDATE chat_members SET leave_at=clock_timestamp() WHERE chat_room_id=$1 AND account_id=$2`, room.ID, f.other)
			} else {
				_, e = tx.Exec(`UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			select {
			case e := <-done:
				if kind == "membership" && !errors.Is(e, calls.ErrForbidden) || kind == "session" && !errors.Is(e, authority.ErrRevoked) {
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("call waiter did not finish")
			}
			if n := countCalls(t, f, `SELECT count(*) FROM call_v1.calls WHERE room_id=$1`, room.ID); n != 0 {
				t.Fatal("revoked waiter persisted call", n)
			}
		})
	}
}
