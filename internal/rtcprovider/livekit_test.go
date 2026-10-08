// SPDX-License-Identifier: AGPL-3.0-only
package rtcprovider

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"solsynth.local/go-migration/ports/messager/calls"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, rt http.RoundTripper) *LiveKit {
	t.Helper()
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		t.Fatal(e)
	}
	p, e := New(Config{Endpoint: "wss://synthetic.invalid", APIKey: "synthetic-key", APISecret: secret, CloudRevocation: true, Timeout: time.Second}, rt)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func claims(t *testing.T, p *LiveKit, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("not a JWT")
	}
	h := hmac.New(sha256.New, p.cfg.APISecret)
	h.Write([]byte(parts[0] + "." + parts[1]))
	sig, e := base64.RawURLEncoding.DecodeString(parts[2])
	if e != nil || !hmac.Equal(sig, h.Sum(nil)) {
		t.Fatal("bad signature")
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		t.Fatal(e)
	}
	var c map[string]any
	if e = json.Unmarshal(data, &c); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestLiveKitTokenHasBoundedRoomIdentityGrants(t *testing.T) {
	p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("token mint must not use network")
		return nil, nil
	}))
	now := time.Now().UTC().Truncate(time.Second)
	g := calls.Grant{Room: "synthetic-room", Identity: "synthetic-identity", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	token, e := p.Token(context.Background(), g)
	if e != nil {
		t.Fatal(e)
	}
	c := claims(t, p, token)
	v := c["video"].(map[string]any)
	if c["iss"] != p.cfg.APIKey || c["sub"] != g.Identity || c["nbf"] != float64(now.Unix()) || c["exp"] != float64(g.ExpiresAt.Unix()) || v["room"] != g.Room || v["roomJoin"] != true || v["roomAdmin"] != false || v["canUpdateOwnMetadata"] != false {
		t.Fatal("incorrect grant", c)
	}
	for _, forbidden := range []string{"roomCreate", "roomList", "roomRecord", "ingressAdmin"} {
		if _, ok := v[forbidden]; ok {
			t.Fatal("excessive grant", forbidden)
		}
	}
	for _, bad := range []calls.Grant{{Room: g.Room, Identity: g.Identity, IssuedAt: now, ExpiresAt: now.Add(6 * time.Minute)}, {Room: g.Room, Identity: g.Identity, IssuedAt: now, ExpiresAt: now}, {Room: g.Room, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}} {
		if _, e = p.Token(context.Background(), bad); !errors.Is(e, calls.ErrInvalid) {
			t.Fatal(e)
		}
	}
}
func TestLiveKitTwirpContractAndExplicitRevocation(t *testing.T) {
	var p *LiveKit
	var seen []string
	p = fixture(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.URL.Scheme != "https" || r.URL.Host != "synthetic.invalid" {
			t.Fatal("incorrect transport", r.URL)
		}
		method := strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/")
		seen = append(seen, method)
		c := claims(t, p, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		v := c["video"].(map[string]any)
		if c["exp"].(float64)-c["nbf"].(float64) > 60 {
			t.Fatal("management token too long")
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Fatal(e)
		}
		if method == "CreateRoom" || method == "DeleteRoom" {
			if v["roomCreate"] != true || v["roomAdmin"] != nil {
				t.Fatal("incorrect management grant", v)
			}
		} else if v["roomAdmin"] != true || v["room"] != "synthetic-room" {
			t.Fatal("unscoped admin", v)
		}
		switch method {
		case "CreateRoom":
			if body["name"] != "synthetic-room" || body["egress"] != nil || body["agents"] != nil {
				t.Fatal(body)
			}
			return response(200, `{"name":"synthetic-room"}`), nil
		case "ListParticipants":
			return response(200, `{"participants":[{"identity":"opaque-id","name":"","joined_at":"1770000000","tracks":[{"sid":"audio-1","type":"AUDIO"}]}]}`), nil
		case "RemoveParticipant":
			if body["revoke_token_ts"] == nil || body["identity"] != "opaque-id" {
				t.Fatal(body)
			}
		case "MutePublishedTrack":
			if body["track_sid"] != "audio-1" || body["muted"] != true {
				t.Fatal(body)
			}
		}
		return response(200, `{}`), nil
	}))
	ctx := context.Background()
	if e := p.EnsureRoom(ctx, "synthetic-room"); e != nil {
		t.Fatal(e)
	}
	items, e := p.Participants(ctx, "synthetic-room")
	if e != nil || len(items) != 1 || items[0].TrackSID != "audio-1" || items[0].AccountID != "" {
		t.Fatal(items, e)
	}
	if e = p.Mute(ctx, "synthetic-room", "opaque-id", "audio-1", true); e != nil {
		t.Fatal(e)
	}
	if e = p.Remove(ctx, "synthetic-room", "opaque-id", time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = p.DeleteRoom(ctx, "synthetic-room"); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 5 {
		t.Fatal(seen)
	}
}
func TestLiveKitMissingStrictConfigurationFailsClosed(t *testing.T) {
	secret := make([]byte, 32)
	rand.Read(secret)
	base := Config{Endpoint: "wss://synthetic.invalid", APIKey: "synthetic-key", APISecret: secret, CloudRevocation: true, Timeout: time.Second}
	for _, change := range []func(*Config){func(c *Config) { c.Endpoint = "https://synthetic.invalid" }, func(c *Config) { c.Endpoint = "wss://user:secret@synthetic.invalid" }, func(c *Config) { c.Endpoint = "wss://synthetic.invalid/?token=x" }, func(c *Config) { c.APIKey = "" }, func(c *Config) { c.APISecret = nil }, func(c *Config) { c.CloudRevocation = false }, func(c *Config) { c.Timeout = 0 }} {
		c := base
		change(&c)
		if _, e := New(c, nil); !errors.Is(e, calls.ErrUnconfigured) {
			t.Fatal("accepted incomplete config", e)
		}
	}
}
func TestLiveKitProviderFailureDoesNotClaimRemoval(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{404, `{"code":"not_found"}`}, {403, `{"message":"sensitive provider diagnostics"}`}, {502, `bad gateway`}} {
		p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }))
		if e := p.Remove(context.Background(), "room", "identity", time.Now()); !errors.Is(e, calls.ErrProvider) || strings.Contains(e.Error(), "sensitive") {
			t.Fatal(e)
		}
	}
	p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(404, `{"code":"not_found"}`), nil }))
	if e := p.DeleteRoom(context.Background(), "room"); e != nil {
		t.Fatal(e)
	}
	p = fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(200, `{"name":"wrong-room"}`), nil }))
	if e := p.EnsureRoom(context.Background(), "room"); !errors.Is(e, calls.ErrProvider) {
		t.Fatal(e)
	}
	p = fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(302, `{}`), nil }))
	if e := p.Remove(context.Background(), "room", "id", time.Now()); !errors.Is(e, calls.ErrProvider) {
		t.Fatal(e)
	}
}
