// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/passport/ipcheck"
	"strings"
	"testing"
)

func TestPGBackendIPCheckComposition(t *testing.T) {
	f := newBackendFixture(t)
	lat, lon := 12.5, -6.25
	city := "Synthetic city"
	provider, e := ipcheck.NewDataset(ipcheck.Dataset{Revision: "synthetic-v1", Provenance: "Independently authored synthetic records", Entries: []ipcheck.Entry{{Prefix: "198.51.100.0/24", Geo: ipcheck.Geo{City: &city, Latitude: &lat, Longitude: &lon}}}})
	if e != nil {
		t.Fatal(e)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, IPCheck: &ipcheck.Config{Provider: provider, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, ForwardedHeader: "X-Forwarded-For"}})
	if e != nil {
		t.Fatal(e)
	}
	call := func(path, peer, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := fixtureNoBodyRequest("GET", path, token)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", "198.51.100.7")
		r.Header.Set("X-Role", "admin")
		r.Header.Set("Cookie", "synthetic-cookie-secret")
		r.Header.Set("X-Api-Key", "synthetic-header-secret")
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	w := call("/passport/ip-check/geo", "10.0.0.5:1234", "", 200)
	var geo ipcheck.Geo
	if json.Unmarshal(w.Body.Bytes(), &geo) != nil || geo.City == nil || *geo.City != city {
		t.Fatal("geo route projection")
	}
	call("/api/ip-check/geo", "192.0.2.1:1234", "", 404)
	call("/api/ip-check", "10.0.0.5:1234", "", 401)
	call("/passport/ip-check", "10.0.0.5:1234", f.token, 403)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true,version=version+1 WHERE id=$1::uuid`, f.actor)
	admin := pgIssue(t, f.issuer, f.actor).Token
	w = call("/passport/ip-check", "10.0.0.5:1234", admin, 200)
	if strings.Contains(w.Body.String(), admin) || strings.Contains(w.Body.String(), "synthetic-cookie-secret") || strings.Contains(w.Body.String(), "synthetic-header-secret") {
		t.Fatal("diagnostic secret reflection")
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false,version=version+1 WHERE id=$1::uuid`, f.actor)
	call("/api/ip-check", "10.0.0.5:1234", admin, 401)
	call("/api/ip-check", "10.0.0.5:1234", pgIssue(t, f.issuer, f.actor).Token, 403)
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
}
