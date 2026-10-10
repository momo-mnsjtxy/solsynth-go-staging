// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationhttp"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type federationKeys struct {
	locals map[string]*rsa.PrivateKey
	remote f.RemoteKey
}

func (k federationKeys) LocalKey(_ context.Context, id string) (crypto.Signer, error) {
	v := k.locals[id]
	if v == nil {
		return nil, f.ErrUnavailable
	}
	return v, nil
}
func (k federationKeys) RemoteKey(_ context.Context, id string) (f.RemoteKey, error) {
	if id != k.remote.KeyID {
		return f.RemoteKey{}, f.ErrNotFound
	}
	return k.remote, nil
}
func (k federationKeys) RemoteActor(_ context.Context, id string) (f.RemoteKey, error) {
	if id != k.remote.Actor {
		return f.RemoteKey{}, f.ErrNotFound
	}
	return k.remote, nil
}

type federationFixture struct {
	db                                      *sql.DB
	r                                       *federationpg.Repository
	post                                    *postpg.Repository
	handler                                 http.Handler
	config                                  f.Config
	keys                                    federationKeys
	remoteKey                               *rsa.PrivateKey
	clock                                   *atomic.Int64
	actor, publisher, name, actorURI, token string
	ctx                                     context.Context
	guard                                   *authoritypg.TransactionGuard
	verifier                                *authority.Verifier
	issuer                                  *authority.Service
	authConfig                              authority.Config
	preserve                                bool
}

func federationDB(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t)
	for _, s := range []string{authoritypg.Schema, accountpg.Schema, socialpg.Schema, postpg.Schema, federationpg.Schema} {
		query(t, db, s)
	}
	return db
}
func newFederationFixture(t *testing.T, manual bool) *federationFixture {
	return newFederationFixtureWithAuthority(t, manual, nil)
}
func newFederationFixtureWithAuthority(t *testing.T, manual bool, configure func(*authority.Config)) *federationFixture {
	t.Helper()
	db := federationDB(t)
	x := &federationFixture{db: db, actor: authorityUUID(t), clock: &atomic.Int64{}}
	x.clock.Store(time.Now().UTC().Truncate(time.Second).Unix())
	ctx := context.Background()
	x.name = "synthetic_federation_" + strings.ReplaceAll(x.actor, "-", "")
	if e := authoritypg.SeedSynthetic(ctx, db, x.actor, x.name, time.Unix(x.clock.Load(), 0)); e != nil {
		t.Fatal(e)
	}
	store := authoritypg.New(db)
	if e := store.ReplacePolicy(ctx, x.actor, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "publishers.*"}, {Pattern: f.ReadPermission}, {Pattern: f.KeyAdminPermission}}, nil); e != nil {
		t.Fatal(e)
	}
	ac := authorityConfig(t, authorityKeys(t), x.clock)
	if configure != nil {
		configure(&ac)
	}
	x.authConfig = ac
	var e error
	x.issuer, e = authority.New(store, ac)
	if e != nil {
		t.Fatal(e)
	}
	x.token = pgIssue(t, x.issuer, x.actor).Token
	x.verifier, e = authority.NewVerifier(store, ac)
	if e != nil {
		t.Fatal(e)
	}
	principal, e := x.verifier.Authenticate(ctx, x.token)
	if e != nil {
		t.Fatal(e)
	}
	x.guard = authoritypg.NewTransactionGuard(store, x.verifier)
	x.ctx = x.guard.Bind(ctx, principal)
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,5,'2020-01-01')`, x.actor)
	p, e := accountpg.NewForCompatibilityTests(db).CreatePublisher(ctx, x.actor, accounts.PublisherPatch{Name: &x.name, Nick: &x.name})
	if e != nil {
		t.Fatal(e)
	}
	x.publisher = p.ID
	local, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	remote, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	x.remoteKey = remote
	x.keys = federationKeys{map[string]*rsa.PrivateKey{x.publisher: local}, f.RemoteKey{Actor: "https://remote.example.test/actors/" + x.name, Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actors/" + x.name + "#main-key", PublicKey: &remote.PublicKey}}
	x.config = f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, Now: func() time.Time { return time.Unix(x.clock.Load(), 0) }, MaxAttempts: 3, DeliveryTimeout: 5 * time.Second}
	x.actorURI = x.config.Origin + "/activitypub/actors/" + x.name
	x.r, e = federationpg.New(db, x.config, x.keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	x.post = postpg.NewWithAuthority(db, x.guard)
	x.handler = federationhttp.Handler{Repository: x.r, Config: x.config, Keys: x.keys, Authority: x.verifier}
	if e = x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: true, Manual: manual}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if x.preserve {
			return
		}
		query(t, db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, x.publisher)
		query(t, db, `UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND status IN('pending','inflight')`, x.publisher)
	})
	return x
}
func (x *federationFixture) activity(t *testing.T, kind string, object any) f.Activity {
	t.Helper()
	b, e := json.Marshal(object)
	if e != nil {
		t.Fatal(e)
	}
	return f.Activity{Context: f.Context, ID: x.keys.remote.Actor + "/events/" + authorityUUID(t), Type: kind, Actor: x.keys.remote.Actor, Object: b, To: []string{x.actorURI}}
}
func (x *federationFixture) verified(t *testing.T, a f.Activity) f.Verified {
	t.Helper()
	b, _ := json.Marshal(a)
	r := httptest.NewRequest("POST", x.actorURI+"/inbox", bytes.NewReader(b))
	if e := f.Sign(r, b, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); e != nil {
		t.Fatal(e)
	}
	v, e := f.Verify(x.ctx, x.config, x.keys, r, b)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (x *federationFixture) receive(t *testing.T, a f.Activity) error {
	t.Helper()
	return x.r.Receive(x.ctx, x.name, x.verified(t, a))
}
func (x *federationFixture) call(t *testing.T, method, path, body string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, x.config.Origin+path, strings.NewReader(body))
	req = req.WithContext(x.ctx)
	if authorized {
		req.Header.Set("Authorization", "Bearer "+x.token)
	}
	w := httptest.NewRecorder()
	x.handler.ServeHTTP(w, req)
	return w
}
func (x *federationFixture) makePost(t *testing.T, body string, visibility int, draft bool) posts.Post {
	t.Helper()
	p := posts.Patch{Content: &body, Visibility: &visibility}
	if draft {
		now := x.config.Clock()
		p.DraftedAt = &now
	}
	v, e := x.post.Create(x.ctx, x.actor, x.publisher, p)
	if e != nil {
		t.Fatal(e)
	}
	// The repository clock is PostgreSQL time; the federation/authority clock is
	// explicitly frozen. Align synthetic post fixture timestamps instead of
	// assuming key generation and SQL calls finish within a one-second offset.
	at := x.config.Clock().UTC().Truncate(time.Microsecond)
	query(t, x.db, `UPDATE owned_text_posts SET created_at=$2::timestamptz,updated_at=$2::timestamptz,published_at=CASE WHEN drafted_at IS NULL THEN $2::timestamptz ELSE NULL END,drafted_at=CASE WHEN drafted_at IS NOT NULL THEN $2::timestamptz ELSE NULL END WHERE id=$1`, v.ID, at)
	query(t, x.db, `UPDATE owned_text_post_audit SET occurred_at=$2 WHERE post_id=$1`, v.ID, at)
	v.CreatedAt = at
	v.UpdatedAt = at
	if draft {
		v.DraftedAt = &at
	} else {
		v.PublishedAt = &at
	}
	return v
}
func (x *federationFixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if e := x.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE publisher_id=$1`, x.publisher).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPGFederationCanonicalPublicHTTP(t *testing.T) {
	x := newFederationFixture(t, true)
	public := x.makePost(t, "<script>literal text</script>", 0, false)
	private := x.makePost(t, "private", posts.Private, false)
	draft := x.makePost(t, "draft", 0, true)
	a, e := x.r.Actor(x.ctx, strings.ToUpper(x.name))
	if e != nil || a.ID != x.actorURI || a.PublicKey.Owner != x.actorURI || !a.Manual {
		t.Fatal(a, e)
	}
	for _, id := range []string{private.ID, draft.ID, authorityUUID(t)} {
		if _, e := x.r.Object(x.ctx, id); !errors.Is(e, f.ErrNotFound) {
			t.Fatal("private object leaked", id, e)
		}
	}
	n, e := x.r.Object(x.ctx, public.ID)
	if e != nil || n.AttributedTo != x.actorURI || strings.Contains(n.Content, "<script>") {
		t.Fatal(n, e)
	}
	for _, path := range []string{"/activitypub/actors/" + x.name, "/.well-known/webfinger?resource=acct:" + x.name + "@local.example.test", "/activitypub/objects/" + public.ID, "/activitypub/actors/" + x.name + "/outbox", "/activitypub/actors/" + x.name + "/outbox?offset=0&take=20"} {
		w := x.call(t, "GET", path, "", false)
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "draft") {
			t.Fatal("private projection", w.Body.String())
		}
	}
	// Actor URL segments resolve names only; publisher UUIDs are not aliases.
	if _, e := x.r.Actor(x.ctx, x.publisher); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("publisher ID accepted as actor name", e)
	}
	c, e := x.r.Outbox(x.ctx, x.name, 0, 20)
	if e != nil || c.Total != 1 || len(c.Items) != 1 {
		t.Fatal(c, e)
	}
	path := "/api/activitypub/publishers/" + x.publisher + "/settings"
	if w := x.call(t, "PATCH", path, `{"enabled":false,"manually_approves_followers":true}`, false); w.Code != 401 {
		t.Fatal("anonymous settings", w.Code)
	}
	if w := x.call(t, "PATCH", path, `{"enabled":false,"manually_approves_followers":true}`, true); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e = x.r.Actor(x.ctx, x.name); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("disabled actor", e)
	}
	if _, e = x.r.Object(x.ctx, public.ID); !errors.Is(e, f.ErrNotFound) {
		t.Fatal("disabled publisher object", e)
	}
}
func TestPGFederationInboxReplayAndTargetIsolation(t *testing.T) {
	x := newFederationFixture(t, false)
	follow := x.activity(t, "Follow", x.actorURI)
	v := x.verified(t, follow)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- x.r.Receive(context.Background(), x.name, v) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if x.count(t, "owned_federation_inbox") != 1 || x.count(t, "owned_federation_relationships") != 1 || x.count(t, "owned_federation_deliveries") != 1 {
		t.Fatal("duplicate inbox effects")
	}
	replay := follow
	replay.To = []string{"https://local.example.test/other"}
	if e := x.receive(t, replay); !errors.Is(e, f.ErrReplay) {
		t.Fatal("ID-content collision", e)
	}
	wrong := x.activity(t, "Follow", "https://local.example.test/activitypub/actors/another")
	if e := x.receive(t, wrong); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("wrong target accepted", e)
	}
	if e := x.r.Receive(x.ctx, x.name, f.Verified{}); !errors.Is(e, f.ErrSignature) {
		t.Fatal("zero verification envelope", e)
	}
	// Reuse this fixture's existing signer. Alias routing must preserve the
	// signed request target and the stable activity identity independently.
	newName := "renamed-" + x.name
	tx, e := x.db.BeginTx(x.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = accountpg.RenamePublisherIdentity(x.ctx, tx, x.publisher, x.name, newName, x.config.Clock()); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = x.r.Receive(x.ctx, x.name, v); e != nil {
		t.Fatal("cached inbox route replay", e)
	}
	if e = x.r.Receive(x.ctx, newName, v); !errors.Is(e, f.ErrSignature) {
		t.Fatal("proof used for a different request target", e)
	}
	raw, _ := json.Marshal(follow)
	request := httptest.NewRequest("POST", x.config.Origin+"/activitypub/actors/"+newName+"/inbox", bytes.NewReader(raw))
	if e = f.Sign(request, raw, x.keys.remote.KeyID, x.remoteKey, x.config.Clock()); e != nil {
		t.Fatal(e)
	}
	aliasProof, e := f.Verify(x.ctx, x.config, x.keys, request, raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = x.r.Receive(x.ctx, newName, aliasProof); e != nil {
		t.Fatal("current alias inbox replay", e)
	}
	if x.count(t, "owned_federation_inbox") != 1 || x.count(t, "owned_federation_relationships") != 1 || x.count(t, "owned_federation_deliveries") != 1 {
		t.Fatal("alias replay duplicated effects")
	}
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "block", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("blocked actor accepted", e)
	}
	c, e := x.r.Collection(x.ctx, x.name, "followers", 0, 20)
	if e != nil || c.Total != 0 {
		t.Fatal("blocked follower counted", c, e)
	}
	var status string
	if e = x.db.QueryRow(`SELECT status FROM owned_federation_deliveries WHERE publisher_id=$1`, x.publisher).Scan(&status); e != nil || status != "cancelled" {
		t.Fatal("blocked delivery retained", status, e)
	}
}
func TestPGFederationRemoteObjectsAndFollowControl(t *testing.T) {
	x := newFederationFixture(t, true)
	incoming := x.activity(t, "Follow", x.actorURI)
	if e := x.receive(t, incoming); e != nil {
		t.Fatal(e)
	}
	if x.count(t, "owned_federation_deliveries") != 0 {
		t.Fatal("manual follow autoaccepted")
	}
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "accept", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e := x.db.QueryRow(`SELECT payload FROM owned_federation_deliveries WHERE publisher_id=$1 AND activity_type='Follow'`, x.publisher).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var outbound f.Activity
	if e := json.Unmarshal(raw, &outbound); e != nil {
		t.Fatal(e)
	}
	if e := x.receive(t, x.activity(t, "Accept", outbound)); e != nil {
		t.Fatal(e)
	}
	note := f.Note{ID: x.keys.remote.Actor + "/notes/1", Type: "Note", AttributedTo: x.keys.remote.Actor, Content: "public", To: []string{f.Public}}
	create := x.activity(t, "Create", note)
	create.To = []string{f.Public}
	if e := x.receive(t, create); e != nil {
		t.Fatal(e)
	}
	note.Content = "edited"
	update := x.activity(t, "Update", note)
	update.To = []string{f.Public}
	if e := x.receive(t, update); e != nil {
		t.Fatal(e)
	}
	note.ID = x.keys.remote.Actor + "/notes/private"
	note.To = []string{x.actorURI}
	private := x.activity(t, "Create", note)
	private.To = []string{f.Public}
	if e := x.receive(t, private); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("private content admitted", e)
	}
	if x.count(t, "owned_federation_objects") != 1 {
		t.Fatal("private content persisted")
	}
	deleted := x.activity(t, "Delete", createObjectID(t, create))
	if e := x.receive(t, deleted); e != nil {
		t.Fatal(e)
	}
	create.ID = x.keys.remote.Actor + "/events/resurrection"
	if e := x.receive(t, create); !errors.Is(e, f.ErrForbidden) {
		t.Fatal("deleted remote object resurrected", e)
	}
	undo := x.activity(t, "Undo", incoming)
	if e := x.receive(t, undo); e != nil {
		t.Fatal(e)
	}
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "unfollow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	if x.count(t, "owned_federation_objects") != 0 {
		t.Fatal("unfollow cache retained")
	}
}
func createObjectID(t *testing.T, a f.Activity) string {
	t.Helper()
	id, e := f.ObjectID(a.Object)
	if e != nil {
		t.Fatal(e)
	}
	return id
}

type federationCapture struct {
	mu       sync.Mutex
	codes    []int
	requests [][]byte
}

func (c *federationCapture) Send(ctx context.Context, r *http.Request) (int, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return 0, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, b)
	if len(c.codes) == 0 {
		return 202, nil
	}
	code := c.codes[0]
	c.codes = c.codes[1:]
	return code, nil
}
func TestPGFederationDurableDeliveryAndPrivacy(t *testing.T) {
	x := newFederationFixture(t, false)
	if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	transport := &federationCapture{codes: []int{503, 202}}
	if _, e := x.r.ProcessDeliveries(x.ctx, nil, 10); !errors.Is(e, f.ErrUnavailable) {
		t.Fatal("nil transport", e)
	}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 10); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 10); e != nil || n != 0 {
		t.Fatal("early retry", n, e)
	}
	x.clock.Add(61)
	reopenedDB := database(t)
	reopened, e := federationpg.New(reopenedDB, x.config, x.keys, authoritypg.NewTransactionGuard(authoritypg.New(reopenedDB), x.verifier))
	if e != nil {
		t.Fatal(e)
	}
	if n, e := reopened.ProcessDeliveries(x.ctx, transport, 10); e != nil || n != 1 {
		t.Fatal("durable retry", n, e)
	}
	if len(transport.requests) != 2 || !bytes.Equal(transport.requests[0], transport.requests[1]) {
		t.Fatal("retry changed activity")
	}
	post := x.makePost(t, "public before queue", 0, false)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	x.clock.Add(1)
	private := posts.Private
	if _, e := x.post.Update(x.ctx, x.actor, post.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if len(transport.requests) != 2 {
		t.Fatal("private transition leaked queued content")
	}
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal("privacy tombstone", n, e)
	}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if len(transport.requests) != 2 {
		t.Fatal("never-dispatched post created cleanup authority")
	}
	// Explicitly exhausted lease is recovered to terminal dead, never retried.
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	query(t, x.db, `UPDATE owned_federation_deliveries SET status='inflight',attempts=3,lease_token=$2,lease_until='2020-01-01' WHERE publisher_id=$1 AND activity_type='Follow'`, x.publisher, authorityUUID(t))
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 0 {
		t.Fatal("exhausted retry", n, e)
	}
	var status string
	if e = x.db.QueryRow(`SELECT status FROM owned_federation_deliveries WHERE publisher_id=$1 AND activity_type='Follow'`, x.publisher).Scan(&status); e != nil || status != "dead" {
		t.Fatal(status, e)
	}
	other := x.makePost(t, "same timestamp first", posts.Public, false)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	query(t, x.db, `UPDATE owned_text_posts SET content='same timestamp newer' WHERE id=$1`, other.ID)
	query(t, x.db, `INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at) VALUES($1,$2,'update',now())`, x.actor, other.ID)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal("audit revision missed same-timestamp edit", n, e)
	}
	before := len(transport.requests)
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if len(transport.requests) != before+1 || !bytes.Contains(transport.requests[before], []byte("same timestamp newer")) {
		t.Fatal("stale same-timestamp content sent")
	}
	// Concurrent workers recover one expired nonfinal lease exactly once.
	if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	query(t, x.db, `UPDATE owned_federation_deliveries SET status='inflight',attempts=1,lease_token=$2,lease_until='2020-01-01' WHERE publisher_id=$1 AND activity_type='Accept' AND status='pending'`, x.publisher, authorityUUID(t))
	before = len(transport.requests)
	var workers sync.WaitGroup
	var attempted atomic.Int64
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			n, e := x.r.ProcessDeliveries(context.Background(), transport, 1)
			attempted.Add(int64(n))
			errs <- e
		}()
	}
	workers.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if attempted.Load() != 1 || len(transport.requests) != before+1 {
		t.Fatal("duplicate lease claim", attempted.Load(), len(transport.requests)-before)
	}
	var recovered int
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_deliveries WHERE publisher_id=$1 AND activity_type='Accept' AND status='sent' AND attempts=2`, x.publisher).Scan(&recovered); e != nil || recovered != 2 {
		t.Fatal("expired lease did not retain retry count", recovered, e)
	}
}
func TestPGFederationFreshAuthorityAndAtomicRollback(t *testing.T) {
	x := newFederationFixture(t, false)
	if e := x.r.Change(context.Background(), x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); e == nil {
		t.Fatal("unbound caller actor authorized")
	}
	other := authorityUUID(t)
	if e := x.r.Settings(x.ctx, other, x.publisher, f.Settings{}); e == nil {
		t.Fatal("different actor bound to proof")
	}
	// Fault after follow-state and delivery writes must roll back every effect.
	constraint := "federation_fail_" + strings.ReplaceAll(x.publisher, "-", "")
	query(t, x.db, `ALTER TABLE owned_federation_inbox ADD CONSTRAINT `+constraint+` CHECK(publisher_id<>'`+x.publisher+`'::uuid) NOT VALID`)
	follow := x.activity(t, "Follow", x.actorURI)
	if e := x.receive(t, follow); e == nil {
		t.Fatal("injected SQL failure accepted")
	}
	query(t, x.db, `ALTER TABLE owned_federation_inbox DROP CONSTRAINT `+constraint)
	if x.count(t, "owned_federation_relationships") != 0 || x.count(t, "owned_federation_inbox") != 0 || x.count(t, "owned_federation_deliveries") != 0 {
		t.Fatal("partial inbox commit")
	}
	if e := x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: true, Manual: true}); e != nil {
		t.Fatal(e)
	}
	if e := x.receive(t, follow); e != nil {
		t.Fatal(e)
	}
	if e := x.issuer.Revoke(x.ctx, x.token, "current"); e != nil {
		t.Fatal(e)
	}
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "accept", RemoteActor: x.keys.remote.Actor}); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked authority mutated federation", e)
	}
	if x.count(t, "owned_federation_deliveries") != 0 {
		t.Fatal("revoked accept enqueued")
	}
}

// loopbackFederationTransport is test-only, constructed after database(t) has
// verified the marked disposable CI cluster. It cannot be enabled in runtime.
type loopbackFederationTransport struct {
	base   *url.URL
	client *http.Client
}

func (l loopbackFederationTransport) Send(ctx context.Context, r *http.Request) (int, error) {
	copy := r.Clone(ctx)
	u := *r.URL
	u.Scheme = l.base.Scheme
	u.Host = l.base.Host
	copy.URL = &u
	response, e := l.client.Do(copy)
	if e != nil {
		return 0, e
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}
func TestPGFederationSignedLoopbackDelivery(t *testing.T) {
	x := newFederationFixture(t, false) // Guard is checked BEFORE opening any socket.
	if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	peerKeys := federationKeys{remote: f.RemoteKey{Actor: x.actorURI, Inbox: x.actorURI + "/inbox", KeyID: x.actorURI + "#main-key", PublicKey: &x.keys.locals[x.publisher].PublicKey}}
	peerConfig := x.config
	peerConfig.Origin = "https://remote.example.test"
	peerConfig.AllowedOrigins = []string{x.config.Origin}
	var valid atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(io.LimitReader(r.Body, f.MaxBody+1))
		if e == nil {
			_, e = f.Verify(r.Context(), peerConfig, peerKeys, r, b)
		}
		if e != nil {
			w.WriteHeader(401)
			return
		}
		valid.Add(1)
		w.WriteHeader(202)
	}))
	defer server.Close()
	base, e := url.Parse(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Timeout = 5 * time.Second
	if n, e := x.r.ProcessDeliveries(x.ctx, loopbackFederationTransport{base, client}, 5); e != nil || n != 1 || valid.Load() != 1 {
		t.Fatal("real signed delivery", n, e, valid.Load())
	}
}
func TestPGFederationRestartPersistence(t *testing.T) {
	db := federationDB(t)
	const actor = "fed10000-0000-4000-8000-000000000001"
	const pub = "fed10000-0000-4000-8000-000000000002"
	if os.Getenv("PG_TEST_PHASE") == "before" {
		query(t, db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at) VALUES($1,'synthetic_federation_restart','Synthetic restart',now())`, actor)
		query(t, db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at) VALUES($1,'federation_restart','Synthetic restart',0,$2,now(),now())`, pub, actor)
		query(t, db, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at) VALUES($1,false,true,now())`, pub)
		query(t, db, `INSERT INTO owned_federation_peers(actor_uri,inbox_uri,updated_at) VALUES('https://remote.example.test/restart','https://remote.example.test/inbox',now())`)
		query(t, db, `INSERT INTO owned_federation_relationships(publisher_id,actor_uri,direction,status,follow_id,updated_at) VALUES($1,'https://remote.example.test/restart','in','accepted','https://remote.example.test/follow/restart',now())`, pub)
		query(t, db, `INSERT INTO owned_federation_inbox(publisher_id,activity_id,actor_uri,digest,received_at) VALUES($1,'https://remote.example.test/follow/restart','https://remote.example.test/restart',$2,now())`, pub, make([]byte, 32))
		query(t, db, `INSERT INTO owned_federation_deliveries(publisher_id,actor_uri,inbox_uri,activity_id,activity_type,payload,status,attempts,next_attempt,created_at) VALUES($1,'https://remote.example.test/restart','https://remote.example.test/inbox','https://local.example.test/restart/accept','Accept',$2,'dead',3,now(),now())`, pub, []byte(`{"type":"Accept"}`))
	}
	var status string
	var attempts, inbox int
	e := db.QueryRow(`SELECT d.status,d.attempts,(SELECT count(*) FROM owned_federation_inbox WHERE publisher_id=$1) FROM owned_federation_deliveries d WHERE d.publisher_id=$1`, pub).Scan(&status, &attempts, &inbox)
	if e != nil || status != "dead" || attempts != 3 || inbox != 1 {
		t.Fatal("restart lost durable federation state", status, attempts, inbox, e)
	}
}
