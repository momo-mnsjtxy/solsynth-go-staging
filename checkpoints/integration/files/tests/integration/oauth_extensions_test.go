// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"sync"
	"testing"
	"time"
)

func enableDevice(t *testing.T, f *oauthFixture) {
	t.Helper()
	c := *f.app.OAuthConfig
	c.AllowedGrantTypes = append(c.AllowedGrantTypes, oauth.DeviceGrant)
	f.app = backendDecode[apps.App](t, backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, devJSON(t, apps.AppPatch{OAuthConfig: &c}), 200))
}
func pollableDevice(t *testing.T, f oauthFixture, raw string) {
	t.Helper()
	query(t, f.db, `UPDATE owned_oauth_devices SET last_polled_at=clock_timestamp()-make_interval(secs=>interval_seconds+1) WHERE hash=$1`, oauthDeviceHash(raw))
}
func oauthDeviceHash(raw string) []byte {
	h := sha256.Sum256([]byte("solsynth.oauth.v1\x00device\x00" + raw))
	return h[:]
}
func TestPGOAuthDevicePendingSlowdownConsentAndSingleUse(t *testing.T) {
	f := newOAuthFixture(t, false)
	enableDevice(t, &f)
	form := url.Values{"client_id": {f.app.Slug}, "scope": {"openid profile offline_access"}}
	d := backendDecode[oauth.DeviceAuthorization](t, oauthHTTP(t, f.service, "POST", "/auth/open/device", "", form, 200))
	if d.Interval != 5 || d.ExpiresIn <= 0 || len(d.DeviceCode) != 43 || d.VerificationURIComplete == "" {
		t.Fatal(d)
	}
	req := oauth.TokenRequest{GrantType: oauth.DeviceGrant, ClientID: f.app.Slug, DeviceCode: d.DeviceCode}
	for _, expected := range []string{"authorization_pending", "slow_down"} {
		_, e := f.repo.Token(f.ctx, req)
		var failure *oauth.Failure
		if !errors.As(e, &failure) || failure.Code != expected {
			t.Fatal(expected, e)
		}
	}
	if e := f.repo.DeviceDecide(f.ctx, f.subject, d.UserCode, true); !errors.Is(e, oauth.ErrGrant) {
		t.Fatal("unreviewed consent", e)
	}
	preview := backendDecode[oauth.ClientInfo](t, oauthHTTP(t, f.service, "GET", "/auth/open/device/verify?user_code="+url.QueryEscape(d.UserCode), f.userToken, nil, 200))
	if preview.ClientID != f.app.Slug || len(preview.Scopes) != 3 {
		t.Fatal(preview)
	}
	oauthHTTP(t, f.service, "POST", "/auth/open/device/verify", f.userToken, url.Values{"user_code": {d.UserCode}, "authorize": {"true"}}, 204)
	pollableDevice(t, f, d.DeviceCode)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var tokens []oauth.Tokens
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.repo.Token(f.ctx, req)
			if e == nil {
				mu.Lock()
				tokens = append(tokens, v)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(tokens) != 1 {
		t.Fatalf("single-use device successes %d", len(tokens))
	}
	p, e := f.verifier.AuthenticateOAuth(f.ctx, tokens[0].AccessToken)
	if e != nil || p.Account.ID != f.subject {
		t.Fatal(p.Account.ID, e)
	}
	if _, e = f.verifier.Authenticate(f.ctx, tokens[0].AccessToken); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("ordinary credential promotion", e)
	}
	var n int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_oauth_devices WHERE hash=$1 AND used_at IS NOT NULL AND session_id=$2 AND user_hash<>$3`, oauthDeviceHash(d.DeviceCode), p.Session.ID, []byte(d.UserCode)).Scan(&n); e != nil || n != 1 {
		t.Fatal("hashed durable consumed device", n, e)
	}
}
func TestPGOAuthDeviceDenyExpiryParentAndClientRevocation(t *testing.T) {
	f := newOAuthFixture(t, true)
	enableDevice(t, &f)
	for _, scenario := range []string{"denied", "expired", "parent_revoked", "client_changed"} {
		t.Run(scenario, func(t *testing.T) {
			d, e := f.repo.DeviceAuthorize(f.ctx, f.app.Slug, f.secret, "openid profile")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.repo.DevicePreview(f.ctx, f.subject, d.UserCode); e != nil {
				t.Fatal(e)
			}
			if e = f.repo.DeviceDecide(f.ctx, f.subject, d.UserCode, scenario != "denied"); e != nil {
				t.Fatal(e)
			}
			switch scenario {
			case "expired":
				query(t, f.db, `UPDATE owned_oauth_devices SET expires_at=clock_timestamp()-interval '1 second' WHERE hash=$1`, oauthDeviceHash(d.DeviceCode))
			case "parent_revoked":
				p, e := f.verifier.Authenticate(f.ctx, f.userToken)
				if e != nil {
					t.Fatal(e)
				}
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, p.Session.ID)
			case "client_changed":
				query(t, f.db, `UPDATE owned_custom_apps SET oauth_epoch=oauth_epoch+1 WHERE id=$1`, f.app.ID)
			}
			if _, e = f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: oauth.DeviceGrant, ClientID: f.app.Slug, ClientSecret: f.secret, DeviceCode: d.DeviceCode}); e == nil {
				t.Fatal("invalid device redeemed", scenario)
			}
			var n int
			if e = f.db.QueryRow(`SELECT count(*) FROM owned_oauth_devices WHERE hash=$1 AND session_id IS NOT NULL`, oauthDeviceHash(d.DeviceCode)).Scan(&n); e != nil || n != 0 {
				t.Fatal("denial committed grant", n, e)
			}
			// Each following scenario receives a fresh canonical parent proof.
			f.userToken = pgIssue(t, f.issuer, f.subject).Token
			p, e := f.verifier.Authenticate(f.ctx, f.userToken)
			if e != nil {
				t.Fatal(e)
			}
			f.ctx = f.transactionGuard.Bind(f.ctx, p)
		})
	}
}
func TestPGOAuthIntrospectionClientIsolationAndAccessExpiry(t *testing.T) {
	f := newOAuthFixture(t, true)
	tokens := oauthExchange(t, f)
	active, e := f.repo.Introspect(f.ctx, f.app.Slug, f.secret, tokens.AccessToken)
	if e != nil || active["active"] != true || active["sub"] != f.subject || active["client_id"] != f.app.Slug {
		t.Fatal(active, e)
	}
	if _, e = f.repo.Introspect(f.ctx, f.app.Slug, "", tokens.AccessToken); !errors.Is(e, oauth.ErrClient) {
		t.Fatal("anonymous introspection", e)
	}
	for _, raw := range []string{f.userToken, "invalid", tokens.IDToken} {
		inactive, e := f.repo.Introspect(f.ctx, f.app.Slug, f.secret, raw)
		if e != nil || len(inactive) != 1 || inactive["active"] != false {
			t.Fatal(inactive, e)
		}
	}
	f.clock.Add(tokens.ExpiresIn + 1)
	inactive, e := f.repo.Introspect(f.ctx, f.app.Slug, f.secret, tokens.AccessToken)
	if e != nil || len(inactive) != 1 || inactive["active"] != false {
		t.Fatal("access expiry used session TTL", inactive, e)
	}
}
func TestPGOAuthRegistrationOwnershipAndResourceHostScopes(t *testing.T) {
	f := newOAuthFixture(t, true)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	host, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keyDir, f.clock), SessionService: f.issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, OAuthEnabled: true, OAuthConsentURL: "https://synthetic-consent.invalid/authorize"})
	if e != nil {
		t.Fatal(e)
	}
	if e = host.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	req := oauth.Registration{Developer: f.dev, ProjectID: f.app.ProjectID, ClientName: "Registered device", RedirectURIs: []string{"https://synthetic-client.invalid/callback"}, GrantTypes: []string{"authorization_code", oauth.DeviceGrant}, ResponseTypes: []string{"code"}, Scope: "openid profile.read posts.view", TokenEndpointAuthMethod: "client_secret_basic"}
	raw, _ := json.Marshal(req)
	call := func(token string, status int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/auth/open/register", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		host.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("register %d %s", w.Code, w.Body.String())
		}
		return w
	}
	call("", 401)
	call(f.userToken, 403)
	registration := backendDecode[oauth.RegistrationResult](t, call(f.ownerToken, 201))
	if registration.ClientSecret == nil || registration.ClientID == "" {
		t.Fatal(registration)
	}
	request, verifier := oauthRequest(f)
	request.ClientID = registration.ClientID
	request.Scope = req.Scope
	if _, e = f.repo.Preview(f.ctx, f.subject, request); e != nil {
		t.Fatal(e)
	}
	decision, e := f.repo.Decide(f.ctx, f.subject, request, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(decision.RedirectURI)
	tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: registration.ClientID, ClientSecret: *registration.ClientSecret, Code: u.Query().Get("code"), CodeVerifier: verifier, RedirectURI: request.RedirectURI})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, host, "GET", "/api/accounts/me", tokens.AccessToken, "", 200)
	backendCall(t, host, "GET", "/api/posts", tokens.AccessToken, "", 200)
	backendCall(t, host, "POST", "/api/posts", tokens.AccessToken, `{"content":"cannot create with read scope"}`, 403)
	backendCall(t, host, "GET", "/api/admin/accounts", tokens.AccessToken, "", 401)
	backendCall(t, host, "DELETE", "/api/accounts/me", tokens.AccessToken, "", 401)
	if e = f.repo.RevokeConsent(f.ctx, f.subject, registration.ClientID); e != nil {
		t.Fatal(e)
	}
	backendCall(t, host, "GET", "/api/accounts/me", tokens.AccessToken, "", 401)
}

func oauthDeviceBlocker(t *testing.T, f oauthFixture, raw string) (*sql.Tx, int) {
	t.Helper()
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid int
	if e = tx.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	var found []byte
	if e = tx.QueryRowContext(f.ctx, `SELECT hash FROM owned_oauth_devices WHERE hash=$1 FOR UPDATE`, oauthDeviceHash(raw)).Scan(&found); e != nil {
		t.Fatal(e)
	}
	return tx, pid
}

func TestPGOAuthDeviceExpiryAndPreviewAgeAfterLockWait(t *testing.T) {
	for _, scenario := range []string{"preview_expiry", "decision_expiry", "preview_age"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOAuthFixture(t, false)
			enableDevice(t, &f)
			d, e := f.repo.DeviceAuthorize(f.ctx, f.app.Slug, "", "openid profile")
			if e != nil {
				t.Fatal(e)
			}
			if scenario != "preview_expiry" {
				if _, e = f.repo.DevicePreview(f.ctx, f.subject, d.UserCode); e != nil {
					t.Fatal(e)
				}
			}
			var expires time.Time
			if scenario == "preview_age" {
				e = f.db.QueryRowContext(f.ctx, `UPDATE owned_oauth_devices SET previewed_at=clock_timestamp()-interval '5 minutes'+interval '2 seconds' WHERE hash=$1 RETURNING previewed_at+interval '5 minutes'`, oauthDeviceHash(d.DeviceCode)).Scan(&expires)
			} else {
				e = f.db.QueryRowContext(f.ctx, `UPDATE owned_oauth_devices SET expires_at=clock_timestamp()+interval '2 seconds' WHERE hash=$1 RETURNING expires_at`, oauthDeviceHash(d.DeviceCode)).Scan(&expires)
			}
			if e != nil {
				t.Fatal(e)
			}
			blocker, pid := oauthDeviceBlocker(t, f, d.DeviceCode)
			done := make(chan error, 1)
			go func() {
				if scenario == "preview_expiry" {
					_, e := f.repo.DevicePreview(f.ctx, f.subject, d.UserCode)
					done <- e
				} else {
					done <- f.repo.DeviceDecide(f.ctx, f.subject, d.UserCode, true)
				}
			}()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "owned_oauth_devices")
			organizationWaitExpired(t, f.ctx, f.db, expires)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, oauth.ErrGrant) {
				t.Fatal("expired device/preview committed after a real row wait", scenario, e)
			}
			var decisions, consents int
			if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_oauth_devices WHERE hash=$1 AND decision IS NOT NULL`, oauthDeviceHash(d.DeviceCode)).Scan(&decisions); e != nil {
				t.Fatal(e)
			}
			if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_app_consents WHERE account_id=$1 AND app_id=$2`, f.subject, f.app.ID).Scan(&consents); e != nil || decisions != 0 || consents != 0 {
				t.Fatal("expired approval persisted consent", decisions, consents, e)
			}
			if scenario == "preview_expiry" {
				var n int
				if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_oauth_devices WHERE hash=$1 AND previewed_at IS NOT NULL`, oauthDeviceHash(d.DeviceCode)).Scan(&n); e != nil || n != 0 {
					t.Fatal("expired preview persisted", n, e)
				}
			}
		})
	}
}

func TestPGOAuthDevicePollRechecksClientAfterLockWait(t *testing.T) {
	for _, scenario := range []string{"secret_expiry", "secret_revoked", "app_epoch"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOAuthFixture(t, true)
			enableDevice(t, &f)
			d, e := f.repo.DeviceAuthorize(f.ctx, f.app.Slug, f.secret, "openid profile")
			if e != nil {
				t.Fatal(e)
			}
			var blocker *sql.Tx
			var pid int
			var expires time.Time
			if scenario == "secret_expiry" {
				e = f.db.QueryRowContext(f.ctx, `UPDATE owned_app_secrets SET expires_at=clock_timestamp()+interval '2 seconds' WHERE app_id=$1 AND revoked_at IS NULL RETURNING expires_at`, f.app.ID).Scan(&expires)
				if e != nil {
					t.Fatal(e)
				}
				blocker, pid = oauthDeviceBlocker(t, f, d.DeviceCode)
			} else {
				blocker, pid = atomicBlocker(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, "owned_authority_accounts", f.actor)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: oauth.DeviceGrant, ClientID: f.app.Slug, ClientSecret: f.secret, DeviceCode: d.DeviceCode})
				done <- e
			}()
			if scenario == "secret_expiry" {
				waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "owned_oauth_devices")
				organizationWaitExpired(t, f.ctx, f.db, expires)
			} else {
				waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "owned_authority_accounts")
				if scenario == "secret_revoked" {
					_, e = blocker.ExecContext(f.ctx, `UPDATE owned_app_secrets SET revoked_at=clock_timestamp() WHERE app_id=$1`, f.app.ID)
				} else {
					_, e = blocker.ExecContext(f.ctx, `UPDATE owned_custom_apps SET oauth_epoch=oauth_epoch+1 WHERE id=$1`, f.app.ID)
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, oauth.ErrClient) {
				t.Fatal("stale client disclosed poll state", scenario, e)
			}
			var n int
			if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_oauth_devices WHERE hash=$1 AND last_polled_at IS NULL AND interval_seconds=5 AND session_id IS NULL`, oauthDeviceHash(d.DeviceCode)).Scan(&n); e != nil || n != 1 {
				t.Fatal("stale client changed durable poll state", scenario, n, e)
			}
		})
	}
}

func TestPGOAuthResourceScopesCreatePostAndChatWithOriginalActor(t *testing.T) {
	f := newOAuthFixture(t, true)
	c := *f.app.OAuthConfig
	c.AllowedScopes = append(c.AllowedScopes, "posts.view", "posts.create", "chat.view", "chat.messages.create")
	f.app = backendDecode[apps.App](t, backendCall(t, f.handler, "PATCH", "/api/private/apps/"+f.app.ID+f.query, f.token, devJSON(t, apps.AppPatch{OAuthConfig: &c}), 200))
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	host, e := application.NewPostgres(f.db, nil, application.Config{Authority: authorityConfig(t, f.keyDir, f.clock), SessionService: f.issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, OAuthEnabled: true, OAuthConsentURL: "https://synthetic-consent.invalid/authorize"})
	if e != nil {
		t.Fatal(e)
	}
	room := backendDecode[rooms.Room](t, backendCall(t, host, "POST", "/api/chat", f.ownerToken, `{"name":"Delegated original actor room"}`, 200))
	p, e := f.verifier.Authenticate(f.ctx, f.ownerToken)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.transactionGuard.Bind(f.ctx, p)
	f.subject = f.actor
	req, verifier := oauthRequest(f)
	req.Scope = "openid posts.view posts.create chat.view chat.messages.create"
	if _, e = f.repo.Preview(f.ctx, f.subject, req); e != nil {
		t.Fatal(e)
	}
	decision, e := f.repo.Decide(f.ctx, f.subject, req, true)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(decision.RedirectURI)
	tokens, e := f.repo.Token(f.ctx, oauth.TokenRequest{GrantType: "authorization_code", ClientID: f.app.Slug, ClientSecret: f.secret, Code: u.Query().Get("code"), CodeVerifier: verifier, RedirectURI: req.RedirectURI})
	if e != nil {
		t.Fatal(e)
	}
	post := backendDecode[posts.Post](t, backendCall(t, host, "POST", "/api/posts?pub="+f.app.PublisherID, tokens.AccessToken, `{"content":"Explicit delegated post scope","visibility":0}`, 200))
	message := backendDecode[struct {
		ID string `json:"id"`
	}](t, backendCall(t, host, "POST", "/api/chat/"+room.ID+"/messages", tokens.AccessToken, `{"content":"Explicit delegated chat write scope"}`, 200))
	var n int
	if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_text_posts WHERE id=$1 AND publisher_id=$2 AND content='Explicit delegated post scope'`, post.ID, f.app.PublisherID).Scan(&n); e != nil || n != 1 {
		t.Fatal("delegated post was not persisted under original publisher", n, e)
	}
	if e = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM chat_messages m JOIN chat_members sender ON sender.id=m.sender_id WHERE m.id=$1 AND m.chat_room_id=$2 AND sender.account_id=$3`, message.ID, room.ID, f.actor).Scan(&n); e != nil || n != 1 {
		t.Fatal("delegated chat was not persisted under original actor", n, e)
	}
	backendCall(t, host, "POST", "/api/chat", tokens.AccessToken, `{"name":"Cannot promote message scope to room administration"}`, 401)
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, p.Session.ID)
	backendCall(t, host, "POST", "/api/chat/"+room.ID+"/messages", tokens.AccessToken, `{"content":"Revoked parent cannot write"}`, 401)
}
