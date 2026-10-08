// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const accountA = "a1000000-0000-4000-8000-000000000001"
const accountB = "a1000000-0000-4000-8000-000000000002"

type memState struct {
	account     Account
	sessions    map[string]Session
	codes       map[string]LoginCode
	rules       []Rule
	punishments []Punishment
}
type memStore struct {
	mu          sync.Mutex
	state       map[string]*memState
	failure     error
	saveFailure bool
}
type memTx struct {
	state *memState
	fail  bool
}

func cloneState(v *memState) *memState {
	n := &memState{account: v.account, sessions: map[string]Session{}, codes: map[string]LoginCode{}, rules: append([]Rule(nil), v.rules...), punishments: append([]Punishment(nil), v.punishments...)}
	for k, se := range v.sessions {
		se.Scopes = append([]string(nil), se.Scopes...)
		se.RefreshHash = append([]byte(nil), se.RefreshHash...)
		se.PreviousHash = append([]byte(nil), se.PreviousHash...)
		se.SealedPair = append([]byte(nil), se.SealedPair...)
		n.sessions[k] = se
	}
	for k, c := range v.codes {
		c.Hash = append([]byte(nil), c.Hash...)
		c.Scopes = append([]string(nil), c.Scopes...)
		n.codes[k] = c
	}
	return n
}
func (m *memStore) WithinAccount(ctx context.Context, id string, fn func(Transaction) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	if m.failure != nil {
		return m.failure
	}
	state := m.state[id]
	if state == nil {
		return ErrNotFound
	}
	cp := cloneState(state)
	if e := fn(&memTx{cp, m.saveFailure}); e != nil {
		return e
	}
	m.state[id] = cp
	return nil
}
func (m *memStore) ReadAccount(ctx context.Context, id string, fn func(Transaction) error) error {
	return m.WithinAccount(ctx, id, fn)
}
func (m *memStore) FindRefresh(ctx context.Context, h []byte) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure != nil {
		return "", "", m.failure
	}
	for a, v := range m.state {
		for id, s := range v.sessions {
			if bytes.Equal(s.RefreshHash, h) || bytes.Equal(s.PreviousHash, h) {
				return a, id, nil
			}
		}
	}
	return "", "", ErrNotFound
}
func (m *memStore) FindCode(ctx context.Context, h []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for a, v := range m.state {
		if _, ok := v.codes[string(h)]; ok {
			return a, nil
		}
	}
	return "", ErrNotFound
}
func (m *memTx) Account() Account            { return m.state.account }
func (m *memTx) SaveAccount(a Account) error { m.state.account = a; return nil }
func (m *memTx) Session(id string) (Session, error) {
	s, ok := m.state.sessions[id]
	if !ok {
		return s, ErrNotFound
	}
	return s, nil
}
func (m *memTx) Sessions() ([]Session, error) {
	out := []Session{}
	for _, v := range m.state.sessions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *memTx) SaveSession(s Session) error {
	if m.fail {
		return errors.New("injected persistence failure")
	}
	var prior *Session
	if old, ok := m.state.sessions[s.ID]; ok {
		prior = &old
	}
	next, e := PrepareSessionWrite(prior, s)
	if e != nil {
		return e
	}
	m.state.sessions[s.ID] = next
	return nil
}
func (m *memTx) Rules() ([]Rule, error)             { return m.state.rules, nil }
func (m *memTx) Punishments() ([]Punishment, error) { return m.state.punishments, nil }
func (m *memTx) Code(h []byte) (LoginCode, error) {
	c, ok := m.state.codes[string(h)]
	if !ok {
		return c, ErrNotFound
	}
	return c, nil
}
func (m *memTx) SaveCode(c LoginCode) error { m.state.codes[string(c.Hash)] = c; return nil }

var testKeyOnce sync.Once
var testKey *rsa.PrivateKey

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		var e error
		testKey, e = rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			panic(e)
		}
	})
	return testKey
}
func setup(t *testing.T) (*Service, *memStore, *atomic.Int64) {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).Unix())
	m := &memStore{state: map[string]*memState{}}
	for _, id := range []string{accountA, accountB} {
		m.state[id] = &memState{account: Account{ID: id, Name: "synthetic", Nick: "Synthetic", Version: 1, CreatedAt: time.Unix(now.Load(), 0)}, sessions: map[string]Session{}, codes: map[string]LoginCode{}}
	}
	k := key(t)
	s, e := New(m, Config{Issuer: "https://synthetic.invalid", Audience: "synthetic-api", SigningKeyID: "test", SigningKey: k, TrustedKeys: map[string]*rsa.PublicKey{"test": &k.PublicKey}, RefreshHashKey: bytes.Repeat([]byte{1}, 32), ReplaySealKey: bytes.Repeat([]byte{2}, 32), AccessTTL: 10 * time.Minute, SessionTTL: time.Hour, RefreshGrace: 60 * time.Second, Now: func() time.Time { return time.Unix(now.Load(), 0) }})
	if e != nil {
		t.Fatal(e)
	}
	return s, m, now
}
func issue(t *testing.T, s *Service, a string, scope ...string) Pair {
	t.Helper()
	p, e := s.IssueSession(context.Background(), a, SessionOptions{Label: "synthetic", Scopes: scope})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func want(t *testing.T, e, errorWant error) {
	t.Helper()
	if !errors.Is(e, errorWant) {
		t.Fatalf("got %v want %v", e, errorWant)
	}
}
func TestAuthorityLifecycleAndIsolation(t *testing.T) {
	s, m, now := setup(t)
	ctx := context.Background()
	p := issue(t, s, accountA, "*")
	b := issue(t, s, accountB, "*")
	principal, e := s.Authenticate(ctx, p.Token)
	if e != nil || principal.Account.ID != accountA || len(principal.Session.RefreshHash) != 0 || len(principal.Session.SealedPair) != 0 {
		t.Fatal(principal, e)
	}
	other, _ := s.Authenticate(ctx, b.Token)
	if e = s.Revoke(ctx, p.Token, other.Session.ID); e == nil {
		t.Fatal("cross-account revoke")
	}
	if _, e = s.Authenticate(ctx, b.Token); e != nil {
		t.Fatal(e)
	}
	list, total, e := s.Sessions(ctx, p.Token, 0, 50, false)
	if e != nil || total != 1 || len(list) != 1 || !list[0].IsCurrent {
		t.Fatal(list, total, e)
	}
	now.Add(600)
	_, e = s.Authenticate(ctx, p.Token)
	want(t, e, ErrExpired)
	rot, e := s.Refresh(ctx, p.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Authenticate(ctx, p.Token)
	want(t, e, ErrStale)
	if _, e = s.Authenticate(ctx, rot.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.Revoke(ctx, rot.Token, "current"); e != nil {
		t.Fatal(e)
	}
	_, e = s.Authenticate(ctx, rot.Token)
	want(t, e, ErrRevoked)
	m.failure = errors.New("database offline")
	_, e = s.Authenticate(ctx, b.Token)
	want(t, e, ErrUnavailable)
}
func TestAuthorityConcurrentRefreshGraceAndStorage(t *testing.T) {
	s, m, now := setup(t)
	ctx := context.Background()
	p := issue(t, s, accountA, "*")
	const n = 20
	got := make(chan Pair, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); pair, e := s.Refresh(ctx, p.RefreshToken, 0, ""); got <- pair; errs <- e }()
	}
	wg.Wait()
	close(got)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var canonical Pair
	for pair := range got {
		if canonical.Token == "" {
			canonical = pair
		}
		if pair.Token != canonical.Token || pair.RefreshToken != canonical.RefreshToken {
			t.Fatal("concurrent rotations diverged")
		}
	}
	m.mu.Lock()
	for _, se := range m.state[accountA].sessions {
		if se.Epoch != 2 || se.AuthorizationEpoch != 1 || bytes.Contains(se.SealedPair, []byte(canonical.RefreshToken)) || bytes.Equal(se.RefreshHash, []byte(canonical.RefreshToken)) {
			t.Fatal("unsafe or repeated rotation")
		}
	}
	m.mu.Unlock()
	now.Add(60)
	_, e := s.Refresh(ctx, p.RefreshToken, 0, "")
	want(t, e, ErrRefresh)
	if _, e = s.Authenticate(ctx, canonical.Token); e != nil {
		t.Fatal(e)
	}
	m.saveFailure = true
	_, e = s.Refresh(ctx, canonical.RefreshToken, 0, "")
	want(t, e, ErrUnavailable)
	m.saveFailure = false
	if _, e = s.Authenticate(ctx, canonical.Token); e != nil {
		t.Fatal("failed rotation did not roll back", e)
	}
}
func TestAuthorityGrantScopePunishmentAndProof(t *testing.T) {
	s, m, now := setup(t)
	ctx := context.Background()
	p := issue(t, s, accountA, "posts.*")
	_, e := s.Authorize(ctx, p.Token, "posts.create")
	want(t, e, ErrForbidden)
	m.state[accountA].rules = []Rule{{Pattern: "posts.*"}}
	principal, e := s.Authorize(ctx, p.Token, "posts.create")
	if e != nil {
		t.Fatal(e)
	}
	tampered := principal
	tampered.Account.ID = accountB
	_, e = s.AuthorizePrincipal(ctx, tampered, "posts.create")
	want(t, e, ErrInvalid)
	tampered = principal
	tampered.Session.Scopes = append([]string(nil), principal.Session.Scopes...)
	tampered.Session.Scopes[0] = "*"
	_, e = s.AuthorizePrincipal(ctx, tampered, "posts.create")
	want(t, e, ErrInvalid)
	tampered = principal
	tampered.Session.Epoch++
	_, e = s.AuthorizePrincipal(ctx, tampered, "posts.create")
	want(t, e, ErrInvalid)
	again, e := s.AuthorizePrincipal(ctx, principal, "posts.create")
	if e != nil || again.Account.ID != accountA {
		t.Fatal(e)
	}
	_, e = s.AuthorizePrincipal(ctx, Principal{Account: Account{ID: accountA}}, "posts.create")
	want(t, e, ErrInvalid)
	_, e = s.Authorize(ctx, p.Token, "postscript.create")
	want(t, e, ErrForbidden)
	m.state[accountA].account.Superuser = true
	_, e = s.Authorize(ctx, p.Token, "wallets.balance.modify")
	want(t, e, ErrForbidden)
	end := time.Unix(now.Load()+1, 0)
	m.state[accountA].punishments = []Punishment{{Kind: "permission_modification", Pattern: "posts.*", ExpiresAt: &end}}
	_, e = s.Authorize(ctx, p.Token, "posts.create")
	want(t, e, ErrForbidden)
	now.Add(1)
	if _, e = s.Authorize(ctx, p.Token, "posts.create"); e != nil {
		t.Fatal(e)
	}
	m.state[accountA].rules = append(m.state[accountA].rules, Rule{Pattern: "posts.create", Deny: true})
	_, e = s.Authorize(ctx, p.Token, "posts.create")
	want(t, e, ErrForbidden)
	if e = s.InvalidateAccount(ctx, accountA); e != nil {
		t.Fatal(e)
	}
	_, e = s.AuthorizePrincipal(ctx, principal, "posts.create")
	want(t, e, ErrStale)
	_, e = s.Refresh(ctx, p.RefreshToken, 0, "")
	want(t, e, ErrStale)
}
func TestAuthorityOneUseCodeAndVersionInvalidation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	code, e := s.CreateLoginCode(ctx, accountA, "synthetic", []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.ExchangeCode(ctx, code)
	if e != nil || p.Token == "" {
		t.Fatal(e)
	}
	_, e = s.ExchangeCode(ctx, code)
	want(t, e, ErrInvalid)
	code, e = s.CreateLoginCode(ctx, accountA, "synthetic", []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InvalidateAccount(ctx, accountA); e != nil {
		t.Fatal(e)
	}
	_, e = s.ExchangeCode(ctx, code)
	want(t, e, ErrInvalid)
}
func signedRaw(t *testing.T, k *rsa.PrivateKey, h, p string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	v := enc.EncodeToString([]byte(h)) + "." + enc.EncodeToString([]byte(p))
	sum := sha256.Sum256([]byte(v))
	sig, e := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if e != nil {
		t.Fatal(e)
	}
	return v + "." + enc.EncodeToString(sig)
}
func TestAuthorityJWTValidationIsStrict(t *testing.T) {
	s, _, now := setup(t)
	p := issue(t, s, accountA, "*")
	c, e := s.verify(p.Token)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(c)
	header := `{"alg":"RS256","typ":"JWT","kid":"test"}`
	cases := map[string]string{"alg": signedRaw(t, key(t), `{"alg":"HS256","typ":"JWT","kid":"test"}`, string(raw)), "kid": signedRaw(t, key(t), `{"alg":"RS256","typ":"JWT","kid":"unknown"}`, string(raw)), "duplicate": signedRaw(t, key(t), header, strings.Replace(string(raw), `"iss":`, `"iss":"ignored","iss":`, 1)), "unsigned": strings.Join(strings.Split(p.Token, ".")[:2], ".") + "."}
	cases["case_alias"] = signedRaw(t, key(t), header, strings.Replace(string(raw), `"iss":`, `"ISS":`, 1))
	for _, change := range []string{"issuer", "audience", "future", "lane", "epoch", "sid"} {
		v := c
		switch change {
		case "issuer":
			v.Issuer = "untrusted"
		case "audience":
			v.Audience = "other"
		case "future":
			v.Issued = now.Load() + 1
			v.NotBefore = v.Issued
		case "lane":
			v.Use = "refresh"
		case "epoch":
			v.Epoch = 0
		case "sid":
			v.ID = accountB
		}
		b, _ := json.Marshal(v)
		cases[change] = signedRaw(t, key(t), header, string(b))
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) { _, e := s.Authenticate(context.Background(), token); want(t, e, ErrInvalid) })
	}
}
func request(s *Service, method, path, body, token, content string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if content != "" {
		r.Header.Set("Content-Type", content)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestAuthorityHTTPClientContractsAndOAuthLane(t *testing.T) {
	s, _, now := setup(t)
	p := issue(t, s, accountA, "*")
	w := request(s, "GET", "/stargate/accounts/me", "", p.Token, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"`+accountA+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(s, "GET", "/stargate/sessions?offset=0&take=50&includeChildren=false", "", p.Token, "")
	if w.Code != 200 || w.Header().Get("X-Total") != "1" || !strings.Contains(w.Body.String(), `"is_current":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(s, "POST", "/stargate/auth/token", fmt.Sprintf(`{"grant_type":"refresh_token","refresh_token":%q}`, p.RefreshToken), "", "application/json")
	var pair map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pair)
	if w.Code != 200 || pair["token"] == nil || pair["access_token"] != nil || pair["expires_in"] == nil || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	_, e := s.IssueSession(context.Background(), accountB, SessionOptions{Type: 1, ClientID: "synthetic-solian", Scopes: []string{"*"}})
	want(t, e, ErrInvalid)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {p.RefreshToken}, "client_id": {"synthetic-solian"}}
	w = request(s, "POST", "/stargate/auth/open/token", form.Encode(), "", "application/x-www-form-urlencoded")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatal("unbound OAuth lane accepted a first-party refresh", w.Code, w.Body.String())
	}
	w = request(s, "GET", "/stargate/accounts/me?tk="+p.Token, "", "", "")
	if w.Code != 401 {
		t.Fatal("query credential accepted")
	}
	r := httptest.NewRequest("GET", "/stargate/accounts/me", nil)
	r.Header.Set("X-Account-ID", accountA)
	r.AddCookie(&http.Cookie{Name: "AuthToken", Value: p.Token})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("untrusted identity")
	}
	now.Add(600)
	w = request(s, "GET", "/stargate/accounts/me", "", pairString(pair, "token"), "")
	if w.Code != 401 || !strings.Contains(w.Body.String(), "TOKEN_EXPIRED") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func pairString(p map[string]any, k string) string { s, _ := p[k].(string); return s }
func TestAuthorityBadConfigAndHeader(t *testing.T) {
	s, _, _ := setup(t)
	c := s.cfg
	c.TrustedKeys = map[string]*rsa.PublicKey{}
	if _, e := New(s.store, c); e == nil {
		t.Fatal("untrusted signer")
	}
	c = s.cfg
	c.ReplaySealKey = c.RefreshHashKey
	if _, e := New(s.store, c); e == nil {
		t.Fatal("same secret keys")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Add("Authorization", "Bearer one")
	r.Header.Add("Authorization", "Bearer two")
	_, e := BearerToken(r)
	want(t, e, ErrInvalid)
}

func TestAuthorityPublicKeyOnlyVerifier(t *testing.T) {
	s, m, _ := setup(t)
	p := issue(t, s, accountA, "posts.*")
	m.state[accountA].rules = []Rule{{Pattern: "posts.*"}}
	v, e := NewVerifier(m, Config{Issuer: s.cfg.Issuer, Audience: s.cfg.Audience, TrustedKeys: s.cfg.TrustedKeys, Now: s.cfg.Now})
	if e != nil {
		t.Fatal(e)
	}
	principal, e := v.Authenticate(context.Background(), p.Token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.AuthorizePrincipal(context.Background(), principal, "posts.create"); e != nil {
		t.Fatal(e)
	}
	if v.service.cfg.SigningKey != nil || len(v.service.cfg.RefreshHashKey) != 0 || len(v.service.cfg.ReplaySealKey) != 0 || v.service.seal != nil {
		t.Fatal("verifier retained signing or refresh capabilities")
	}
	if _, e = s.AuthorizePrincipal(context.Background(), principal, "posts.create"); !errors.Is(e, ErrInvalid) {
		t.Fatal("proof crossed verifier boundary")
	}
	if e = s.InvalidateAccount(context.Background(), accountA); e != nil {
		t.Fatal(e)
	}
	if _, e = v.AuthorizePrincipal(context.Background(), principal, "posts.create"); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
}
