// SPDX-License-Identifier: AGPL-3.0-only
package connectionprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	p "solsynth.local/go-migration/ports/passport/connections"
	"strings"
	"time"
)

type OAuthConfig struct {
	Provider         string   `json:"provider"`
	AuthorizationURL string   `json:"authorization_url"`
	TokenURL         string   `json:"token_url"`
	UserinfoURL      string   `json:"userinfo_url"`
	ClientID         string   `json:"client_id"`
	ClientSecret     string   `json:"-"`
	CallbackURL      string   `json:"callback_url"`
	Scopes           []string `json:"scopes"`
}
type OAuth struct {
	cfg    OAuthConfig
	client *http.Client
}

func secureURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}
func restrictedClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	v := *c
	v.Timeout = 10 * time.Second
	v.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &v
}
func NewOAuth(c OAuthConfig, client *http.Client) (*OAuth, error) {
	if !p.ValidProvider(c.Provider) || !secureURL(c.AuthorizationURL) || !secureURL(c.TokenURL) || !secureURL(c.UserinfoURL) || !secureURL(c.CallbackURL) || c.ClientID == "" || c.ClientSecret == "" || len(c.ClientID) > 2048 || len(c.ClientSecret) > 4096 || len(c.Scopes) > 32 {
		return nil, p.ErrInvalid
	}
	for _, s := range c.Scopes {
		if s == "" || len(s) > 256 || strings.ContainsAny(s, " \x00\r\n\t") {
			return nil, p.ErrInvalid
		}
	}
	c.Scopes = append([]string{}, c.Scopes...)
	return &OAuth{c, restrictedClient(client)}, nil
}
func (o *OAuth) Name() string { return o.cfg.Provider }
func (o *OAuth) Start(state, challenge string) (string, error) {
	u, _ := url.Parse(o.cfg.AuthorizationURL)
	q := url.Values{"response_type": {"code"}, "client_id": {o.cfg.ClientID}, "redirect_uri": {o.cfg.CallbackURL}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if len(o.cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(o.cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func readJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return p.ErrProof
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 128<<10+1))
	if e != nil || len(b) > 128<<10 {
		return p.ErrProof
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	if d.Decode(v) != nil {
		return p.ErrProof
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return p.ErrProof
	}
	return nil
}
func (o *OAuth) Verify(ctx context.Context, v p.Completion, verifier string) (out p.Verified, e error) {
	if v.Code == "" || len(v.OpenID) != 0 {
		return out, p.ErrProof
	}
	data := url.Values{"grant_type": {"authorization_code"}, "code": {v.Code}, "redirect_uri": {o.cfg.CallbackURL}, "client_id": {o.cfg.ClientID}, "client_secret": {o.cfg.ClientSecret}, "code_verifier": {verifier}}
	req, e := http.NewRequestWithContext(ctx, "POST", o.cfg.TokenURL, strings.NewReader(data.Encode()))
	if e != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, e := o.client.Do(req)
	if e != nil {
		return out, p.ErrUnavailable
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if e = readJSON(resp, &token); e != nil {
		return
	}
	if token.AccessToken == "" || len(token.AccessToken) > 16384 || strings.ContainsAny(token.AccessToken, "\x00\r\n") || !strings.EqualFold(token.TokenType, "bearer") {
		return out, p.ErrProof
	}
	req, e = http.NewRequestWithContext(ctx, "GET", o.cfg.UserinfoURL, nil)
	if e != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, e = o.client.Do(req)
	if e != nil {
		return out, p.ErrUnavailable
	}
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Username      string `json:"preferred_username"`
		Name          string `json:"name"`
	}
	if e = readJSON(resp, &info); e != nil {
		return
	}
	out = p.Verified{Identifier: info.Sub, Username: info.Username, DisplayName: info.Name}
	if info.EmailVerified {
		out.Email = info.Email
	}
	e = p.ValidateVerified(out)
	return
}
