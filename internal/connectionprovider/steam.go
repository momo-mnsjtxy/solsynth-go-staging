// SPDX-License-Identifier: AGPL-3.0-only
package connectionprovider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	p "solsynth.local/go-migration/ports/passport/connections"
	"strconv"
	"strings"
	"time"
)

const steamEndpoint = "https://steamcommunity.com/openid/login"
const openidNamespace = "http://specs.openid.net/auth/2.0"

type Steam struct {
	callback string
	client   *http.Client
	now      func() time.Time
}

func NewSteam(callback string, client *http.Client) (*Steam, error) {
	if !secureURL(callback) {
		return nil, p.ErrInvalid
	}
	return &Steam{callback, restrictedClient(client), time.Now}, nil
}
func (s *Steam) Name() string { return "steam" }
func (s *Steam) returnTo(state string) string {
	u, _ := url.Parse(s.callback)
	q := u.Query()
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}
func (s *Steam) Start(state, _ string) (string, error) {
	u, _ := url.Parse(steamEndpoint)
	callback, _ := url.Parse(s.callback)
	realm := callback.Scheme + "://" + callback.Host + "/"
	u.RawQuery = url.Values{"openid.ns": {openidNamespace}, "openid.mode": {"checkid_setup"}, "openid.return_to": {s.returnTo(state)}, "openid.realm": {realm}, "openid.identity": {"http://specs.openid.net/auth/2.0/identifier_select"}, "openid.claimed_id": {"http://specs.openid.net/auth/2.0/identifier_select"}}.Encode()
	return u.String(), nil
}
func (s *Steam) Verify(ctx context.Context, v p.Completion, _ string) (out p.Verified, e error) {
	m := v.OpenID
	if v.Code != "" || m["openid.ns"] != openidNamespace || m["openid.mode"] != "id_res" || m["openid.op_endpoint"] != steamEndpoint || m["openid.return_to"] != s.returnTo(v.State) || m["openid.identity"] != m["openid.claimed_id"] {
		return out, p.ErrProof
	}
	claimed := m["openid.claimed_id"]
	prefix := "https://steamcommunity.com/openid/id/"
	if !strings.HasPrefix(claimed, prefix) {
		return out, p.ErrProof
	}
	id := strings.TrimPrefix(claimed, prefix)
	if len(id) != 17 {
		return out, p.ErrProof
	}
	if _, e = strconv.ParseUint(id, 10, 64); e != nil {
		return out, p.ErrProof
	}
	signed := map[string]bool{}
	for _, key := range strings.Split(m["openid.signed"], ",") {
		signed[key] = true
	}
	for _, key := range []string{"op_endpoint", "claimed_id", "identity", "return_to", "response_nonce", "assoc_handle"} {
		if !signed[key] || m["openid."+key] == "" {
			return out, p.ErrProof
		}
	}
	nonce := m["openid.response_nonce"]
	if len(nonce) < 21 || len(nonce) > 256 {
		return out, p.ErrProof
	}
	stamp, e := time.Parse(time.RFC3339, nonce[:20])
	if e != nil || stamp.After(s.now().Add(time.Minute)) || stamp.Before(s.now().Add(-10*time.Minute)) {
		return out, p.ErrProof
	}
	data := url.Values{}
	for k, value := range m {
		if !strings.HasPrefix(k, "openid.") || len(k) > 128 || len(value) > 4096 {
			return out, p.ErrProof
		}
		data.Set(k, value)
	}
	data.Set("openid.mode", "check_authentication")
	req, e := http.NewRequestWithContext(ctx, "POST", steamEndpoint, strings.NewReader(data.Encode()))
	if e != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := s.client.Do(req)
	if e != nil {
		return out, p.ErrUnavailable
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if e != nil || len(body) > 8192 || resp.StatusCode != 200 {
		return out, p.ErrProof
	}
	valid := false
	for _, line := range strings.Split(string(body), "\n") {
		if line == "is_valid:true" {
			valid = true
		}
	}
	if !valid {
		return out, p.ErrProof
	}
	return p.Verified{Identifier: id}, nil
}
