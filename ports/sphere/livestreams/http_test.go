// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"testing"
)

const actor = "a1000000-0000-4000-8000-000000000001"
const stream = "b1000000-0000-4000-8000-000000000001"

type fakeAuthority struct{}

func (fakeAuthority) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer synthetic" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: actor}}, nil
}
func (fakeAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (fakeAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}

type fakeRepo struct {
	Repository
	called     int
	publisher  string
	publishing bool
	who        string
}

func (r *fakeRepo) Create(_ context.Context, who, pub, key string, p Patch) (Stream, error) {
	r.called++
	r.publisher = pub
	r.who = who
	return Stream{ID: stream}, nil
}
func (r *fakeRepo) Token(_ context.Context, who, id string, publish, tool bool) (TokenResponse, error) {
	r.called++
	r.publishing = publish
	r.who = who
	return TokenResponse{Identity: "opaque"}, nil
}
func request(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestLiveHTTPStrictInputAndIdentity(t *testing.T) {
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	for _, body := range []string{`null`, `{"Title":"wrong case"}`, `{"title":"a","title":"b"}`, `{"metadata":{"x":1,"x":2}}`, `{"title":"a"} {}`, `{"ingress_id":"supplied"}`} {
		if w := request(h, "POST", "/api/livestreams", body, "Bearer synthetic"); w.Code != 400 || repo.called != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := request(h, "POST", "/api/livestreams?pub=synthetic-publisher", `{"title":"Synthetic"}`, "Bearer synthetic"); w.Code != 200 || repo.publisher != "synthetic-publisher" || repo.who != actor || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w, repo)
	}
	before := repo.called
	for _, query := range []string{"streamer=1", "streamer=true&streamer=false", "identity=someone", "token=secret"} {
		if w := request(h, "GET", "/api/livestreams/"+stream+"/token?"+query, "", "Bearer synthetic"); w.Code != 400 || repo.called != before {
			t.Fatal(w.Code, query)
		}
	}
	if w := request(h, "GET", "/api/livestreams/"+stream+"/token", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/livestreams/"+stream+"/token?streamer=false", "", "Bearer synthetic"); w.Code != 200 || repo.publishing {
		t.Fatal(w, repo)
	}
}
func TestLiveHTTPRejectsSpoofedActorAndUnsupportedEffects(t *testing.T) {
	repo := &fakeRepo{}
	h := (&Service{Repository: repo, Authority: fakeAuthority{}}).Handler()
	r := httptest.NewRequest("POST", "/api/livestreams", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer synthetic")
	r.Header.Set("X-Account-ID", actor)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || repo.called != 0 {
		t.Fatal(w)
	}
	for _, route := range []string{"awards", "chat", "egress", "hls"} {
		status := 501
		if route == "egress" || route == "hls" {
			status = 503
		}
		if w = request(h, "POST", "/api/livestreams/"+stream+"/"+route, `{}`, "Bearer synthetic"); w.Code != status || repo.called != 0 {
			t.Fatal(w)
		}
	}
}
