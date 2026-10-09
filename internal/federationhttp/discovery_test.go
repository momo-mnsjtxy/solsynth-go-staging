// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"testing"
)

type serverFixture struct {
	fixtureRepo
	err error
}

func (r *serverFixture) ServerActor(context.Context) (f.ServerActor, error) {
	return f.ServerActor{ID: "https://local.example.test/activitypub/actor", Username: "synthetic-server", Type: "Application", PublicKey: f.PublicKey{ID: "https://local.example.test/activitypub/actor#main-key", Owner: "https://local.example.test/activitypub/actor", PEM: "synthetic-public"}}, r.err
}
func TestServerDiscoveryRoutesAndEmptyPages(t *testing.T) {
	h, _ := testHandler()
	h.Repository = &serverFixture{}
	for _, path := range []string{"/activitypub/actor", "/activitypub/actor/main-key", "/activitypub/actor/outbox", "/activitypub/actor/followers", "/activitypub/actor/following", "/activitypub/actor/outbox?page=true", "/.well-known/webfinger?resource=acct:SYNTHETIC-server@local.example.test", "/activitypub/actors/alice/main-key", "/activitypub/actors/alice/publickey"} {
		w := call(h, "GET", path)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := call(h, "GET", "/activitypub/actor/outbox?page=true")
	var page map[string]any
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page["type"] != "OrderedCollectionPage" || page["totalItems"] != float64(0) || len(page["orderedItems"].([]any)) != 0 {
		t.Fatal(page)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/activitypub/actor?bad=true", 400}, {"POST", "/activitypub/actor", 405}, {"POST", "/activitypub/actor/inbox", 422}, {"GET", "/activitypub/actor/outbox?page=false", 400}, {"GET", "/activitypub/actor/followers?page=true&page=true", 400}, {"GET", "/activitypub/actor/missing", 404}, {"GET", "/activitypub/actors/alice/main-key?token=bad", 400}} {
		w := call(h, tc.method, tc.path)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code)
		}
	}
	w = call(h, "GET", "/.well-known/host-meta")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xrd+xml") {
		t.Fatal(w.Code, w.Header())
	}
	var doc struct {
		Link struct {
			Template string `xml:"template,attr"`
		} `xml:"Link"`
	}
	if e := xml.Unmarshal(w.Body.Bytes(), &doc); e != nil || doc.Link.Template != h.Config.Origin+"/.well-known/webfinger?resource={uri}" {
		t.Fatal(doc, e)
	}
	if !IsPublicRoute("/.well-known/host-meta") || !IsPrivateRoute("/api/activitypub/publishers/p/discovery") {
		t.Fatal("missing routing boundary")
	}
	h.Repository = &serverFixture{err: f.ErrUnavailable}
	if w = call(h, "GET", "/activitypub/actor/main-key"); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

type discoveryRepositoryFixture struct {
	fixtureRepo
	calls int
}

func (r *discoveryRepositoryFixture) RequestDiscovery(_ context.Context, _, _, resource string) (f.DiscoveryJob, error) {
	r.calls++
	return f.DiscoveryJob{Resource: resource}, nil
}
func (r *discoveryRepositoryFixture) Discovery(_ context.Context, _, _, resource string) (f.DiscoveryJob, error) {
	r.calls++
	return f.DiscoveryJob{Resource: resource}, nil
}
func TestDiscoveryRequestExactMediaAndFieldNames(t *testing.T) {
	for _, tc := range []struct {
		body, media string
		want        int
	}{
		{`{"resource":"acct:alice@remote.example.test"}`, "application/json", 200},
		{`{"resource":"acct:alice@remote.example.test"}`, "text/plain", 400},
		{`{"resource":"acct:alice@remote.example.test"}`, "", 400},
		{`{"Resource":"acct:alice@remote.example.test"}`, "application/json", 400},
		{`{"resource":"acct:alice@remote.example.test","RESOURCE":"acct:bob@remote.example.test"}`, "application/json", 400},
		{`{"resource":null}`, "application/json", 400},
		{`{"resource":"acct:alice@remote.example.test","extra":1}`, "application/json", 400},
	} {
		repo := &discoveryRepositoryFixture{}
		h := Handler{Repository: repo}
		request := httptest.NewRequest("POST", "/api/activitypub/publishers/p/discovery", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", tc.media)
		w := httptest.NewRecorder()
		h.discovery(w, request, "a", "p")
		if w.Code != tc.want || tc.want != 200 && repo.calls != 0 {
			t.Fatal(tc, w.Code, w.Body.String(), repo.calls)
		}
	}
}
