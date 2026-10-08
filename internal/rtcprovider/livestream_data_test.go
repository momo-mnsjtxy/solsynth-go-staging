// SPDX-License-Identifier: AGPL-3.0-only
package rtcprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"strings"
	"testing"
)

func TestLiveStreamDataUsesExplicitDestinationsAndRoomScope(t *testing.T) {
	recipient := "d1000000-0000-4000-8000-000000000001"
	payload := []byte(`{"id":"stable-event","content":"synthetic"}`)
	var p *LiveKit
	requests := 0
	p = fixture(t, roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "synthetic.invalid" || r.URL.Path != "/twirp/livekit.RoomService/SendData" || r.Method != "POST" {
			t.Fatal(r.URL)
		}
		grant := claims(t, p, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["video"].(map[string]any)
		if len(grant) != 2 || grant["room"] != "synthetic-room" || grant["roomAdmin"] != true {
			t.Fatal(grant)
		}
		var body struct {
			Room         string   `json:"room"`
			Data         string   `json:"data"`
			Kind         string   `json:"kind"`
			Destinations []string `json:"destination_identities"`
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Fatal(e)
		}
		raw, e := base64.StdEncoding.DecodeString(body.Data)
		if e != nil || string(raw) != string(payload) || body.Kind != "RELIABLE" || body.Room != "synthetic-room" || len(body.Destinations) != 1 || body.Destinations[0] != recipient {
			t.Fatal(body, e)
		}
		return response(200, `{}`), nil
	}))
	if e := p.SendStreamData(context.Background(), "synthetic-room", []string{recipient}, payload); e != nil {
		t.Fatal(e)
	}
	for _, ids := range [][]string{nil, {}, {recipient, recipient}, {"user-supplied"}} {
		if e := p.SendStreamData(context.Background(), "synthetic-room", ids, payload); !errors.Is(e, live.ErrInvalid) {
			t.Fatal(ids, e)
		}
	}
	if e := p.SendStreamData(context.Background(), "synthetic-room", []string{recipient}, []byte(`{"x":"`+strings.Repeat("a", live.MaxDataPacketBytes)+`"}`)); !errors.Is(e, live.ErrInvalid) {
		t.Fatal(e)
	}
	if requests != 1 {
		t.Fatal("invalid packets reached transport", requests)
	}
}
func TestLiveStreamDataRequiresAcknowledgedProviderResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{500, `{"message":"provider secret"}`}, {404, `{"code":"not_found"}`}, {200, `null`}, {302, `{}`}, {200, `invalid`}} {
		p := fixture(t, roundTrip(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }))
		e := p.SendStreamData(context.Background(), "synthetic-room", []string{"d1000000-0000-4000-8000-000000000001"}, []byte(`{}`))
		if !errors.Is(e, live.ErrProvider) || strings.Contains(e.Error(), "secret") {
			t.Fatal(tc, e)
		}
	}
}
