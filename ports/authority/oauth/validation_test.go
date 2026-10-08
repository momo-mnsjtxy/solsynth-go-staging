// SPDX-License-Identifier: AGPL-3.0-only
package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"net/url"
	"solsynth.local/go-migration/ports/develop/apps"
	"strings"
	"testing"
)

func validRequest() (Request, apps.App, string) {
	v := strings.Repeat("a", 43)
	h := sha256.Sum256([]byte(v))
	r := Request{ClientID: "synthetic", RedirectURI: "https://client.invalid/callback", ResponseType: "code", Scope: "openid profile offline_access", State: strings.Repeat("s", 32), CodeChallenge: base64.RawURLEncoding.EncodeToString(h[:]), CodeChallengeMethod: "S256", Nonce: strings.Repeat("n", 32)}
	a := apps.App{Slug: "synthetic", OAuthConfig: &apps.OAuthConfig{RedirectURIs: []string{r.RedirectURI}, AllowedScopes: []string{"openid", "profile", "offline_access"}, AllowedGrantTypes: []string{"authorization_code", "refresh_token"}, RequirePKCE: true, AllowOfflineAccess: true, IsPublicClient: true}}
	return r, a, v
}
func TestOAuthExactRedirectPKCEAndKnownScopes(t *testing.T) {
	r, a, v := validRequest()
	if _, e := Validate(r, a); e != nil || !PKCE(v, r.CodeChallenge) {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Request){func(r *Request) { r.RedirectURI += "/" }, func(r *Request) { r.RedirectURI = "https://client.invalid.evil.test/callback" }, func(r *Request) { r.CodeChallengeMethod = "plain" }, func(r *Request) { r.CodeChallenge += "=" }, func(r *Request) { r.Scope = "openid *" }, func(r *Request) { r.Scope = "openid posts.create" }, func(r *Request) { r.State = "" }, func(r *Request) { r.Nonce = "bad\nnonce" }, func(r *Request) { r.ResponseType = "token" }} {
		copy := r
		mutate(&copy)
		if _, e := Validate(copy, a); e == nil {
			t.Fatal("invalid request accepted", copy)
		}
	}
	if PKCE(strings.Repeat("b", 43), r.CodeChallenge) || PKCE(v+"!", r.CodeChallenge) {
		t.Fatal("wrong verifier accepted")
	}
}
func TestOAuthStrictFormAndSingleClientAuthentication(t *testing.T) {
	for _, body := range []string{"client_id=one&client_id=two", "client_id=one&access_token=bad", "client_id=%ZZ"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, e := parameters(r, "client_id"); e == nil {
			t.Fatal(body)
		}
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.SetBasicAuth(url.QueryEscape("synthetic"), url.QueryEscape("opaque+secret"))
	c, s, e := credentials(r, url.Values{"client_id": {"synthetic"}})
	if e != nil || c != "synthetic" || s != "opaque+secret" {
		t.Fatal(c, s, e)
	}
	if _, _, e = credentials(r, url.Values{"client_id": {"synthetic"}, "client_secret": {"second"}}); e == nil {
		t.Fatal("mixed client auth accepted")
	}
	r.Header.Add("Authorization", "Basic another")
	if _, _, e = credentials(r, url.Values{}); e == nil {
		t.Fatal("duplicate authorization")
	}
}
