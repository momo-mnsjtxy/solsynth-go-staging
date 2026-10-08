// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager"
	"solsynth.local/go-migration/ports/passport/board"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

const hostAccountA = "a2000000-0000-4000-8000-000000000001"
const hostAccountB = "a2000000-0000-4000-8000-000000000002"
const hostRoom = "a2000000-0000-4000-8000-000000000003"

type authMemory struct {
	mu       sync.Mutex
	accounts map[string]*authState
}
type authState struct {
	account  authority.Account
	sessions map[string]authority.Session
	rules    []authority.Rule
}

func (m *authMemory) WithinAccount(ctx context.Context, id string, fn func(authority.Transaction) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.accounts[id]
	if s == nil {
		return authority.ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(s)
}
func (m *authMemory) ReadAccount(ctx context.Context, id string, fn func(authority.Transaction) error) error {
	return m.WithinAccount(ctx, id, fn)
}
func (m *authMemory) FindRefresh(context.Context, []byte) (string, string, error) {
	return "", "", authority.ErrNotFound
}
func (m *authMemory) FindCode(context.Context, []byte) (string, error) {
	return "", authority.ErrNotFound
}
func (s *authState) Account() authority.Account            { return s.account }
func (s *authState) SaveAccount(a authority.Account) error { s.account = a; return nil }
func (s *authState) Session(id string) (authority.Session, error) {
	v, ok := s.sessions[id]
	if !ok {
		return v, authority.ErrNotFound
	}
	return v, nil
}
func (s *authState) Sessions() ([]authority.Session, error) {
	out := []authority.Session{}
	for _, v := range s.sessions {
		out = append(out, v)
	}
	return out, nil
}
func (s *authState) SaveSession(v authority.Session) error        { s.sessions[v.ID] = v; return nil }
func (s *authState) Rules() ([]authority.Rule, error)             { return s.rules, nil }
func (s *authState) Punishments() ([]authority.Punishment, error) { return nil, nil }
func (s *authState) Code([]byte) (authority.LoginCode, error) {
	return authority.LoginCode{}, authority.ErrNotFound
}
func (s *authState) SaveCode(authority.LoginCode) error { return errors.New("unused test operation") }

var hostKeyOnce sync.Once
var hostKey *rsa.PrivateKey

func hostIdentity(t *testing.T) (*authority.Service, *authority.Verifier, *authMemory) {
	t.Helper()
	hostKeyOnce.Do(func() {
		var err error
		hostKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
	})
	store := &authMemory{accounts: map[string]*authState{}}
	for _, id := range []string{hostAccountA, hostAccountB} {
		store.accounts[id] = &authState{account: authority.Account{ID: id, Name: id, Version: 1, CreatedAt: time.Now().UTC()}, sessions: map[string]authority.Session{}, rules: []authority.Rule{{Pattern: "*"}}}
	}
	config := authority.Config{Issuer: "https://host-test.invalid", Audience: "owned-host", SigningKey: hostKey, SigningKeyID: "ephemeral", TrustedKeys: map[string]*rsa.PublicKey{"ephemeral": &hostKey.PublicKey}, RefreshHashKey: bytes.Repeat([]byte{1}, 32), ReplaySealKey: bytes.Repeat([]byte{2}, 32)}
	service, err := authority.New(store, config)
	if err != nil {
		t.Fatal(err)
	}
	v, err := authority.NewVerifier(store, config)
	if err != nil {
		t.Fatal(err)
	}
	return service, v, store
}
func hostToken(t *testing.T, s *authority.Service, id string, scopes ...string) string {
	t.Helper()
	p, err := s.IssueSession(context.Background(), id, authority.SessionOptions{Label: "recorder", Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
	return p.Token
}
func hostRequest(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPrivateProofFreshnessAndCredentialRemoval(t *testing.T) {
	s, v, store := hostIdentity(t)
	token := hostToken(t, s, hostAccountA, "accounts.view")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("raw credentials reached domain")
		}
		p, err := (identity{v}).require(r.Context(), "accounts.view")
		if err != nil {
			writeAuthorityError(w, err)
			return
		}
		if p.Account.ID != hostAccountA {
			t.Error("wrong account")
		}
		w.WriteHeader(204)
	})
	h := bindIdentity(v, inner)
	if w := hostRequest(h, "GET", "/", token, ""); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := hostRequest(h, "GET", "/", "unsigned", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := hostRequest(h, "GET", "/?access_token=x", token, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Synthetic-Principal", "owner")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	narrow := hostToken(t, s, hostAccountA, "unrelated.permission")
	if w = hostRequest(h, "GET", "/", narrow, ""); w.Code != 403 {
		t.Fatal("scope not enforced", w.Code)
	}
	// A change after token verification but before domain authorization must deny.
	revoked := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		store.accounts[hostAccountA].rules = []authority.Rule{{Pattern: "accounts.view", Deny: true}}
		store.mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	if w = hostRequest(revoked, "GET", "/", token, ""); w.Code != 403 {
		t.Fatal("stale grants used", w.Code)
	}
	store.mu.Lock()
	store.accounts[hostAccountA].account.Version++
	store.mu.Unlock()
	if w = hostRequest(h, "GET", "/", token, ""); w.Code != 401 {
		t.Fatal("stale version accepted", w.Code)
	}
}

type hostBoardRepo struct {
	mu    sync.Mutex
	reads []string
}

func (m *hostBoardRepo) List(ctx context.Context, id string) ([]board.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads = append(m.reads, id)
	return []board.Item{}, nil
}
func (*hostBoardRepo) Replace(context.Context, string, []board.Item) error {
	return errors.New("unused")
}
func (*hostBoardRepo) FindByID(context.Context, string, string) (board.Item, error) {
	return board.Item{}, errors.New("unused")
}
func (*hostBoardRepo) FindFirstWidget(context.Context, string, string, string) (board.Item, error) {
	return board.Item{}, errors.New("unused")
}
func (*hostBoardRepo) Save(context.Context, board.Item) error { return errors.New("unused") }
func TestBoardUsesVerifiedAccountAndFreshPermission(t *testing.T) {
	s, v, store := hostIdentity(t)
	repo := &hostBoardRepo{}
	h := bindIdentity(v, board.NewHTTPHandler(board.HTTPOptions{Service: board.Service{Repository: repo}, Authority: boardIdentity{identity{v}}}))
	a := hostToken(t, s, hostAccountA, board.SelfManage)
	b := hostToken(t, s, hostAccountB, board.SelfManage)
	if w := hostRequest(h, "GET", "/api/accounts/me/board", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, token := range []string{a, b} {
		if w := hostRequest(h, "GET", "/api/accounts/me/board", token, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	store.mu.Lock()
	store.accounts[hostAccountA].rules = nil
	store.mu.Unlock()
	if w := hostRequest(h, "GET", "/api/accounts/me/board", a, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if len(repo.reads) != 2 || repo.reads[0] != hostAccountA || repo.reads[1] != hostAccountB {
		t.Fatal(repo.reads)
	}
	if w := hostRequest(h, "HEAD", "/api/accounts/me/board", b, ""); w.Code != 405 || len(repo.reads) != 2 {
		t.Fatal(w.Code, repo.reads)
	}
}

type hostMessages struct {
	mu       sync.Mutex
	accounts []string
}

func (m *hostMessages) record(account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts = append(m.accounts, account)
}
func (m *hostMessages) Sync(_ context.Context, q messager.SyncQuery) (messager.SyncResponse, error) {
	m.record(q.AccountID)
	return messager.SyncResponse{Messages: []messager.MessageProjection{}, CurrentTimestamp: time.Now().UTC()}, nil
}
func (m *hostMessages) ListMessages(_ context.Context, q messager.MessageListQuery) (messager.MessagePage, error) {
	m.record(q.AccountID)
	return messager.MessagePage{Messages: []messager.MessageProjection{}}, nil
}
func (m *hostMessages) GetMessage(_ context.Context, q messager.MessageQuery) (messager.MessageProjection, error) {
	m.record(q.AccountID)
	return messager.MessageProjection{}, nil
}
func TestMessagerBindsProofAndRetainsPublicRead(t *testing.T) {
	s, v, _ := hostIdentity(t)
	repo := &hostMessages{}
	h := bindIdentity(v, messager.NewAuthoritySyncHandler(repo, syncIdentity{identity{v}}))
	token := hostToken(t, s, hostAccountA, "unrelated.permission")
	p := "/api/chat/" + hostRoom
	if w := hostRequest(h, "POST", p+"/sync", "", `{"last_sync_timestamp":0}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := hostRequest(h, "POST", p+"/sync", token, `{"last_sync_timestamp":0}`)
	if w.Code != 200 || w.Header().Get("X-Solsynth-Projection") != "owned-authority-room-sync-projection-v1" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = hostRequest(h, "GET", p+"/messages", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(repo.accounts) != 2 || repo.accounts[0] != hostAccountA || repo.accounts[1] != "" {
		t.Fatal(repo.accounts)
	}
	// Invalid supplied identity never falls back to an anonymous read.
	if w = hostRequest(h, "GET", p+"/messages", "malformed", ""); w.Code != 401 || len(repo.accounts) != 2 {
		t.Fatal(w.Code)
	}
}
func TestTaxonomyGrantsAreExplicitAndNoFixtureIdentity(t *testing.T) {
	s, v, store := hostIdentity(t)
	token := hostToken(t, s, hostAccountA, "posts.tags.create")
	a := taxonomyIdentity{identity: identity{v}}
	h := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := a.Require(r.Context(), taxonomy.TagsCreate)
		if err != nil {
			if errors.Is(err, taxonomy.ErrForbidden) {
				w.WriteHeader(403)
			} else {
				w.WriteHeader(401)
			}
			return
		}
		if actor != hostAccountA {
			t.Error(actor)
		}
		w.WriteHeader(204)
	}))
	if w := hostRequest(h, "GET", "/", token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	store.mu.Lock()
	store.accounts[hostAccountA].rules = []authority.Rule{{Pattern: "posts.tags.create", Deny: true}}
	store.mu.Unlock()
	if w := hostRequest(h, "GET", "/", token, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if _, err := a.Require(context.Background(), taxonomy.Grant("UnknownConstant")); !errors.Is(err, taxonomy.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := a.Publisher(context.Background(), hostAccountA); !errors.Is(err, taxonomy.ErrUnauthenticated) {
		t.Fatal("account is not publisher identity", err)
	}
}

func TestTaxonomyUsesDocumentedCategoryGrants(t *testing.T) {
	s, v, _ := hostIdentity(t)
	a := taxonomyIdentity{identity: identity{v}}
	for _, tc := range []struct {
		grant taxonomy.Grant
		key   string
	}{{taxonomy.CategoriesManage, "post.categories.manage"}, {taxonomy.CategoriesSubscribe, "post.categories.subscribe"}} {
		h := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := a.Require(r.Context(), tc.grant); err != nil {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(204)
		}))
		valid := hostToken(t, s, hostAccountA, tc.key)
		incorrect := hostToken(t, s, hostAccountA, strings.Replace(tc.key, "post.", "posts.", 1))
		if w := hostRequest(h, "POST", "/", valid, ""); w.Code != 204 {
			t.Fatal(tc.key, w.Code)
		}
		if w := hostRequest(h, "POST", "/", incorrect, ""); w.Code != 403 {
			t.Fatal("undocumented grant accepted", w.Code)
		}
	}
}

func TestPostIdentityPublicReadAndFreshProof(t *testing.T) {
	s, v, store := hostIdentity(t)
	a := postIdentity{identity{v}}
	token := hostToken(t, s, hostAccountA, "posts.create")
	h := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err == nil && r.Method == "POST" {
			err = a.Authorize(r, "posts.create")
		}
		if err != nil {
			writeAuthorityError(w, err)
			return
		}
		w.Header().Set("Verified-Account", id)
		w.WriteHeader(204)
	}))
	if w := hostRequest(h, "GET", "/api/posts", "", ""); w.Code != 204 || w.Header().Get("Verified-Account") != "" {
		t.Fatal(w.Code, w.Header())
	}
	if w := hostRequest(h, "POST", "/api/posts", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := hostRequest(h, "POST", "/api/posts", token, ""); w.Code != 204 || w.Header().Get("Verified-Account") != hostAccountA {
		t.Fatal(w.Code, w.Header())
	}
	store.mu.Lock()
	store.accounts[hostAccountA].rules = nil
	store.mu.Unlock()
	if w := hostRequest(h, "POST", "/api/posts", token, ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := hostRequest(h, "GET", "/api/posts", token, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := hostRequest(h, "GET", "/api/posts", "invalid", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestParallelContextsDoNotShareIdentity(t *testing.T) {
	s, v, _ := hostIdentity(t)
	a := hostToken(t, s, hostAccountA, "*")
	b := hostToken(t, s, hostAccountB, "*")
	h := bindIdentity(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := (identity{v}).require(r.Context())
		if err != nil {
			writeAuthorityError(w, err)
			return
		}
		w.Header().Set("Verified-Account", p.Account.ID)
		w.WriteHeader(204)
	}))
	var wg sync.WaitGroup
	for n := 0; n < 40; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			token, id := a, hostAccountA
			if n%2 != 0 {
				token, id = b, hostAccountB
			}
			w := hostRequest(h, "GET", "/", token, "")
			if w.Code != 204 || w.Header().Get("Verified-Account") != id {
				t.Error(w.Code, w.Header())
			}
		}(n)
	}
	wg.Wait()
}
