// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored from the current official Egress JSON API docs.
package rtcprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

// These are operator configuration only. No HTTP request can supply endpoints,
// storage credentials, bucket names or an object path. Never log these values.
type OutputPolicy struct{ Destinations map[string]OutputDestination }
type OutputDestination struct {
	Kind, URL string
	Storage   *OutputStorage
}
type OutputStorage struct {
	PrivateNamespace bool   `json:"-"`
	AccessKey        string `json:"access_key"`
	Secret           string `json:"secret"`
	SessionToken     string `json:"session_token,omitempty"`
	Bucket           string `json:"bucket"`
	Region           string `json:"region,omitempty"`
	Endpoint         string `json:"endpoint,omitempty"`
	ForcePathStyle   bool   `json:"force_path_style,omitempty"`
}

var errOutputAbsent = errors.New("acknowledged missing egress")

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9-]{0,64}$`)
var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func fixedEndpoint(raw, scheme string, path bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != scheme || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(raw, "\r\n\t") || len(raw) > 2048 {
		return false
	}
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(h)
	if ip != nil || h == "localhost" || strings.HasSuffix(h, ".localhost") || !strings.Contains(h, ".") {
		return false
	}
	return path && u.Path != "" && u.Path != "/" || !path && (u.Path == "" || u.Path == "/")
}
func validateStorage(v *OutputStorage) bool {
	return v != nil && v.PrivateNamespace && len(v.AccessKey) >= 1 && len(v.AccessKey) <= 512 && len(v.Secret) >= 16 && len(v.Secret) <= 4096 && len(v.SessionToken) <= 8192 && bucketPattern.MatchString(v.Bucket) && !strings.Contains(v.Bucket, "..") && net.ParseIP(v.Bucket) == nil && regionPattern.MatchString(v.Region) && (v.Endpoint == "" || fixedEndpoint(v.Endpoint, "https", false)) && !strings.ContainsAny(v.AccessKey+v.Secret+v.SessionToken, "\r\n\x00")
}

// NewWithOutputs configures real Egress requests without making a request. The
// existing RTC provider still owns the explicit endpoint and runtime credential.
func NewWithOutputs(c Config, policy OutputPolicy, transport http.RoundTripper) (*LiveKit, error) {
	p, e := New(c, transport)
	if e != nil {
		return nil, e
	}
	if len(policy.Destinations) < 1 || len(policy.Destinations) > 32 {
		return nil, live.ErrOutputUnavailable
	}
	copy := OutputPolicy{Destinations: map[string]OutputDestination{}}
	for id, d := range policy.Destinations {
		if !live.ValidOutputDestination(id) {
			return nil, live.ErrOutputUnavailable
		}
		switch d.Kind {
		case "rtmp":
			if !fixedEndpoint(d.URL, "rtmps", true) || d.Storage != nil {
				return nil, live.ErrOutputUnavailable
			}
		case "hls", "file":
			if d.URL != "" || !validateStorage(d.Storage) {
				return nil, live.ErrOutputUnavailable
			}
			st := *d.Storage
			d.Storage = &st
		default:
			return nil, live.ErrOutputUnavailable
		}
		copy.Destinations[id] = d
	}
	p.outputs = &copy
	return p, nil
}
func (p *LiveKit) OutputContract() string {
	if p == nil || p.outputs == nil {
		return ""
	}
	return live.EgressContract
}
func (p *LiveKit) selected(ids []string, kind string) ([]string, []OutputDestination, error) {
	if p == nil || p.outputs == nil || len(ids) < 1 || len(ids) > 8 || kind != "hls" && kind != "egress" {
		return nil, nil, live.ErrOutputUnavailable
	}
	keys := append([]string(nil), ids...)
	sort.Strings(keys)
	out := []OutputDestination{}
	files := 0
	for i, id := range keys {
		d, ok := p.outputs.Destinations[id]
		if !ok || i > 0 && keys[i-1] == id {
			return nil, nil, live.ErrInvalid
		}
		if kind == "hls" && (len(ids) != 1 || d.Kind != "hls") || kind == "egress" && d.Kind == "hls" {
			return nil, nil, live.ErrInvalid
		}
		if d.Kind == "file" {
			files++
		}
		out = append(out, d)
	}
	if files > 1 {
		return nil, nil, live.ErrInvalid
	}
	return keys, out, nil
}
func (p *LiveKit) OutputPolicyFingerprint(ids []string, kind string) ([]byte, error) {
	keys, values, e := p.selected(ids, kind)
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(struct {
		Kind   string
		IDs    []string
		Values []OutputDestination
	}{kind, keys, values})
	if e != nil {
		return nil, live.ErrOutputUnavailable
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}
func outputPrefix(destination, id string) string { return "streaming/" + destination + "/" + id + "/" }
func validLayout(s string) bool {
	switch s {
	case "grid", "speaker", "single-speaker", "grid-light", "speaker-light", "single-speaker-light":
		return true
	}
	return false
}
func validRoom(s string) bool { return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\r\n\x00") }

func (p *LiveKit) StartOutput(ctx context.Context, v live.OutputStart) (live.ProviderOutput, error) {
	var zero live.ProviderOutput
	keys, values, e := p.selected(v.Destinations, v.Kind)
	if e != nil {
		return zero, e
	}
	if !authority.ValidID(v.ID) || !validRoom(v.Room) || !validLayout(v.Layout) {
		return zero, live.ErrInvalid
	}
	outputs := []any{}
	urls := []string{}
	for i, d := range values {
		prefix := outputPrefix(keys[i], v.ID)
		switch d.Kind {
		case "rtmp":
			urls = append(urls, d.URL)
		case "file":
			outputs = append(outputs, map[string]any{"file": map[string]any{"file_type": "MP4", "filepath": prefix + "recording.mp4", "disable_manifest": true}, "storage": map[string]any{"s3": d.Storage}})
		case "hls":
			if !live.ValidOutputAsset(v.Playlist) || !strings.HasSuffix(v.Playlist, ".m3u8") || v.Playlist == "recording.m3u8" || v.SegmentDuration < 2 || v.SegmentDuration > 30 {
				return zero, live.ErrInvalid
			}
			outputs = append(outputs, map[string]any{"segments": map[string]any{"protocol": "HLS_PROTOCOL", "filename_prefix": prefix + "segment", "playlist_name": prefix + "recording.m3u8", "live_playlist_name": prefix + v.Playlist, "segment_duration": v.SegmentDuration, "filename_suffix": "INDEX", "disable_manifest": true}, "storage": map[string]any{"s3": d.Storage}})
		}
	}
	if len(urls) > 0 {
		outputs = append(outputs, map[string]any{"stream": map[string]any{"protocol": "RTMP", "urls": urls}})
	}
	var raw egressInfo
	if e = p.outputRequest(ctx, "StartEgress", v.Room, map[string]any{"room_name": v.Room, "template": map[string]string{"layout": v.Layout}, "outputs": outputs}, &raw, false); e != nil {
		return zero, e
	}
	return raw.project(v.Room)
}

type egressInfo struct {
	ID       string          `json:"egress_id"`
	Room     string          `json:"room_name"`
	Status   json.RawMessage `json:"status"`
	Segments []struct {
		Playlist string `json:"playlist_name"`
		Live     string `json:"live_playlist_name"`
	} `json:"segment_results"`
}

func (v egressInfo) project(room string) (live.ProviderOutput, error) {
	if !providerIDPattern.MatchString(v.ID) || v.Room != room || len(v.Segments) > 1 {
		return live.ProviderOutput{}, live.ErrProvider
	}
	out := live.ProviderOutput{ID: v.ID, Room: v.Room, Status: -1}
	var name string
	if json.Unmarshal(v.Status, &name) == nil {
		for i, n := range []string{"EGRESS_STARTING", "EGRESS_ACTIVE", "EGRESS_ENDING", "EGRESS_COMPLETE", "EGRESS_FAILED", "EGRESS_ABORTED", "EGRESS_LIMIT_REACHED"} {
			if name == n {
				out.Status = i
				break
			}
		}
	} else {
		var n int
		if json.Unmarshal(v.Status, &n) == nil && n >= 0 && n <= 6 {
			out.Status = n
		}
	}
	if out.Status < 0 {
		return out, live.ErrProvider
	}
	if len(v.Segments) == 1 {
		out.Playlist = v.Segments[0].Live
		if out.Playlist == "" {
			out.Playlist = v.Segments[0].Playlist
		}
		if len(out.Playlist) > 512 || strings.ContainsAny(out.Playlist, "\r\n\x00") {
			return live.ProviderOutput{}, live.ErrProvider
		}
	}
	return out, nil
}
func (p *LiveKit) ListOutputs(ctx context.Context, room string) ([]live.ProviderOutput, error) {
	if !validRoom(room) {
		return nil, live.ErrInvalid
	}
	var raw struct {
		Items []egressInfo `json:"items"`
	}
	if e := p.outputRequest(ctx, "ListEgress", room, map[string]string{"room_name": room}, &raw, false); e != nil {
		return nil, e
	}
	if len(raw.Items) > 1000 {
		return nil, live.ErrProvider
	}
	out := []live.ProviderOutput{}
	seen := map[string]bool{}
	for _, v := range raw.Items {
		item, e := v.project(room)
		if e != nil || seen[item.ID] {
			return nil, live.ErrProvider
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	return out, nil
}
func (p *LiveKit) StopOutput(ctx context.Context, room, id string) (live.ProviderOutput, error) {
	if !validRoom(room) || !providerIDPattern.MatchString(id) {
		return live.ProviderOutput{}, live.ErrInvalid
	}
	var raw egressInfo
	e := p.outputRequest(ctx, "StopEgress", room, map[string]string{"egress_id": id}, &raw, true)
	if errors.Is(e, errOutputAbsent) {
		return live.ProviderOutput{ID: id, Room: room, Absent: true}, nil
	}
	if e != nil {
		return live.ProviderOutput{}, e
	}
	v, e := raw.project(room)
	if e != nil || v.ID != id {
		return live.ProviderOutput{}, live.ErrProvider
	}
	return v, nil
}
func (p *LiveKit) outputRequest(ctx context.Context, method, room string, body, out any, absentOK bool) error {
	if p == nil || p.outputs == nil || !p.StrictRevocation() {
		return live.ErrOutputUnavailable
	}
	b, e := json.Marshal(body)
	if e != nil {
		return live.ErrInvalid
	}
	at := time.Now().UTC()
	token, e := p.sign("", at, at.Add(time.Minute), map[string]any{"room": room, "roomRecord": true})
	if e != nil {
		return live.ErrProvider
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, p.api+"/twirp/livekit.Egress/"+method, bytes.NewReader(b))
	if e != nil {
		return live.ErrProvider
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := p.client.Do(req)
	if e != nil {
		return live.ErrProvider
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if e != nil || len(data) > 1<<20 {
		return live.ErrProvider
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &problem)
		if absentOK && resp.StatusCode == 404 && problem.Code == "not_found" {
			return errOutputAbsent
		}
		return live.ErrProvider
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil || json.Unmarshal(data, out) != nil {
		return live.ErrProvider
	}
	if method == "StopEgress" {
		if _, ok := object["egress_id"]; !ok {
			return live.ErrProvider
		}
	}
	return nil
}

func (p *LiveKit) OutputUsesRelay(ids []string, kind string) (bool, error) {
	_, values, e := p.selected(ids, kind)
	if e != nil {
		return false, e
	}
	for _, v := range values {
		if v.Kind == "rtmp" {
			return true, nil
		}
	}
	return false, nil
}
