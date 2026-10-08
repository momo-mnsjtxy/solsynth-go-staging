// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/oauthpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type oauthFixture struct {
	developerFixture
	app                                            apps.App
	query                                          string
	repo                                           *oauthpg.Repository
	issuer                                         *authority.Service
	verifier                                       *authority.Verifier
	transactionGuard                               *authoritypg.TransactionGuard
	clock                                          *atomic.Int64
	ctx                                            context.Context
	subject, userToken, ownerToken, keyDir, secret string
	service                                        http.Handler
}

func newOAuthFixture(t *testing.T, confidential bool) oauthFixture {
	t.Helper()
	base := newDeveloperFixture(t)
	_, app, q := base.setup(t)
	cfg := &apps.OAuthConfig{RedirectURIs: []string{"https://synthetic-client.invalid/callback"}, AllowedScopes: []string{"openid", "profile", "email", "offline_access", apps.OAuthClientAuthScope}, AllowedGrantTypes: []string{"authorization_code", "refresh_token"}, RequirePKCE: true, AllowOfflineAccess: true, IsPublicClient: !confidential}
	status := 1
	app = backendDecode[apps.App](t, backendCall(t, base.handler, "PATCH", "/api/private/apps/"+app.ID+q, base.token, devJSON(t, apps.AppPatch{Status: &status, OAuthConfig: cfg}), 200))
	query(t, base.db, signinpg.Schema)
	query(t, base.db, oauthpg.Schema)
	f := oauthFixture{developerFixture: base, app: app, query: q, subject: base.other, clock: &atomic.Int64{}, keyDir: authorityKeys(t)}
	f.clock.Store(time.Now().Unix())
	config := authorityConfig(t, f.keyDir, f.clock)
	store := authoritypg.New(base.db)
	var e error
	f.issuer, e = authority.New(store, config)
	if e != nil {
		t.Fatal(e)
	}
	f.verifier, e = authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	f.transactionGuard = authoritypg.NewTransactionGuard(store, f.verifier)
	f.repo, e = oauthpg.NewWithAuthority(base.db, store, f.issuer, f.transactionGuard, apppg.NewWithAuthority(base.db, f.transactionGuard))
	if e != nil {
		t.Fatal(e)
	}
	f.userToken = pgIssue(t, f.issuer, f.subject).Token
	f.ownerToken = pgIssue(t, f.issuer, base.actor).Token
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	p, e := f.verifier.Authenticate(ctx, f.userToken)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.transactionGuard.Bind(ctx, p)
	f.service = (&oauth.Service{Repository: f.repo, Verifier: f.verifier, Metadata: f.issuer, Bind: f.transactionGuard.Bind, AuthorizationEndpoint: "https://synthetic-consent.invalid/authorize"}).Handler()
	query(t, base.db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at)VALUES($1,$2,clock_timestamp())`, f.subject, "synthetic-oauth-"+f.subject+"@example.invalid")
	if confidential {
		secret := backendDecode[apps.Secret](t, backendCall(t, base.handler, "POST", "/api/private/apps/"+app.ID+"/secrets"+q, base.token, devJSON(t, apps.SecretRequest{Scopes: []string{apps.OAuthClientAuthScope}}), 201))
		f.secret = *secret.Secret
	}
	if e = f.repo.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func oauthRequest(f oauthFixture) (oauth.Request, string) {
	verifier := strings.Repeat("v", 43)
	h := sha256.Sum256([]byte(verifier))
	return oauth.Request{ClientID: f.app.Slug, RedirectURI: "https://synthetic-client.invalid/callback", ResponseType: "code", Scope: "openid profile email offline_access", State: "synthetic-state-" + authorityRandom(), CodeChallenge: base64.RawURLEncoding.EncodeToString(h[:]), CodeChallengeMethod: "S256", Nonce: "synthetic-nonce-" + authorityRandom()}, verifier
}
func authorityRandom() string {
	h := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func oauthCode(t *testing.T, f oauthFixture) (oauth.Request, string, string) {
	t.Helper()
	req, verifier := oauthRequest(f)
	if _, e := f.repo.Preview(f.ctx, f.subject, req); e != nil {
		t.Fatal(e)
	}
	decision, e := f.repo.Decide(f.ctx, f.subject, req, true)
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(decision.RedirectURI)
	if e != nil || u.Query().Get("state") != req.State || u.Query().Get("iss") != f.issuer.OAuthIssuer() {
		t.Fatal("redirect binding", e)
	}
	return req, verifier, u.Query().Get("code")
}
func oauthExchange(t *testing.T, f oauthFixture) oauth.Tokens {
	t.Helper()
	req, verifier, code := oauthCode(t, f)
	tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, ClientSecret: f.secret, RedirectURI: req.RedirectURI, Code: code, CodeVerifier: verifier})
	if e != nil {
		t.Fatal(e)
	}
	return tokens
}
func oauthBound(t *testing.T, f oauthFixture, token string) context.Context {
	t.Helper()
	p, e := f.verifier.AuthenticateOAuth(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	return f.transactionGuard.Bind(f.ctx, p)
}
func oauthForm(req oauth.Request) url.Values {
	return url.Values{"client_id": {req.ClientID}, "redirect_uri": {req.RedirectURI}, "response_type": {req.ResponseType}, "scope": {req.Scope}, "state": {req.State}, "code_challenge": {req.CodeChallenge}, "code_challenge_method": {req.CodeChallengeMethod}, "nonce": {req.Nonce}}
}
func oauthHTTP(t *testing.T, h http.Handler, method, path, token string, form url.Values, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("OAuth %s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}

func TestPGOAuthAuthorizationCodePKCEConsentAndUserInfo(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v := oauthRequest(f)
	form := oauthForm(req)
	oauthHTTP(t, f.service, "GET", "/stargate/auth/open/authorize?"+form.Encode(), "", nil, 401)
	preview := backendDecode[oauth.ClientInfo](t, oauthHTTP(t, f.service, "GET", "/stargate/auth/open/authorize?"+form.Encode(), f.userToken, nil, 200))
	if preview.ClientID != f.app.Slug || len(preview.Scopes) != 4 {
		t.Fatal(preview)
	}
	form.Set("authorize", "true")
	decision := backendDecode[oauth.Decision](t, oauthHTTP(t, f.service, "POST", "/stargate/auth/open/authorize", f.userToken, form, 200))
	redirect, _ := url.Parse(decision.RedirectURI)
	code := redirect.Query().Get("code")
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "client_id": {req.ClientID}, "redirect_uri": {req.RedirectURI}, "code": {code}, "code_verifier": {v}}
	tokens := backendDecode[oauth.Tokens](t, oauthHTTP(t, f.service, "POST", "/stargate/auth/open/token", "", tokenForm, 200))
	if tokens.IDToken == "" || tokens.RefreshToken == "" || tokens.TokenType != "Bearer" {
		t.Fatal("missing OAuth token contract")
	}
	if _, e := f.verifier.Authenticate(f.ctx, tokens.AccessToken); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("delegated token entered ordinary lane", e)
	}
	info := backendDecode[map[string]any](t, oauthHTTP(t, f.service, "GET", "/stargate/auth/open/userinfo", tokens.AccessToken, nil, 200))
	if info["sub"] != f.subject || info["email_verified"] != true || info["preferred_username"] == nil {
		t.Fatal(info)
	}
	backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, `{"name":"Synthetic display update"}`, 200)
	if _, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e != nil {
		t.Fatal("display-only app edit invalidated grant", e)
	}
	p, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	var count int
	query(t, f.db, `SELECT 1`)
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_authority_sessions WHERE id=$1 AND account_id=$2 AND type=1 AND category='oauth-v1' AND refresh_hash IS NULL AND parent_id=$3`, p.Session.ID, f.subject, p.Session.ParentID).Scan(&count); e != nil || count != 1 {
		t.Fatal("canonical session linkage", count, e)
	}
	h := sha256.Sum256([]byte("solsynth.oauth.v1\x00code\x00" + code))
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_oauth_codes WHERE hash=$1 AND used_at IS NOT NULL AND session_id=$2`, h[:], p.Session.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("hashed one-use code", count, e)
	}
	oauthHTTP(t, f.service, "GET", "/stargate/.well-known/openid-configuration", "", nil, 200)
	oauthHTTP(t, f.service, "GET", "/stargate/auth/open/jwks", "", nil, 200)
	deleteForm := url.Values{"client_id": {f.app.Slug}}
	oauthHTTP(t, f.service, "DELETE", "/stargate/auth/open/consent", f.userToken, deleteForm, 204)
	if _, e = f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e == nil {
		t.Fatal("removed consent accepted")
	}
}

func TestPGOAuthRedirectStateNonceAndParentBindings(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v := oauthRequest(f)
	bad := req
	bad.RedirectURI += "/extra"
	if _, e := f.repo.Preview(f.ctx, f.subject, bad); !errors.Is(e, oauth.ErrClient) {
		t.Fatal("unregistered redirect", e)
	}
	bad = req
	bad.Scope = "openid posts.create"
	if _, e := f.repo.Preview(f.ctx, f.subject, bad); !errors.Is(e, oauth.ErrScope) {
		t.Fatal("unmapped resource scope", e)
	}
	if _, e := f.repo.Decide(f.ctx, f.subject, req, true); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("consent without preview", e)
	}
	if _, e := f.repo.Preview(f.ctx, f.subject, req); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*oauth.Request){func(r *oauth.Request) { r.Nonce += "x" }, func(r *oauth.Request) { r.State += "x" }, func(r *oauth.Request) { r.CodeChallenge = strings.Repeat("a", 43) }} {
		changed := req
		mutate(&changed)
		if _, e := f.repo.Decide(f.ctx, f.subject, changed, true); e == nil {
			t.Fatal("request substitution")
		}
	}
	second := pgIssue(t, f.issuer, f.subject)
	p, e := f.verifier.Authenticate(f.ctx, second.Token)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Decide(f.transactionGuard.Bind(f.ctx, p), f.subject, req, true); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("different parent session approved", e)
	}
	decision, e := f.repo.Decide(f.ctx, f.subject, req, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(decision.RedirectURI)
	tokenReq := oauth.TokenRequest{GrantType: "authorization_code", Code: u.Query().Get("code"), ClientID: req.ClientID, RedirectURI: req.RedirectURI, CodeVerifier: strings.Repeat("x", 43)}
	if _, e = f.repo.Token(f.ctx, tokenReq); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("wrong PKCE", e)
	}
	tokenReq.CodeVerifier = v
	tokens, e := f.repo.Token(f.ctx, tokenReq)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Token(f.ctx, tokenReq); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("reused code", e)
	}
	if _, e = f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e == nil {
		t.Fatal("code replay did not revoke session")
	}
	deny, _ := oauthRequest(f)
	if _, e = f.repo.Preview(f.ctx, f.subject, deny); e != nil {
		t.Fatal(e)
	}
	decision, e = f.repo.Decide(f.ctx, f.subject, deny, false)
	if e != nil {
		t.Fatal(e)
	}
	u, _ = url.Parse(decision.RedirectURI)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" || u.Query().Get("state") != deny.State {
		t.Fatal("denial response")
	}
}

func TestPGOAuthConcurrentCodeAndRefreshReuse(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v, code := oauthCode(t, f)
	request := oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, Code: code, CodeVerifier: v, RedirectURI: req.RedirectURI}
	var wg sync.WaitGroup
	var success atomic.Int64
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.repo.Token(f.ctx, request)
			if e == nil {
				success.Add(1)
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, oauth.ErrGrant) {
			t.Fatal(e)
		}
	}
	if success.Load() != 1 {
		t.Fatal("code consumed more than once", success.Load())
	}
	tokens := oauthExchange(t, f)
	refresh := oauth.TokenRequest{GrantType: "refresh_token", ClientID: f.app.Slug, RefreshToken: tokens.RefreshToken}
	first, e := f.repo.Token(f.ctx, refresh)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e == nil {
		t.Fatal("old access epoch remained live")
	}
	refresh.RefreshToken = first.RefreshToken
	success.Store(0)
	results = make(chan error, 2)
	out := make(chan oauth.Tokens, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pair, e := f.repo.Token(f.ctx, refresh)
			if e == nil {
				success.Add(1)
				out <- pair
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	close(out)
	for e := range results {
		if e != nil && !errors.Is(e, oauth.ErrGrant) {
			t.Fatal(e)
		}
	}
	if success.Load() != 1 {
		t.Fatal("refresh consumed more than once", success.Load())
	}
	for pair := range out {
		if _, e = f.verifier.AuthenticateOAuth(f.ctx, pair.AccessToken); e == nil {
			t.Fatal("refresh reuse did not revoke winning token")
		}
	}
}

func TestPGOAuthAppOwnerConsentAndSecretRevocation(t *testing.T) {
	for _, kind := range []string{"app", "owner", "consent", "secret"} {
		t.Run(kind, func(t *testing.T) {
			f := newOAuthFixture(t, kind == "secret")
			tokens := oauthExchange(t, f)
			switch kind {
			case "app":
				backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, `{"status":2}`, 200)
				backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, `{"status":1}`, 200)
			case "owner":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor)
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
			case "consent":
				if e := f.repo.RevokeConsent(f.ctx, f.subject, f.app.Slug); e != nil {
					t.Fatal(e)
				}
				_ = oauthExchange(t, f)
			case "secret":
				parts := strings.Split(f.secret, ".")
				backendCall(t, f.handler, "POST", "/api/private/apps/"+f.app.ID+"/secrets/"+parts[1]+"/rotate"+f.query, f.token, `{}`, 200)
			}
			if _, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e == nil {
				t.Fatal("revoked binding revived", kind)
			}
			if _, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "refresh_token", ClientID: f.app.Slug, ClientSecret: f.secret, RefreshToken: tokens.RefreshToken}); e == nil {
				t.Fatal("revoked refresh accepted", kind)
			}
		})
	}
}

func TestPGOAuthParentRevocationAndTokenExpiry(t *testing.T) {
	f := newOAuthFixture(t, false)
	tokens := oauthExchange(t, f)
	if e := f.issuer.Revoke(f.ctx, f.userToken, "current"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("parent cascade", e)
	}
	g := newOAuthFixture(t, false)
	req, v, code := oauthCode(t, g)
	query(t, g.db, `UPDATE owned_oauth_codes SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, g.subject)
	if _, e := g.repo.Token(g.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, Code: code, CodeVerifier: v, RedirectURI: req.RedirectURI}); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("expired code", e)
	}
	tokens = oauthExchange(t, g)
	g.clock.Add(601)
	if _, e := g.verifier.AuthenticateOAuth(g.ctx, tokens.AccessToken); !errors.Is(e, authority.ErrExpired) {
		t.Fatal("access expiry", e)
	}
}

func TestPGOAuthAtomicRollbackAndCanonicalProof(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v, code := oauthCode(t, f)
	request := oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, Code: code, CodeVerifier: v, RedirectURI: req.RedirectURI}
	constraint := "synthetic_oauth_fail_" + strings.ReplaceAll(f.subject, "-", "")
	query(t, f.db, `ALTER TABLE owned_oauth_audit ADD CONSTRAINT `+constraint+` CHECK(account_id<>'`+f.subject+`'::uuid OR action<>'code.exchanged')NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_oauth_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.Token(f.ctx, request); e == nil {
		t.Fatal("injected failure committed")
	}
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_authority_sessions WHERE account_id=$1 AND type=1`, f.subject).Scan(&n); e != nil || n != 0 {
		t.Fatal("orphan OAuth session", n, e)
	}
	query(t, f.db, `ALTER TABLE owned_oauth_audit DROP CONSTRAINT `+constraint)
	tokens, e := f.repo.Token(f.ctx, request)
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.UserInfo(context.Background(), f.subject); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound principal accepted", e)
	}
	otherVerifier, e := authority.NewVerifier(authoritypg.New(f.db), authorityConfig(t, f.keyDir, f.clock))
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := otherVerifier.AuthenticateOAuth(f.ctx, tokens.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.UserInfo(f.transactionGuard.Bind(f.ctx, foreign), f.subject); e == nil {
		t.Fatal("foreign verifier proof accepted")
	}
	if _, e = f.repo.UserInfo(f.transactionGuard.Bind(f.ctx, p), f.subject); e != nil {
		t.Fatal("original proof failed", e)
	}
}

func TestPGOAuthOwnerDisableDuringLockWait(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v, code := oauthCode(t, f)
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.actor}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: req.ClientID, Code: code, CodeVerifier: v, RedirectURI: req.RedirectURI})
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("owner disable crossed token issuance")
	}
	var n int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_authority_sessions WHERE account_id=$1 AND type=1`, f.subject).Scan(&n); e != nil || n != 0 {
		t.Fatal("disabled owner minted session", n, e)
	}
}

func TestPGOAuthOpposingOwnersAndRetryableGuardBoundary(t *testing.T) {
	a, b := newOAuthFixture(t, false), newOAuthFixture(t, false)
	for _, entry := range []struct {
		f     *oauthFixture
		actor string
	}{{&a, b.actor}, {&b, a.actor}} {
		entry.f.subject = entry.actor
		entry.f.userToken = pgIssue(t, entry.f.issuer, entry.actor).Token
		p, e := entry.f.verifier.Authenticate(entry.f.ctx, entry.f.userToken)
		if e != nil {
			t.Fatal(e)
		}
		entry.f.ctx = entry.f.transactionGuard.Bind(entry.f.ctx, p)
	}
	done := make(chan error, 2)
	start := make(chan struct{})
	for _, f := range []oauthFixture{a, b} {
		go func(f oauthFixture) {
			<-start
			req, _ := oauthRequest(f)
			_, e := f.repo.Preview(f.ctx, f.subject, req)
			if e == nil {
				_, e = f.repo.Decide(f.ctx, f.subject, req, true)
			}
			done <- e
		}(f)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal("opposing account order", e)
		}
	}
	tokens := oauthExchange(t, a)
	proofCtx := oauthBound(t, a, tokens.AccessToken)
	base := atomicAuthorityFixture{db: a.db, ctx: a.ctx}
	blocker, _ := atomicBlocker(t, base, "owned_publishers", a.app.PublisherID)
	if _, e := a.repo.UserInfo(proofCtx, a.subject); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("secondary lock was not retryable", e)
	}
	if e := blocker.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, e := a.repo.UserInfo(proofCtx, a.subject); e != nil {
		t.Fatal("retry after lock release", e)
	}
}

func TestPGOAuthScopeMinimizationAndBoundedCleanup(t *testing.T) {
	f := newOAuthFixture(t, false)
	req, v := oauthRequest(f)
	req.Scope = "openid"
	if _, e := f.repo.Preview(f.ctx, f.subject, req); e != nil {
		t.Fatal(e)
	}
	decision, e := f.repo.Decide(f.ctx, f.subject, req, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(decision.RedirectURI)
	tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", Code: u.Query().Get("code"), ClientID: req.ClientID, RedirectURI: req.RedirectURI, CodeVerifier: v})
	if e != nil {
		t.Fatal(e)
	}
	if tokens.RefreshToken != "" || tokens.Scope != "openid" {
		t.Fatal("unapproved offline access")
	}
	info, e := f.repo.UserInfo(oauthBound(t, f, tokens.AccessToken), f.subject)
	if e != nil || len(info) != 1 || info["sub"] != f.subject {
		t.Fatal("unapproved identity claims", info, e)
	}
	query(t, f.db, `UPDATE owned_oauth_requests SET expires_at=clock_timestamp()-interval '25 hours' WHERE account_id=$1`, f.subject)
	query(t, f.db, `UPDATE owned_oauth_grants SET expires_at=clock_timestamp()-interval '25 hours' WHERE account_id=$1`, f.subject)
	if _, e = f.repo.ProcessDue(f.ctx, 101); !errors.Is(e, oauth.ErrRequest) {
		t.Fatal("unbounded cleanup", e)
	}
	if n, e := f.repo.ProcessDue(f.ctx, 1); e != nil || n != 1 {
		t.Fatal("cleanup first bound", n, e)
	}
	if n, e := f.repo.ProcessDue(f.ctx, 100); e != nil || n < 1 {
		t.Fatal("cleanup remaining root", n, e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_oauth_grants WHERE account_id=$1`, f.subject).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired grant retained", count, e)
	}
	if _, e = f.verifier.AuthenticateOAuth(f.ctx, tokens.AccessToken); e == nil {
		t.Fatal("retired binding accepted")
	}
}

func TestPGOAuthRestartPersistence(t *testing.T) {
	db := database(t)
	root := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-oauth-restart")
	statePath := filepath.Join(root, "state.json")
	type state struct {
		Subject, Client, Access, Refresh string
		Clock                            int64
	}
	var saved state
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newOAuthFixture(t, false)
		tokens := oauthExchange(t, f)
		if e := os.MkdirAll(root, 0700); e != nil {
			t.Fatal(e)
		}
		for _, name := range []string{"synthetic-signing.pem", "synthetic-hash.bin", "synthetic-seal.bin"} {
			raw, e := os.ReadFile(filepath.Join(f.keyDir, name))
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, name), raw, 0600); e != nil {
				t.Fatal(e)
			}
		}
		saved = state{f.subject, f.app.Slug, tokens.AccessToken, tokens.RefreshToken, f.clock.Load()}
		raw, _ := json.Marshal(saved)
		if e := os.WriteFile(statePath, raw, 0600); e != nil {
			t.Fatal(e)
		}
	} else if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(statePath)
		if e != nil || json.Unmarshal(raw, &saved) != nil {
			t.Fatal("missing synthetic OAuth state", e)
		}
	} else {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	clock := &atomic.Int64{}
	clock.Store(saved.Clock)
	config := authorityConfig(t, root, clock)
	store := authoritypg.New(db)
	issuer, e := authority.New(store, config)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(store, config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(store, v)
	repo, e := oauthpg.NewWithAuthority(db, store, issuer, guard, apppg.NewWithAuthority(db, guard))
	if e != nil {
		t.Fatal(e)
	}
	p, e := v.AuthenticateOAuth(context.Background(), saved.Access)
	if e != nil || p.Account.ID != saved.Subject {
		t.Fatal("persisted canonical OAuth session", e)
	}
	if _, e = repo.UserInfo(guard.Bind(context.Background(), p), saved.Subject); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("PG_TEST_PHASE") == "after" {
		tokens, e := repo.Token(context.Background(), oauth.TokenRequest{GrantType: "refresh_token", ClientID: saved.Client, RefreshToken: saved.Refresh})
		if e != nil || tokens.AccessToken == "" {
			t.Fatal("restart refresh", e)
		}
		if _, e = v.AuthenticateOAuth(context.Background(), tokens.AccessToken); e != nil {
			t.Fatal(e)
		}
	}
}
