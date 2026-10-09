// SPDX-License-Identifier: AGPL-3.0-only
package connectionprovider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	p "solsynth.local/go-migration/ports/passport/connections"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestOAuthExchangeUsesPKCEAndVerifiedUserinfo(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/token":
			if r.Method != "POST" {
				t.Fatal(r.Method)
			}
			r.ParseForm()
			if r.PostForm.Get("code_verifier") != "verifier" || r.PostForm.Get("client_secret") != "server-secret" {
				t.Fatal("missing private exchange")
			}
			return response(`{"access_token":"private-access","token_type":"Bearer"}`), nil
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer private-access" {
				t.Fatal("missing token")
			}
			return response(`{"sub":"verified-identity","email":"unverified@example.invalid","email_verified":false,"name":"Verified display"}`), nil
		default:
			t.Fatal("unexpected endpoint", r.URL)
			return nil, nil
		}
	})}
	o, e := NewOAuth(OAuthConfig{Provider: "example", AuthorizationURL: "https://provider.example/auth", TokenURL: "https://provider.example/token", UserinfoURL: "https://provider.example/userinfo", CallbackURL: "https://app.example/callback", ClientID: "client", ClientSecret: "server-secret"}, client)
	if e != nil {
		t.Fatal(e)
	}
	started, e := o.Start("state", "challenge")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(started)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("state") != "state" {
		t.Fatal(started)
	}
	v, e := o.Verify(context.Background(), p.Completion{Code: "server-issued-code"}, "verifier")
	if e != nil || v.Identifier != "verified-identity" || v.Email != "" || calls != 2 {
		t.Fatal(v, e, calls)
	}
}
func TestSteamChecksSignedReturnAndExactProvider(t *testing.T) {
	now := time.Now().UTC()
	calls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != steamEndpoint {
			t.Fatal("arbitrary endpoint")
		}
		r.ParseForm()
		if r.PostForm.Get("openid.mode") != "check_authentication" {
			t.Fatal("not verified")
		}
		return response("ns:" + openidNamespace + "\nis_valid:true\n"), nil
	})}
	s, e := NewSteam("https://app.example/callback", client)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return now }
	m := map[string]string{"openid.ns": openidNamespace, "openid.mode": "id_res", "openid.op_endpoint": steamEndpoint, "openid.claimed_id": "https://steamcommunity.com/openid/id/76561198000000000", "openid.identity": "https://steamcommunity.com/openid/id/76561198000000000", "openid.return_to": s.returnTo("state"), "openid.response_nonce": now.Format(time.RFC3339) + "nonce", "openid.assoc_handle": "private", "openid.signed": "op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle", "openid.sig": "signature"}
	v, e := s.Verify(context.Background(), p.Completion{State: "state", OpenID: m}, "")
	if e != nil || v.Identifier != "76561198000000000" || calls != 1 {
		t.Fatal(v, e)
	}
	m["openid.return_to"] = "https://attacker.example/"
	if _, e = s.Verify(context.Background(), p.Completion{State: "state", OpenID: m}, ""); e == nil || calls != 1 {
		t.Fatal("forged return reached provider")
	}
}
func TestSteamPresenceIdentityCannotBeReplaced(t *testing.T) {
	s, e := NewSteamPresence("0123456789ABCDEF", &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.steampowered.com" || r.URL.Query().Get("steamids") != "76561198000000000" {
			t.Fatal("unbounded target")
		}
		return response(`{"response":{"players":[{"steamid":"76561198000000001","gameid":"730","gameextrainfo":"Synthetic game"}]}}`), nil
	})})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Observe(context.Background(), "76561198000000000"); e == nil {
		t.Fatal("provider changed subject")
	}
}

func TestLastFMLinkingUsesSignedTokenAndPresenceCanonicalSubject(t *testing.T) {
	key, secret := strings.Repeat("a", 32), strings.Repeat("b", 32)
	calls := 0
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "ws.audioscrobbler.com" || r.URL.Path != "/2.0/" {
			t.Fatal("arbitrary provider host")
		}
		if r.Method == "POST" {
			r.ParseForm()
			if r.PostForm.Get("api_sig") == "" || r.PostForm.Get("token") != strings.Repeat("c", 32) {
				t.Fatal("unsigned exchange")
			}
			return response(`{"session":{"name":"Verified_User","key":"dddddddddddddddddddddddddddddddd"}}`), nil
		}
		if r.URL.Query().Get("user") != "verified_user" || r.URL.Query().Get("limit") != "1" {
			t.Fatal("unbounded/wrong subject")
		}
		return response(`{"recenttracks":{"@attr":{"user":"verified_user"},"track":[{"name":"Track","artist":{"#text":"Artist"},"@attr":{"nowplaying":"true"}}]}}`), nil
	})}
	link, e := NewLastFM(key, secret, "https://app.example/callback", client)
	if e != nil {
		t.Fatal(e)
	}
	start, e := link.Start("original-state", "challenge")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(start)
	cb, _ := url.Parse(u.Query().Get("cb"))
	if cb.Query().Get("state") != "original-state" {
		t.Fatal("missing CSRF binding")
	}
	v, e := link.Verify(context.Background(), p.Completion{Code: strings.Repeat("c", 32)}, "verifier")
	if e != nil || v.Identifier != "verified_user" || v.Email != "" {
		t.Fatal(v, e)
	}
	observer, e := NewLastFMPresence(key, client)
	if e != nil {
		t.Fatal(e)
	}
	content, active, e := observer.Observe(context.Background(), v.Identifier)
	if e != nil || !active || *content.Type != "music" || *content.Title != "Track" || calls != 2 {
		t.Fatal(content, active, e, calls)
	}
	if _, _, e = observer.Observe(context.Background(), "attacker?api_key=other"); e == nil || calls != 2 {
		t.Fatal("unverified input escaped canonical identifier", e, calls)
	}
}
