// SPDX-License-Identifier: AGPL-3.0-only
package ipcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

type allow struct{ err error }

func (a allow) AuthorizeDiagnostic(*http.Request) error { return a.err }
func dataset(t *testing.T) *CIDRProvider {
	t.Helper()
	p, e := NewDataset(Dataset{Revision: "synthetic-v1", Provenance: "Independently authored synthetic test data", Entries: []Entry{{"198.51.100.0/24", Geo{City: ptr("Synthetic"), Latitude: ptr(0.0), Longitude: ptr(0.0)}}, {"203.0.113.0/24", Geo{City: ptr("No coordinates")}}}})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func call(s *Service, path, remote string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestGeoRoutesAndDiagnosticPrivacy(t *testing.T) {
	s, e := New(Config{Provider: dataset(t)}, allow{})
	if e != nil {
		t.Fatal(e)
	}
	w := call(s, "/api/ip-check/geo", "198.51.100.9:1234", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var g Geo
	if json.Unmarshal(w.Body.Bytes(), &g) != nil || g.Latitude == nil || *g.Latitude != 0 {
		t.Fatal("zero coordinate lost")
	}
	for _, tc := range []struct{ remote, code string }{{"192.0.2.1:1", "PASSPORT_IP_GEO_NOT_FOUND"}, {"203.0.113.1:1", "PASSPORT_IP_GEO_NO_COORDINATES"}} {
		w = call(s, "/api/ip-check/geo", tc.remote, nil)
		if w.Code != 404 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = call(s, "/api/ip-check", "198.51.100.9:1", map[string]string{"Authorization": "Bearer synthetic-secret", "Cookie": "synthetic-cookie", "X-Api-Key": "synthetic-key", "User-Agent": "Synthetic fixture"})
	if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-secret") || strings.Contains(w.Body.String(), "synthetic-cookie") || strings.Contains(w.Body.String(), "synthetic-key") || !strings.Contains(w.Body.String(), "Synthetic fixture") {
		t.Fatal("diagnostic header policy", w.Code, w.Body.String())
	}
	s.auth = allow{authority.ErrInvalid}
	if w = call(s, "/api/ip-check", "198.51.100.9:1", nil); w.Code != 401 {
		t.Fatal("authority status lost", w.Code)
	}
	if w = call(s, "/api/ip-check/geo", "198.51.100.9:1", nil); w.Code != 200 {
		t.Fatal("public geo required authority")
	}
	empty, _ := New(Config{}, nil)
	if w = call(empty, "/api/ip-check/geo", "198.51.100.9:1", nil); w.Code != 503 {
		t.Fatal("unconfigured geo fabricated", w.Code)
	}
}
func TestTrustedProxyChainAndSpoofRejection(t *testing.T) {
	s, _ := New(Config{Provider: dataset(t)}, allow{})
	w := call(s, "/api/ip-check/geo", "192.0.2.1:3", map[string]string{"X-Forwarded-For": "198.51.100.9"})
	if w.Code != 404 {
		t.Fatal("untrusted forwarded IP accepted")
	}
	s, e := New(Config{Provider: dataset(t), TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, ForwardedHeader: "X-Forwarded-For"}, allow{})
	if e != nil {
		t.Fatal(e)
	}
	w = call(s, "/api/ip-check", "10.0.0.5:3", map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.9, 10.0.0.3"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"client_ip":"198.51.100.9"`) {
		t.Fatal("trusted chain boundary", w.Body.String())
	}
	if w = call(s, "/api/ip-check/geo", "10.0.0.5:3", map[string]string{"X-Forwarded-For": "invalid"}); w.Code != 400 {
		t.Fatal("malformed trusted header")
	}
	for _, header := range []string{"X-Real-IP", "CF-Connecting-IP"} {
		s, e = New(Config{Provider: dataset(t), TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, ForwardedHeader: header}, allow{})
		if e != nil {
			t.Fatal(e)
		}
		if w = call(s, "/api/ip-check/geo", "10.0.0.5:3", map[string]string{header: "198.51.100.9", "X-Forwarded-For": "invalid"}); w.Code != 200 {
			t.Fatal("explicit single header", w.Code)
		}
	}
	if _, e = New(Config{ForwardedHeader: "X-Forwarded-For"}, nil); e == nil {
		t.Fatal("implicit proxy trust")
	}
}
func TestDatasetLongestPrefixValidationAndIsolation(t *testing.T) {
	a := Geo{City: ptr("Broad"), Latitude: ptr(1.0), Longitude: ptr(2.0)}
	d := Dataset{Revision: "synthetic", Provenance: "Own synthetic records", Entries: []Entry{{"198.51.100.0/24", a}, {"198.51.100.9/32", Geo{City: ptr("Specific")}}}}
	p, e := NewDataset(d)
	if e != nil {
		t.Fatal(e)
	}
	*a.City = "Changed input"
	g, e := p.Lookup(context.Background(), netip.MustParseAddr("::ffff:198.51.100.1"))
	if e != nil || *g.City != "Broad" {
		t.Fatal("provider retained mutable input")
	}
	*g.City = "Changed result"
	g, _ = p.Lookup(context.Background(), netip.MustParseAddr("198.51.100.1"))
	if *g.City != "Broad" {
		t.Fatal("mutable result retained")
	}
	g, _ = p.Lookup(context.Background(), netip.MustParseAddr("198.51.100.9"))
	if *g.City != "Specific" {
		t.Fatal("longest prefix")
	}
	d.Entries = append(d.Entries, d.Entries[0])
	if _, e = NewDataset(d); e == nil {
		t.Fatal("duplicate prefix")
	}
	if _, e = DecodeDataset(strings.NewReader(`{"revision":"synthetic","provenance":"Own","entries":[],"unexpected":true}`)); e == nil {
		t.Fatal("unknown config field")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = p.Lookup(ctx, netip.MustParseAddr("198.51.100.1")); e == nil {
		t.Fatal("cancelled lookup")
	}
}
