// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored against official LiveKit RoomService and grants docs.
package rtcprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/messager/calls"
	"strings"
	"time"
)

type Config struct {
	Endpoint  string
	APIKey    string
	APISecret []byte
	// CloudRevocation explicitly asserts that this deployment supports the Cloud
	// RemoveParticipant revoke_token_ts contract. Self-hosted is not equivalent.
	CloudRevocation bool
	Timeout         time.Duration
}
type LiveKit struct {
	cfg    Config
	api    string
	client *http.Client
}

var _ calls.Provider = (*LiveKit)(nil)

func New(c Config, transport http.RoundTripper) (*LiveKit, error) {
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(c.APIKey, "\r\n") || len(c.APIKey) < 1 || len(c.APISecret) < 32 || !c.CloudRevocation || c.Timeout <= 0 || c.Timeout > 10*time.Second {
		return nil, calls.ErrUnconfigured
	}
	c.APISecret = append([]byte(nil), c.APISecret...)
	u.Scheme = "https"
	u.Path = ""
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &LiveKit{c, u.String(), &http.Client{Transport: transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *LiveKit) Name() string           { return "livekit" }
func (p *LiveKit) Endpoint() string       { return p.cfg.Endpoint }
func (p *LiveKit) StrictRevocation() bool { return p.cfg.CloudRevocation }
func (p *LiveKit) sign(identity string, now, expiry time.Time, video map[string]any) (string, error) {
	claims := map[string]any{"iss": p.cfg.APIKey, "sub": identity, "iat": now.Unix(), "nbf": now.Unix(), "exp": expiry.Unix(), "video": video}
	b, e := json.Marshal(claims)
	if e != nil {
		return "", calls.ErrProvider
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	raw := header + "." + base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, p.cfg.APISecret)
	_, _ = h.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}
func (p *LiveKit) Token(ctx context.Context, g calls.Grant) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if g.Room == "" || g.Identity == "" || !g.ExpiresAt.After(g.IssuedAt) || g.ExpiresAt.Sub(g.IssuedAt) > 5*time.Minute {
		return "", calls.ErrInvalid
	}
	return p.sign(g.Identity, g.IssuedAt, g.ExpiresAt, map[string]any{"room": g.Room, "roomJoin": true, "canPublish": true, "canSubscribe": true, "canPublishData": true, "canUpdateOwnMetadata": false, "roomAdmin": false, "canPublishSources": []string{"camera", "microphone", "screen_share", "screen_share_audio"}})
}
func (p *LiveKit) request(ctx context.Context, method, room string, body any, out any, absentOK bool) error {
	b, e := json.Marshal(body)
	if e != nil {
		return calls.ErrProvider
	}
	grant := map[string]any{"room": room, "roomAdmin": true}
	if method == "CreateRoom" || method == "DeleteRoom" {
		grant = map[string]any{"roomCreate": true}
	}
	now := time.Now().UTC()
	token, e := p.sign("", now, now.Add(time.Minute), grant)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", p.api+"/twirp/livekit.RoomService/"+method, bytes.NewReader(b))
	if e != nil {
		return calls.ErrProvider
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, e := p.client.Do(req)
	if e != nil {
		return calls.ErrProvider
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if e != nil || len(data) > 1<<20 {
		return calls.ErrProvider
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var fail struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &fail)
		if absentOK && resp.StatusCode == 404 && fail.Code == "not_found" {
			return nil
		}
		return calls.ErrProvider
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return calls.ErrProvider
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return calls.ErrProvider
	}
	return nil
}
func (p *LiveKit) EnsureRoom(ctx context.Context, room string) error {
	var result struct {
		Name string `json:"name"`
	}
	e := p.request(ctx, "CreateRoom", room, map[string]any{"name": room, "empty_timeout": 300, "departure_timeout": 300, "max_participants": 100}, &result, false)
	if e == nil && result.Name != room {
		return calls.ErrProvider
	}
	return e
}
func (p *LiveKit) DeleteRoom(ctx context.Context, room string) error {
	return p.request(ctx, "DeleteRoom", room, map[string]any{"room": room}, nil, true)
}
func (p *LiveKit) Remove(ctx context.Context, room, identity string, cutoff time.Time) error {
	// Never accept a generic 404: only an acknowledged Cloud cutoff confirms
	// revocation for an identity that has already disconnected.
	return p.request(ctx, "RemoveParticipant", room, map[string]any{"room": room, "identity": identity, "revoke_token_ts": cutoff.Unix()}, nil, false)
}
func (p *LiveKit) Mute(ctx context.Context, room, identity, track string, muted bool) error {
	return p.request(ctx, "MutePublishedTrack", room, map[string]any{"room": room, "identity": identity, "track_sid": track, "muted": muted}, nil, false)
}
func (p *LiveKit) Participants(ctx context.Context, room string) ([]calls.Participant, error) {
	type track struct {
		SID  string          `json:"sid"`
		Type json.RawMessage `json:"type"`
	}
	var result struct {
		Participants []struct {
			Identity string      `json:"identity"`
			Name     string      `json:"name"`
			Joined   json.Number `json:"joined_at"`
			Tracks   []track     `json:"tracks"`
		} `json:"participants"`
	}
	if e := p.request(ctx, "ListParticipants", room, map[string]any{"room": room}, &result, false); e != nil {
		return nil, e
	}
	out := make([]calls.Participant, 0, len(result.Participants))
	for _, v := range result.Participants {
		ts, e := v.Joined.Int64()
		if e != nil {
			return nil, calls.ErrProvider
		}
		item := calls.Participant{Identity: v.Identity, Name: v.Name, JoinedAt: time.Unix(ts, 0).UTC()}
		for _, tr := range v.Tracks {
			if string(tr.Type) == `"AUDIO"` || string(tr.Type) == "0" {
				item.TrackSID = tr.SID
				break
			}
		}
		out = append(out, item)
	}
	return out, nil
}
