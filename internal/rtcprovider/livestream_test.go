// SPDX-License-Identifier: AGPL-3.0-only
package rtcprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/sphere/livestreams"
	"strings"
	"testing"
	"time"
)

func TestLiveStreamTokenRestrictsViewerAndPublisherGrants(t *testing.T) {
	p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider request"); return nil, nil }))
	now := time.Now().UTC().Truncate(time.Second)
	for _, publish := range []bool{false, true} {
		token, e := p.StreamToken(context.Background(), livestreams.Grant{Room: "synthetic-stream", Identity: "opaque-synthetic-viewer", Publish: publish, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
		if e != nil {
			t.Fatal(e)
		}
		c := claims(t, p, token)
		v := c["video"].(map[string]any)
		if v["canPublish"] != publish || v["canSubscribe"] != true || v["canPublishData"] != false || v["roomAdmin"] != false || v["room"] != "synthetic-stream" || v["ingressAdmin"] != nil || v["roomRecord"] != nil || v["roomCreate"] != nil {
			t.Fatal(c)
		}
	}
}
func TestLiveStreamIngressTransportAndOwnershipBinding(t *testing.T) {
	var p *LiveKit
	seen := []string{}
	p = fixture(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "synthetic.invalid" || r.URL.Scheme != "https" || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal(r)
		}
		c := claims(t, p, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		v := c["video"].(map[string]any)
		if len(v) != 1 || v["ingressAdmin"] != true {
			t.Fatal(c)
		}
		var b map[string]any
		if e := json.NewDecoder(r.Body).Decode(&b); e != nil {
			t.Fatal(e)
		}
		method := strings.TrimPrefix(r.URL.Path, "/twirp/livekit.IngressService/")
		seen = append(seen, method)
		raw := `{"ingress_id":"synthetic-ingress","room_name":"synthetic-room","participant_identity":"synthetic-source","name":"synthetic-intent","input_type":"WHIP_INPUT","url":"https://synthetic.invalid/whip","stream_key":"synthetic-only"}`
		switch method {
		case "CreateIngress":
			if b["input_type"] != float64(1) || b["enable_transcoding"] != false || b["url"] != nil || b["room_name"] != "synthetic-room" {
				t.Fatal(b)
			}
			return response(200, raw), nil
		case "ListIngress":
			if b["room_name"] != "synthetic-room" {
				t.Fatal(b)
			}
			return response(200, `{"items":[`+raw+`]}`), nil
		case "DeleteIngress":
			if b["ingress_id"] != "synthetic-ingress" {
				t.Fatal(b)
			}
			return response(404, `{"code":"not_found"}`), nil
		}
		t.Fatal(method)
		return nil, nil
	}))
	c := context.Background()
	v, e := p.CreateIngress(c, livestreams.IngressRequest{Room: "synthetic-room", Identity: "synthetic-source", Name: "synthetic-intent", WHIP: true})
	if e != nil || v.ID != "synthetic-ingress" {
		t.Fatal(v, e)
	}
	rows, e := p.ListIngress(c, "synthetic-room")
	if e != nil || len(rows) != 1 {
		t.Fatal(e)
	}
	if e = p.DeleteIngress(c, v.ID); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
}
func TestLiveStreamIngressRejectsAmbiguousAndUnsafeResponses(t *testing.T) {
	for _, raw := range []string{`null`, `{"items":[{"ingress_id":"unexpected"}]}`, `{"items":[],"next_page_token":{"token":"more"}}`} {
		p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(200, raw), nil }))
		if _, e := p.ListIngress(context.Background(), "synthetic-room"); !errors.Is(e, livestreams.ErrProvider) {
			t.Fatal(raw, e)
		}
	}
	p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(403, `{"message":"sensitive upstream diagnostics"}`), nil
	}))
	if e := p.DeleteIngress(context.Background(), "synthetic-ingress"); !errors.Is(e, livestreams.ErrProvider) || strings.Contains(e.Error(), "sensitive") {
		t.Fatal(e)
	}
	if _, e := p.ListIngress(context.Background(), ""); !errors.Is(e, livestreams.ErrInvalid) {
		t.Fatal(e)
	}
}

func TestLiveStreamMissingRoomRequiresAcknowledgedNotFound(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		empty  bool
	}{{404, `{"code":"not_found"}`, true}, {404, `{}`, false}, {403, `{"code":"permission_denied"}`, false}, {503, `{"code":"unavailable"}`, false}} {
		p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }))
		participants, e := p.StreamParticipants(context.Background(), "synthetic-room")
		if tc.empty {
			if e != nil || len(participants) != 0 {
				t.Fatal(participants, e)
			}
		} else if !errors.Is(e, livestreams.ErrProvider) {
			t.Fatal(e)
		}
	}
}

func TestLiveStreamIngressJSONFieldVariantsRemainBound(t *testing.T) {
	for _, raw := range []string{`{"ingressId":"synthetic-id","roomName":"synthetic-room","participantIdentity":"synthetic-source","name":"synthetic-intent","inputType":"WHIP_INPUT","url":"https://synthetic.invalid/whip","streamKey":"synthetic-only"}`, `{"ingress_id":"synthetic-id","room_name":"synthetic-room","participant_identity":"synthetic-source","name":"synthetic-intent","input_type":1,"url":"https://synthetic.invalid/whip","stream_key":"synthetic-only"}`} {
		p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(200, raw), nil }))
		v, e := p.CreateIngress(context.Background(), livestreams.IngressRequest{Room: "synthetic-room", Identity: "synthetic-source", Name: "synthetic-intent", WHIP: true})
		if e != nil || v.ID != "synthetic-id" || v.Key != "synthetic-only" {
			t.Fatal(v, e)
		}
	}
	p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) {
		return response(200, `{"items":[],"nextPageToken":{"token":"more"}}`), nil
	}))
	if _, e := p.ListIngress(context.Background(), "synthetic-room"); !errors.Is(e, livestreams.ErrProvider) {
		t.Fatal("partial list accepted", e)
	}
}
