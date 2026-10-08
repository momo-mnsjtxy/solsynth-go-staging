// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type notificationProof struct{}
type notificationIdentity struct{ v *authority.Verifier }

func (i notificationIdentity) Account(r *http.Request) (string, error) {
	p, ok := r.Context().Value(notificationProof{}).(authority.Principal)
	if !ok {
		return "", authority.ErrInvalid
	}
	p, e := i.v.AuthenticatePrincipal(r.Context(), p)
	return p.Account.ID, e
}

type notificationFixture struct {
	db                                   *sql.DB
	repo                                 *notificationpg.Repository
	guard                                *authoritypg.TransactionGuard
	v                                    *authority.Verifier
	issuer                               *authority.Service
	actor, other, token, otherToken, app string
	ctx, otherCtx                        context.Context
}

func newNotificationFixture(t *testing.T) notificationFixture {
	t.Helper()
	db := database(t)
	query(t, db, authoritypg.Schema)
	query(t, db, notificationpg.Schema)
	f := notificationFixture{db: db, actor: authorityUUID(t), other: authorityUUID(t), app: "synthetic." + strings.ReplaceAll(authorityUUID(t), "-", "")}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	cfg := authorityConfig(t, authorityKeys(t), now)
	for _, a := range []string{f.actor, f.other} {
		if e := authoritypg.SeedSynthetic(context.Background(), db, a, "synthetic_notification_"+strings.ReplaceAll(a, "-", ""), time.Now().UTC()); e != nil {
			t.Fatal(e)
		}
	}
	var e error
	f.issuer, e = authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.v, e = authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.v)
	f.repo = notificationpg.New(db, f.guard, f.app)
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	bind := func(token string) context.Context {
		p, e := f.v.Authenticate(context.Background(), token)
		if e != nil {
			t.Fatal(e)
		}
		return f.guard.Bind(context.Background(), p)
	}
	f.ctx = bind(f.token)
	f.otherCtx = bind(f.otherToken)
	return f
}
func (f notificationFixture) middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, e := f.v.AuthenticateRequest(r)
		if e != nil {
			notifications.WriteError(w, e)
			return
		}
		ctx := f.guard.Bind(context.WithValue(r.Context(), notificationProof{}, p), p)
		copy := r.Clone(ctx)
		copy.Header.Del("Authorization")
		h.ServeHTTP(w, copy)
	})
}
func notificationCommand(f notificationFixture, key string) notifications.Command {
	return notifications.Command{Key: key, AccountID: f.actor, AppID: f.app, Topic: "owned.integration", Title: "Synthetic notification", Content: "No real recipient or provider", Meta: json.RawMessage(`{"action_uri":"/synthetic"}`)}
}
func notificationEnqueue(t *testing.T, f notificationFixture, c notifications.Command) string {
	t.Helper()
	id, e := f.repo.Enqueue(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func notificationDrain(t *testing.T, fn func() (int, error)) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		n, e := fn()
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			return
		}
	}
	t.Fatal("synthetic projector did not drain")
}

func TestPGNotificationsInboxOutboxIsolation(t *testing.T) {
	f := newNotificationFixture(t)
	c := notificationCommand(f, "one")
	id := notificationEnqueue(t, f, c)
	if same := notificationEnqueue(t, f, c); same != id {
		t.Fatal("idempotency")
	}
	c.Content = "conflicting"
	if _, e := f.repo.Enqueue(context.Background(), c); !errors.Is(e, notifications.ErrConflict) {
		t.Fatal(e)
	}
	second := notificationEnqueue(t, f, notificationCommand(f, "two"))
	if n, e := f.repo.ProcessPending(context.Background(), 100); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if n, e := f.repo.ProcessPending(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if n, e := f.repo.Unread(f.ctx, f.actor, f.app); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if n, e := f.repo.Unread(f.otherCtx, f.other, f.app); e != nil || n != 0 {
		t.Fatal("account leak", n, e)
	}
	if e := f.repo.MarkRead(f.otherCtx, f.other, f.app, id); !errors.Is(e, notifications.ErrNotFound) {
		t.Fatal("foreign read", e)
	}
	if _, e := f.repo.Unread(f.ctx, f.actor, "other.app"); !errors.Is(e, notifications.ErrInvalid) {
		t.Fatal("app leak", e)
	}
	page, total, e := f.repo.List(f.ctx, f.actor, f.app, 1, 0)
	if e != nil || total != 2 || len(page) != 1 || page[0].ViewedAt != nil {
		t.Fatal(page, total, e)
	}
	if n, e := f.repo.Unread(f.ctx, f.actor, f.app); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	repeat, _, e := f.repo.List(f.ctx, f.actor, f.app, 1, 0)
	if e != nil || repeat[0].ViewedAt == nil {
		t.Fatal("read not durable", e)
	}
	events, e := f.repo.Replay(f.ctx, f.actor, f.app, 0, 100)
	if e != nil || len(events.Events) != 2 || events.Events[0].Sequence != 1 || events.Events[1].Sequence != 2 {
		t.Fatal(events, e)
	}
	if e = f.repo.Dismiss(f.ctx, f.actor, f.app, second); e != nil {
		t.Fatal(e)
	}
	events, e = f.repo.Replay(f.ctx, f.actor, f.app, 0, 100)
	if e != nil || len(events.Events) != 1 || events.Cursor != 2 {
		t.Fatal("dismiss replay filtering", events, e)
	}
	h := f.middleware(notifications.HTTP{Repository: f.repo, Identity: notificationIdentity{f.v}, App: f.app}.Handler())
	backendCall(t, h, "POST", "/api/notifications/all/read", f.token, "", 204)
	backendCall(t, h, "GET", "/metoer/notifications?take=5", f.token, "", 200)
	backendCall(t, h, "GET", "/api/notifications/count", f.token, "", 200)
	if e = f.issuer.Revoke(context.Background(), f.token, "current"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Replay(f.ctx, f.actor, f.app, 0, 1); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked proof replay", e)
	}
	if e = f.repo.Dismiss(f.ctx, f.actor, f.app, id); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked mutation", e)
	}
}

type notificationSender struct {
	calls int
	keys  []string
	err   error
}

func (s *notificationSender) Send(ctx context.Context, d notifications.Delivery) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing deadline")
	}
	s.calls++
	s.keys = append(s.keys, d.Key)
	return s.err
}
func TestPGNotificationsDeliveryRetryAndLeaseRecovery(t *testing.T) {
	f := newNotificationFixture(t)
	c := notificationCommand(f, "external")
	c.External = true
	id := notificationEnqueue(t, f, c)
	if _, e := f.repo.ProcessPending(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if ok, e := f.repo.DispatchOne(context.Background(), nil); !ok || !errors.Is(e, notifications.ErrDeliveryUnconfigured) {
		t.Fatal(ok, e)
	}
	var attempts int
	var code string
	var delivered *time.Time
	if e := f.db.QueryRow(`SELECT attempts,last_error,delivered_at FROM notification_v1.deliveries WHERE notification_id=$1`, id).Scan(&attempts, &code, &delivered); e != nil || attempts != 1 || code != "DELIVERY_UNCONFIGURED" || delivered != nil {
		t.Fatal(attempts, code, delivered, e)
	}
	if ok, e := f.repo.DispatchOne(context.Background(), nil); ok || e != nil {
		t.Fatal("backoff ignored", ok, e)
	}
	query(t, f.db, `UPDATE notification_v1.deliveries SET next_attempt_at=now()-interval '1 second',lease_id=$2,lease_until=now()-interval '1 second' WHERE notification_id=$1`, id, authorityUUID(t))
	s := &notificationSender{err: errors.New("secret-provider-response")}
	if ok, e := f.repo.DispatchOne(context.Background(), s); !ok || e == nil {
		t.Fatal(ok, e)
	}
	if e := f.db.QueryRow(`SELECT last_error FROM notification_v1.deliveries WHERE notification_id=$1`, id).Scan(&code); e != nil || code != "DELIVERY_FAILED" {
		t.Fatal(code, e)
	}
	query(t, f.db, `UPDATE notification_v1.deliveries SET next_attempt_at=now()-interval '1 second' WHERE notification_id=$1`, id)
	s.err = nil
	if ok, e := f.repo.DispatchOne(context.Background(), s); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if s.calls != 2 || s.keys[0] != id || s.keys[1] != id {
		t.Fatal("retry idempotency identity", s.keys)
	}
	if ok, e := f.repo.DispatchOne(context.Background(), s); ok || e != nil {
		t.Fatal("duplicate accepted delivery", ok, e)
	}
}

func TestPGNotificationsConcurrentIdempotency(t *testing.T) {
	f := newNotificationFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	ids := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := f.repo.Enqueue(context.Background(), notificationCommand(f, "concurrent"))
			if e != nil {
				errs <- e
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		t.Fatal(e)
	}
	prior := ""
	for id := range ids {
		if prior != "" && id != prior {
			t.Fatal("different durable command")
		}
		prior = id
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.repo.ProcessPending(context.Background(), 100)
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	p, e := f.repo.Replay(f.ctx, f.actor, f.app, 0, 100)
	if e != nil || p.HighWater != 1 || len(p.Events) != 1 {
		t.Fatal(p, e)
	}
	otherPool := database(t)
	r := notificationpg.New(otherPool, f.guard, f.app)
	if _, e = r.Unread(f.ctx, f.actor, f.app); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("different pool accepted", e)
	}
}

func TestPGNotificationsProjectionRollbackAndDisabledAccount(t *testing.T) {
	f := newNotificationFixture(t)
	id := notificationEnqueue(t, f, notificationCommand(f, "rollback"))
	constraint := "synthetic_notification_" + strings.ReplaceAll(f.actor, "-", "")
	// The interpolated values are generated fixture UUID/identifier bytes, never
	// input from an HTTP request. This disposable-only constraint forces the
	// final event append to fail after the inbox INSERT, proving transactionality.
	query(t, f.db, `ALTER TABLE notification_v1.events ADD CONSTRAINT `+constraint+` CHECK(account_id::text<>'`+f.actor+`')`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE notification_v1.events DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.ProcessPending(context.Background(), 100); e == nil {
		t.Fatal("injected projection failure did not fail")
	}
	var inbox int
	var processed *time.Time
	if e := f.db.QueryRow(`SELECT count(*) FROM notification_v1.inbox WHERE id=$1`, id).Scan(&inbox); e != nil || inbox != 0 {
		t.Fatal("partial inbox persisted", inbox, e)
	}
	if e := f.db.QueryRow(`SELECT processed_at FROM notification_v1.outbox WHERE id=$1`, id).Scan(&processed); e != nil || processed != nil {
		t.Fatal("failed source acknowledged", processed, e)
	}
	query(t, f.db, `ALTER TABLE notification_v1.events DROP CONSTRAINT `+constraint)
	if n, e := f.repo.ProcessPending(context.Background(), 100); e != nil || n != 1 {
		t.Fatal("retry did not recover", n, e)
	}
	notificationEnqueue(t, f, notificationCommand(f, "disabled"))
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, e := f.repo.ProcessPending(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Unread(f.ctx, f.actor, f.app); !errors.Is(e, authority.ErrDisabled) {
		t.Fatal("disabled account read", e)
	}
	if e := f.db.QueryRow(`SELECT count(*) FROM notification_v1.inbox WHERE account_id=$1`, f.actor).Scan(&inbox); e != nil || inbox != 1 {
		t.Fatal("disabled recipient received new item", inbox, e)
	}
}

func TestPGNotificationsWalletInboxProjected(t *testing.T) {
	f := newNotificationFixture(t)
	wallets := walletpg.New(f.db)
	payer := walletCreate(t, wallets, f.actor)
	payee := walletCreate(t, wallets, f.other)
	walletFund(t, f.db, payer, "synthetic", "3")
	transaction, e := wallets.Transfer(context.Background(), wallet.TransferCommand{ActorID: f.actor, PayerWalletID: payer.ID, PayeeWalletID: payee.ID, PayerOwner: payer.Owner(), Currency: "synthetic", Amount: walletAmount(t, "1"), Fingerprint: strings.Repeat("a", 64)})
	if e != nil {
		t.Fatal(e)
	}
	notificationDrain(t, func() (int, error) { return f.repo.ProjectWallet(context.Background(), 100) })
	for _, recipient := range []struct {
		account string
		ctx     context.Context
	}{{f.actor, f.ctx}, {f.other, f.otherCtx}} {
		list, total, e := f.repo.List(recipient.ctx, recipient.account, f.app, 100, 0)
		if e != nil || total != 1 || len(list) != 1 || list[0].Topic != "wallets.transactions" || !strings.Contains(string(list[0].Meta), transaction.ID) {
			t.Fatal(list, total, e)
		}
		p, e := f.repo.Replay(recipient.ctx, recipient.account, f.app, 0, 100)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, event := range p.Events {
			if event.Type == "wallet.transaction.created" {
				found = true
				if !strings.Contains(string(event.Data), transaction.ID) {
					t.Fatal("wrong transaction projection")
				}
			}
		}
		if !found {
			t.Fatal("actual wallet outbox did not reach realtime")
		}
	}
	// Projecting again cannot make another inbox item or emit another packet.
	if n, e := f.repo.ProjectWallet(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestPGNotificationsWalletAndChatProjection(t *testing.T) {
	f := newNotificationFixture(t)
	wallets := walletpg.New(f.db)
	w, e := wallets.CreateWallet(context.Background(), wallet.Owner{AccountID: f.actor}, "Synthetic projected wallet")
	if e != nil {
		t.Fatal(e)
	}
	notificationDrain(t, func() (int, error) { return f.repo.ProjectWallet(context.Background(), 100) })
	p, e := f.repo.Replay(f.ctx, f.actor, f.app, 0, 100)
	if e != nil || len(p.Events) != 1 || p.Events[0].Type != "wallet.created" || !strings.Contains(string(p.Events[0].Data), w.ID) {
		t.Fatal(p, e)
	}
	other, e := f.repo.Replay(f.otherCtx, f.other, f.app, 0, 100)
	if e != nil || len(other.Events) != 0 {
		t.Fatal("wallet account leak", other, e)
	}
	repo := chatRepo(t, f.db)
	name := "Synthetic projection room"
	room, e := repo.Create(context.Background(), f.actor, rooms.RoomPatch{Name: &name, IsPublic: boolean(true), IsCommunity: boolean(true)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ChangeMember(context.Background(), f.other, room.ID, rooms.Join, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Send(context.Background(), f.actor, room.ID, rooms.TextRequest{Content: "synthetic secret body"}); e != nil {
		t.Fatal(e)
	}
	notificationDrain(t, func() (int, error) {
		return f.repo.ProjectChat(context.Background(), 100, int(chatPolicy.NoneEncryptionMode))
	})
	other, e = f.repo.Replay(f.otherCtx, f.other, f.app, 0, 100)
	if e != nil || len(other.Events) != 1 || other.Events[0].Type != "owned.chat.changed" || strings.Contains(string(other.Events[0].Data), "secret body") {
		t.Fatal(other, e)
	}
	if _, e = repo.ChangeMember(context.Background(), f.other, room.ID, rooms.Leave, ""); e != nil {
		t.Fatal(e)
	}
	after, e := f.repo.Replay(f.otherCtx, f.other, f.app, 0, 100)
	if e != nil || len(after.Events) != 0 || after.Cursor != other.Cursor {
		t.Fatal("revoked room replay", after, e)
	}
	// Membership rejoin does not grant old realtime invalidations either.
	if _, e = repo.ChangeMember(context.Background(), f.other, room.ID, rooms.Join, ""); e != nil {
		t.Fatal(e)
	}
	after, e = f.repo.Replay(f.otherCtx, f.other, f.app, 0, 100)
	if e != nil || len(after.Events) != 0 {
		t.Fatal("prejoin event leak", after, e)
	}
}

func TestPGNotificationsWebSocketReplayAndRevocation(t *testing.T) {
	// This mandatory guard precedes every socket. Only the approved disposable
	// PostgreSQL/process CI may execute this synthetic transport integration.
	f := newNotificationFixture(t)
	id := notificationEnqueue(t, f, notificationCommand(f, "websocket"))
	if _, e := f.repo.ProcessPending(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	h, e := realtime.New(f.repo, notificationIdentity{f.v}, realtime.Config{App: f.app, PollInterval: 20 * time.Millisecond, WriteTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(f.middleware(h))
	defer server.Close()
	header := http.Header{"Authorization": []string{"Bearer " + f.token}}
	dial := func(after string) *websocket.Conn {
		c, response, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?namespace="+f.app+"&after="+after, header)
		if e != nil {
			if response != nil {
				t.Fatal(response.Status, e)
			}
			t.Fatal(e)
		}
		return c
	}
	c := dial("0")
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var packet struct {
		Type     string          `json:"type"`
		Data     json.RawMessage `json:"data"`
		Sequence int64           `json:"sequence"`
	}
	if e = c.ReadJSON(&packet); e != nil || packet.Type != "notifications.new" || packet.Sequence != 1 || !strings.Contains(string(packet.Data), id) {
		t.Fatal(packet, e)
	}
	_ = c.Close()
	c = dial("1")
	defer c.Close()
	if e = c.WriteJSON(map[string]any{"type": "ping", "data": nil}); e != nil {
		t.Fatal(e)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if e = c.ReadJSON(&packet); e != nil || packet.Type != "pong" {
		t.Fatal(packet, e)
	}
	if e = f.issuer.Revoke(context.Background(), f.token, "current"); e != nil {
		t.Fatal(e)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if e = c.ReadJSON(&packet); e == nil {
		t.Fatal("revoked session kept streaming", packet)
	}
	_, response, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?namespace="+f.app, header)
	if e == nil || response == nil || response.StatusCode != 401 {
		t.Fatal("revoked reconnect", response, e)
	}
}

func TestPGNotificationsRestartPersistence(t *testing.T) {
	db := database(t)
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	file := filepath.Join(root, "notification-restart.json")
	type saved struct{ Actor, App, ID, Token, Keys string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newNotificationFixture(t)
		c := notificationCommand(f, "restart")
		c.External = true
		id := notificationEnqueue(t, f, c)
		if _, e := f.repo.ProcessPending(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
		if _, e := f.repo.DispatchOne(context.Background(), nil); !errors.Is(e, notifications.ErrDeliveryUnconfigured) {
			t.Fatal(e)
		}
		// A fresh synthetic key set and session reconstructs the verifier after the
		// database restart, without retaining a Principal or an in-memory event hub.
		dir := authorityKeys(t)
		persistent := filepath.Join(root, "notification-restart-keys")
		if e := os.Mkdir(persistent, 0700); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"synthetic-signing.pem", "synthetic-hash.bin", "synthetic-seal.bin"} {
			b, e := os.ReadFile(filepath.Join(dir, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(persistent, name), b, 0600); e != nil {
				t.Fatal(e)
			}
		}
		now := &atomic.Int64{}
		now.Store(time.Now().UTC().Unix())
		issuer := newPGAuthority(t, db, persistent, now)
		token := pgIssue(t, issuer, f.actor).Token
		b, _ := json.Marshal(saved{f.actor, f.app, id, token, persistent})
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Skip("database restart phase required")
	}
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var s saved
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	v, e := authority.NewVerifier(authoritypg.New(db), authorityConfig(t, s.Keys, now))
	if e != nil {
		t.Fatal(e)
	}
	proof, e := v.Authenticate(context.Background(), s.Token)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	r := notificationpg.New(db, g, s.App)
	ctx := g.Bind(context.Background(), proof)
	p, e := r.Replay(ctx, s.Actor, s.App, 0, 100)
	if e != nil || len(p.Events) != 1 || !strings.Contains(string(p.Events[0].Data), s.ID) {
		t.Fatal("replay lost on restart", p, e)
	}
	list, total, e := r.List(ctx, s.Actor, s.App, 10, 0)
	if e != nil || total != 1 || len(list) != 1 || list[0].ID != s.ID {
		t.Fatal("inbox lost on restart", list, total, e)
	}
	query(t, db, `UPDATE notification_v1.deliveries SET next_attempt_at=now()-interval '1 second' WHERE notification_id=$1`, s.ID)
	sender := &notificationSender{}
	if ok, e := r.DispatchOne(ctx, sender); !ok || e != nil || sender.calls != 1 || sender.keys[0] != s.ID {
		t.Fatal("pending delivery lost on restart", ok, e)
	}
}
