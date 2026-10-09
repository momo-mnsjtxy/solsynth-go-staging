// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored from the official LiveKit ingress and grant docs.
package rtcprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/sphere/livestreams"
	"strings"
	"time"
)

var _ livestreams.Provider = (*LiveKit)(nil)

// An acknowledged missing room is empty for ingress idle cleanup. Transport,
// authorization and other provider failures must never masquerade as emptiness.
func (p *LiveKit) StreamParticipants(ctx context.Context, room string) ([]calls.Participant, error) {
	if room == "" || len(room) > 256 {
		return nil, livestreams.ErrInvalid
	}
	var result struct {
		Participants []struct {
			Identity string `json:"identity"`
			Name     string `json:"name"`
		} `json:"participants"`
	}
	if e := p.request(ctx, "ListParticipants", room, map[string]any{"room": room}, &result, true); e != nil {
		return nil, livestreams.ErrProvider
	}
	if len(result.Participants) > 10000 {
		return nil, livestreams.ErrProvider
	}
	out := make([]calls.Participant, 0, len(result.Participants))
	for _, p := range result.Participants {
		if p.Identity == "" {
			return nil, livestreams.ErrProvider
		}
		out = append(out, calls.Participant{Identity: p.Identity, Name: p.Name})
	}
	return out, nil
}

func (p *LiveKit) StreamToken(ctx context.Context, g livestreams.Grant) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if g.Room == "" || g.Identity == "" || len(g.Room) > 256 || len(g.Identity) > 128 || !g.ExpiresAt.After(g.IssuedAt) || g.ExpiresAt.Sub(g.IssuedAt) > 5*time.Minute {
		return "", livestreams.ErrInvalid
	}
	grants := map[string]any{"room": g.Room, "roomJoin": true, "canSubscribe": true, "canPublish": g.Publish, "canPublishData": false, "canUpdateOwnMetadata": false, "roomAdmin": false}
	if g.Publish {
		grants["canPublishSources"] = []string{"camera", "microphone", "screen_share", "screen_share_audio"}
	}
	return p.sign(g.Identity, g.IssuedAt, g.ExpiresAt, grants)
}

func (p *LiveKit) ingressRequest(ctx context.Context, method string, body, out any, absentOK bool) error {
	if p == nil || !p.StrictRevocation() {
		return livestreams.ErrUnconfigured
	}
	b, e := json.Marshal(body)
	if e != nil {
		return livestreams.ErrInvalid
	}
	now := time.Now().UTC()
	token, e := p.sign("", now, now.Add(time.Minute), map[string]any{"ingressAdmin": true})
	if e != nil {
		return livestreams.ErrProvider
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, p.api+"/twirp/livekit.IngressService/"+method, bytes.NewReader(b))
	if e != nil {
		return livestreams.ErrProvider
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, e := p.client.Do(req)
	if e != nil {
		return livestreams.ErrProvider
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if e != nil || len(data) > 1<<20 {
		return livestreams.ErrProvider
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &problem)
		if absentOK && resp.StatusCode == 404 && problem.Code == "not_found" {
			return nil
		}
		return livestreams.ErrProvider
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return livestreams.ErrProvider
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return livestreams.ErrProvider
	}
	return nil
}

type ingressWire struct {
	ID       string          `json:"ingress_id"`
	Name     string          `json:"name"`
	Room     string          `json:"room_name"`
	Identity string          `json:"participant_identity"`
	URL      string          `json:"url"`
	Key      string          `json:"stream_key"`
	Input    json.RawMessage `json:"input_type"`
}

func (w *ingressWire) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return livestreams.ErrProvider
	}
	for camel, snake := range map[string]string{"ingressId": "ingress_id", "roomName": "room_name", "participantIdentity": "participant_identity", "streamKey": "stream_key", "inputType": "input_type"} {
		if value, ok := fields[camel]; ok {
			if _, duplicate := fields[snake]; duplicate {
				return livestreams.ErrProvider
			}
			fields[snake] = value
			delete(fields, camel)
		}
	}
	normalized, e := json.Marshal(fields)
	if e != nil {
		return e
	}
	type plain ingressWire
	return json.Unmarshal(normalized, (*plain)(w))
}

func (w ingressWire) project() (livestreams.Ingress, error) {
	typ := 0
	switch string(w.Input) {
	case "", "0", `"RTMP_INPUT"`:
		typ = 0
	case "1", `"WHIP_INPUT"`:
		typ = 1
	default:
		return livestreams.Ingress{}, livestreams.ErrProvider
	}
	u, e := url.Parse(w.URL)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || typ == 0 && (u.Scheme != "rtmp" && u.Scheme != "rtmps") || typ == 1 && u.Scheme != "https" || w.ID == "" || w.Room == "" || w.Identity == "" || len(w.ID) > 256 || len(w.Key) > 4096 || len(w.URL) > 2048 || len(w.Name) > 256 || len(w.Room) > 256 || len(w.Identity) > 128 || strings.ContainsAny(w.Key, "\r\n\x00") {
		return livestreams.Ingress{}, livestreams.ErrProvider
	}
	return livestreams.Ingress{ID: w.ID, Name: w.Name, Room: w.Room, Identity: w.Identity, URL: w.URL, Key: w.Key, InputType: typ}, nil
}
func (p *LiveKit) CreateIngress(ctx context.Context, v livestreams.IngressRequest) (livestreams.Ingress, error) {
	if v.Room == "" || v.Identity == "" || v.Name == "" || len(v.Room) > 256 || len(v.Identity) > 128 || len(v.Name) > 256 || !livestreams.Text(v.ParticipantName, 128) || !v.WHIP && !v.Transcoding {
		return livestreams.Ingress{}, livestreams.ErrInvalid
	}
	input := 0
	if v.WHIP {
		input = 1
	}
	var wire ingressWire
	e := p.ingressRequest(ctx, "CreateIngress", map[string]any{"input_type": input, "name": v.Name, "room_name": v.Room, "participant_identity": v.Identity, "participant_name": v.ParticipantName, "enable_transcoding": v.Transcoding}, &wire, false)
	if e != nil {
		return livestreams.Ingress{}, e
	}
	result, e := wire.project()
	if e != nil {
		return result, e
	}
	if result.Room != v.Room || result.Identity != v.Identity || result.Name != v.Name || result.InputType != input {
		return livestreams.Ingress{}, livestreams.ErrProvider
	}
	return result, nil
}
func (p *LiveKit) ListIngress(ctx context.Context, room string) ([]livestreams.Ingress, error) {
	if room == "" || len(room) > 256 {
		return nil, livestreams.ErrInvalid
	}
	var result struct {
		Items     []ingressWire   `json:"items"`
		Next      json.RawMessage `json:"next_page_token"`
		NextCamel json.RawMessage `json:"nextPageToken"`
	}
	if e := p.ingressRequest(ctx, "ListIngress", map[string]any{"room_name": room}, &result, false); e != nil {
		return nil, e
	}
	if len(result.Items) > 100 || (len(result.Next) > 0 && string(result.Next) != "null" && string(result.Next) != `""` && string(result.Next) != "{}") || (len(result.NextCamel) > 0 && string(result.NextCamel) != "null" && string(result.NextCamel) != `""` && string(result.NextCamel) != "{}") {
		return nil, livestreams.ErrProvider
	}
	out := make([]livestreams.Ingress, 0, len(result.Items))
	seen := map[string]bool{}
	for _, wire := range result.Items {
		v, e := wire.project()
		if e != nil || v.Room != room || seen[v.ID] {
			return nil, livestreams.ErrProvider
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out, nil
}
func (p *LiveKit) DeleteIngress(ctx context.Context, id string) error {
	if id == "" || len(id) > 256 {
		return livestreams.ErrInvalid
	}
	return p.ingressRequest(ctx, "DeleteIngress", map[string]any{"ingress_id": id}, nil, true)
}
