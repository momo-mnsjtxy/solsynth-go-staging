// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

type fixtureRepo struct {
	calls    int
	received int
}

func (r *fixtureRepo) Actor(context.Context, string) (f.Actor, error) {
	r.calls++
	return f.Actor{Context: f.Context, ID: "https://local.example.test/activitypub/actors/alice", Username: "alice", Type: "Person"}, nil
}
func (r *fixtureRepo) Object(context.Context, string) (f.Note, error) {
	r.calls++
	return f.Note{Type: "Note"}, nil
}
func (r *fixtureRepo) Outbox(context.Context, string, int, int) (f.Collection, error) {
	r.calls++
	return f.Collection{Context: f.Context, ID: "page", PartOf: "https://local.example.test/activitypub/actors/alice/outbox", Type: "OrderedCollectionPage", Total: 1, Items: []any{"synthetic"}}, nil
}
func (r *fixtureRepo) Collection(context.Context, string, string, int, int) (f.Collection, error) {
	r.calls++
	return f.Collection{Type: "OrderedCollection"}, nil
}
func (r *fixtureRepo) Receive(_ context.Context, _ string, v f.Verified) error {
	if !v.ValidFor("https://local.example.test/activitypub/actors/alice") {
		return f.ErrSignature
	}
	r.received++
	return nil
}
func (r *fixtureRepo) Settings(context.Context, string, string, f.Settings) error {
	r.calls++
	return nil
}
func (r *fixtureRepo) Change(context.Context, string, string, f.Change) error { r.calls++; return nil }
func (r *fixtureRepo) Relationships(context.Context, string, string, int, int) ([]f.Relationship, error) {
	r.calls++
	return nil, nil
}

type fixtureKeys struct{ k *rsa.PrivateKey }

func (k fixtureKeys) LocalKey(context.Context, string) (crypto.Signer, error) { return k.k, nil }
func (k fixtureKeys) RemoteKey(_ context.Context, id string) (f.RemoteKey, error) {
	if id != "https://remote.example.test/actor#main-key" {
		return f.RemoteKey{}, f.ErrNotFound
	}
	return f.RemoteKey{Actor: "https://remote.example.test/actor", Inbox: "https://remote.example.test/inbox", KeyID: id, PublicKey: &k.k.PublicKey}, nil
}
func (k fixtureKeys) RemoteActor(ctx context.Context, id string) (f.RemoteKey, error) {
	return k.RemoteKey(ctx, id+"#main-key")
}
func testHandler() (Handler, *fixtureRepo) {
	repo := &fixtureRepo{}
	return Handler{Repository: repo, Config: f.Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, MaxAttempts: 3, DeliveryTimeout: time.Second}}, repo
}
func call(h Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "https://local.example.test"+path, nil))
	return w
}
func TestHTTPDiscoveryAndBoundaries(t *testing.T) {
	h, repo := testHandler()
	tests := []struct {
		method, path string
		status       int
	}{
		{"GET", "/.well-known/webfinger?resource=acct:alice@local.example.test", 200},
		{"GET", "/.well-known/webfinger?resource=acct:alice@wrong.example.test", 404},
		{"GET", "/.well-known/webfinger?resource=acct:alice@local.example.test&resource=acct:bob@local.example.test", 400},
		{"GET", "/activitypub/actors/alice", 200},
		{"GET", "/activitypub/actors/alice/outbox", 200},
		{"GET", "/activitypub/actors/alice/outbox?offset=0&take=10", 200},
		{"GET", "/activitypub/actors/alice/outbox?offset=-1", 400},
		{"GET", "/activitypub/actors/alice/outbox?token=secret", 400},
		{"GET", "/activitypub/actors/alice/inbox", 405},
		{"POST", "/activitypub/actors/alice/outbox", 405},
		{"GET", "/activitypub/actors/alice/../bob", 400},
		{"GET", "/activitypub/actors/alice/%69nbox", 400},
		{"POST", "/api/activitypub/publishers/00000000-0000-4000-8000-000000000001/relationships", 503},
	}
	for _, tt := range tests {
		w := call(h, tt.method, tt.path)
		if w.Code != tt.status {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
	w := call(h, "GET", "/activitypub/actors/alice/outbox")
	var collection f.Collection
	if e := json.Unmarshal(w.Body.Bytes(), &collection); e != nil || collection.Type != "OrderedCollection" || collection.First == "" || collection.Total != 1 {
		t.Fatal(collection, e)
	}
	n := repo.calls
	r := httptest.NewRequest("GET", "https://local.example.test/activitypub/actors/alice", nil)
	r.Header.Set("X-Account-ID", "forged")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || repo.calls != n {
		t.Fatal("identity injection reached repository")
	}
}

func TestHTTPWebFingerAliasKeepsStableActor(t *testing.T) {
	h, _ := testHandler()
	w := call(h, "GET", "/.well-known/webfinger?resource=acct:retired@local.example.test")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	var body struct {
		Subject string   `json:"subject"`
		Aliases []string `json:"aliases"`
		Links   []struct {
			Href string `json:"href"`
		} `json:"links"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body.Subject != "acct:alice@local.example.test" || len(body.Aliases) != 1 || body.Aliases[0] != "acct:retired@local.example.test" || len(body.Links) != 1 || body.Links[0].Href != "https://local.example.test/activitypub/actors/alice" {
		t.Fatal(body)
	}
}
func TestHTTPInboxOnlyAcceptsVerifiedSignatures(t *testing.T) {
	h, repo := testHandler()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	h.Keys = fixtureKeys{key}
	body := `{"id":"https://remote.example.test/events/1","type":"Follow","actor":"https://remote.example.test/actor","object":"https://local.example.test/activitypub/actors/alice"}`
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "https://local.example.test/activitypub/actors/alice/inbox", strings.NewReader(body))
		if e := f.Sign(r, []byte(body), "https://remote.example.test/actor#main-key", key, h.Config.Clock()); e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := request()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || repo.received != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = request()
	r.Header.Del("Signature")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || repo.received != 1 {
		t.Fatal("unsigned inbox accepted", w.Code)
	}
	r = request()
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 406 || repo.received != 1 {
		t.Fatal("unsupported type accepted", w.Code)
	}
}
