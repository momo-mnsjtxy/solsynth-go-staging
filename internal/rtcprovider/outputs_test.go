// SPDX-License-Identifier: AGPL-3.0-only
package rtcprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func outputFixture(t *testing.T, rt http.RoundTripper) *LiveKit {
	t.Helper()
	p, e := NewWithOutputs(Config{Endpoint: "wss://synthetic.invalid", APIKey: "synthetic-only", APISecret: []byte(strings.Repeat("s", 32)), CloudRevocation: true, Timeout: time.Second}, OutputPolicy{Destinations: map[string]OutputDestination{
		"synthetic-relay": {Kind: "rtmp", URL: "rtmps://relay.example.invalid/live/synthetic-key"},
		"synthetic-hls":   {Kind: "hls", Storage: &OutputStorage{PrivateNamespace: true, AccessKey: "synthetic-access", Secret: strings.Repeat("synthetic-only", 3), Bucket: "synthetic-private", Endpoint: "https://storage.example.invalid"}},
		"synthetic-file":  {Kind: "file", Storage: &OutputStorage{PrivateNamespace: true, AccessKey: "synthetic-access", Secret: strings.Repeat("synthetic-only", 3), Bucket: "synthetic-private"}},
	}}, rt)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestLiveKitOutputsUseFixedDestinationsAndRoomRecord(t *testing.T) {
	const id = "b1000000-0000-4000-8000-000000000001"
	var provider *LiveKit
	var body map[string]any
	provider = outputFixture(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://synthetic.invalid/twirp/livekit.Egress/StartEgress" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("wrong endpoint or transport")
		}
		grant := claims(t, provider, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["video"].(map[string]any)
		if len(grant) != 2 || grant["roomRecord"] != true || grant["room"] != "synthetic-room" {
			t.Fatal("expanded provider authority", grant)
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil || json.Unmarshal(raw, &body) != nil {
			t.Fatal(e)
		}
		return response(200, `{"egress_id":"EG_synthetic","room_name":"synthetic-room","status":"EGRESS_ACTIVE"}`), nil
	}))
	hash, e := provider.OutputPolicyFingerprint([]string{"synthetic-hls"}, "hls")
	if e != nil || len(hash) != 32 {
		t.Fatal(e)
	}
	result, e := provider.StartOutput(context.Background(), live.OutputStart{ID: id, Room: "synthetic-room", Kind: "hls", Destinations: []string{"synthetic-hls"}, Playlist: "playlist.m3u8", SegmentDuration: 6, Layout: "grid"})
	if e != nil || result.ID != "EG_synthetic" || result.Status != 1 {
		t.Fatal(result, e)
	}
	outputs := body["outputs"].([]any)
	o := outputs[0].(map[string]any)
	segments := o["segments"].(map[string]any)
	if len(outputs) != 1 || segments["live_playlist_name"] != "streaming/synthetic-hls/"+id+"/playlist.m3u8" || segments["filename_prefix"] != "streaming/synthetic-hls/"+id+"/segment" || body["web"] != nil {
		t.Fatal("unbounded output construction")
	}
	_, e = provider.StartOutput(context.Background(), live.OutputStart{ID: id, Room: "synthetic-room", Kind: "egress", Destinations: []string{"synthetic-file", "synthetic-relay"}, Layout: "grid"})
	if e != nil {
		t.Fatal(e)
	}
	outputs = body["outputs"].([]any)
	if len(outputs) != 2 {
		t.Fatal("missing configured outputs")
	}
	for _, bad := range []live.OutputStart{
		{ID: id, Room: "synthetic-room", Kind: "egress", Destinations: []string{"https://external.invalid"}, Layout: "grid"},
		{ID: id, Room: "synthetic-room", Kind: "hls", Destinations: []string{"synthetic-hls"}, Playlist: "../secret.m3u8", SegmentDuration: 6, Layout: "grid"},
		{ID: id, Room: "synthetic-room", Kind: "hls", Destinations: []string{"synthetic-hls"}, Playlist: "playlist.m3u8", SegmentDuration: 6, Layout: "https://external.invalid"},
	} {
		if _, e = provider.StartOutput(context.Background(), bad); e == nil {
			t.Fatal("unsafe output accepted")
		}
	}
}
func TestLiveKitOutputsRejectAmbiguousAndForeignAcknowledgements(t *testing.T) {
	const id = "b1000000-0000-4000-8000-000000000001"
	for _, body := range []string{`{}`, `{"egress_id":"","room_name":"synthetic-room","status":3}`, `{"egress_id":"EG_other","room_name":"other-room","status":1}`, `{"egress_id":"EG_synthetic","room_name":"synthetic-room","status":"UNKNOWN"}`, `null`} {
		p := outputFixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
		if _, e := p.StartOutput(context.Background(), live.OutputStart{ID: id, Room: "synthetic-room", Kind: "egress", Destinations: []string{"synthetic-relay"}, Layout: "grid"}); e == nil {
			t.Fatal("invalid start acknowledgement", body)
		}
		if out, e := p.StopOutput(context.Background(), "synthetic-room", "EG_synthetic"); e == nil || out.Absent {
			t.Fatal("invalid stop acknowledgement", body, out, e)
		}
	}
	for _, tc := range []struct {
		status int
		body   string
		absent bool
	}{{404, `{"code":"not_found"}`, true}, {404, `{"code":"permission_denied"}`, false}, {401, `{"code":"not_found"}`, false}, {502, `synthetic-sensitive-response`, false}} {
		p := outputFixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }))
		out, e := p.StopOutput(context.Background(), "synthetic-room", "EG_synthetic")
		if tc.absent {
			if e != nil || !out.Absent || !out.Terminal() {
				t.Fatal(out, e)
			}
		} else if !errors.Is(e, live.ErrProvider) {
			t.Fatal("provider error leaked", e)
		}
	}
	p := outputFixture(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"items":[{"egress_id":"EG_synthetic","room_name":"synthetic-room","status":1,"segment_results":[{"live_playlist_name":"streaming/synthetic-hls/`+id+`/playlist.m3u8"}]}]}`), nil
	}))
	items, e := p.ListOutputs(context.Background(), "synthetic-room")
	if e != nil || len(items) != 1 || items[0].Playlist != "streaming/synthetic-hls/"+id+"/playlist.m3u8" {
		t.Fatal(items, e)
	}
}

func TestLiveKitOutputPolicyIsCopiedAndRejectsUnconfiguredStorage(t *testing.T) {
	config := Config{Endpoint: "wss://synthetic.invalid", APIKey: "synthetic", APISecret: []byte(strings.Repeat("s", 32)), CloudRevocation: true, Timeout: time.Second}
	for _, d := range []OutputDestination{
		{Kind: "rtmp", URL: "rtmp://relay.example.invalid/live/key"},
		{Kind: "rtmp", URL: "rtmps://127.0.0.1/live/key"},
		{Kind: "rtmp", URL: "rtmps://user:password@relay.example.invalid/live/key"},
		{Kind: "hls", Storage: &OutputStorage{AccessKey: "synthetic", Secret: strings.Repeat("s", 32), Bucket: "synthetic-private"}},
		{Kind: "hls", Storage: &OutputStorage{PrivateNamespace: true, AccessKey: "synthetic", Secret: strings.Repeat("s", 32), Bucket: "synthetic-private", Endpoint: "http://storage.example.invalid"}},
	} {
		if _, e := NewWithOutputs(config, OutputPolicy{Destinations: map[string]OutputDestination{"synthetic": d}}, nil); e == nil {
			t.Fatal("unsafe operator policy accepted")
		}
	}
	storage := &OutputStorage{PrivateNamespace: true, AccessKey: "synthetic-access", Secret: strings.Repeat("s", 32), Bucket: "synthetic-private"}
	policy := OutputPolicy{Destinations: map[string]OutputDestination{"synthetic": {Kind: "hls", Storage: storage}}}
	p, e := NewWithOutputs(config, policy, roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("configuration made a provider request")
		return nil, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	before, e := p.OutputPolicyFingerprint([]string{"synthetic"}, "hls")
	if e != nil {
		t.Fatal(e)
	}
	storage.Bucket = "changed-private"
	policy.Destinations["synthetic"] = OutputDestination{Kind: "rtmp", URL: "rtmps://different.example.invalid/live/key"}
	after, e := p.OutputPolicyFingerprint([]string{"synthetic"}, "hls")
	if e != nil || string(before) != string(after) {
		t.Fatal("caller mutated active operator policy", e)
	}
	plain, e := New(config, nil)
	if e != nil {
		t.Fatal(e)
	}
	if plain.OutputContract() != "" {
		t.Fatal("plain RTC configuration enabled egress")
	}
}
