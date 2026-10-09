// SPDX-License-Identifier: AGPL-3.0-only
package activities

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/authority"
)

type spy struct {
	Repository
	q                 Request
	f                 Filter
	actor, target, id string
	session           bool
	calls             int
}

func (s *spy) Set(_ context.Context, a string, q Request, b bool, id string) (Activity, error) {
	s.calls++
	s.actor = a
	s.q = q
	s.session = b
	s.id = id
	return Activity{Content: q.Content, ID: "activity", AccountID: a, Visibility: q.Visibility, LeaseMinutes: q.LeaseMinutes}, nil
}
func (s *spy) List(_ context.Context, a, b string, f Filter) ([]Activity, int, error) {
	s.calls++
	s.actor = a
	s.target = b
	s.f = f
	return []Activity{}, 17, nil
}
func (s *spy) Catalog(_ context.Context, a string, f Filter) ([]Catalog, error) {
	s.calls++
	s.actor = a
	s.f = f
	return []Catalog{}, nil
}
func (s *spy) Stats(_ context.Context, a string, f Filter, _ bool) (map[string]int64, error) {
	s.calls++
	s.actor = a
	s.f = f
	return map[string]int64{}, nil
}
func (s *spy) End(_ context.Context, a, m string) (Activity, error) {
	s.calls++
	s.actor = a
	s.id = m
	return Activity{}, nil
}
func (s *spy) Delete(_ context.Context, a, id, m string) error {
	s.calls++
	s.actor = a
	s.id = id + ":" + m
	return nil
}

type auth struct{}

func (auth) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	if r.Header.Get("Authorization") != "Bearer signed-fixture" {
		return authority.Principal{}, authority.ErrInvalid
	}
	return authority.Principal{Account: authority.Account{ID: "11111111-1111-4111-8111-111111111111"}}, nil
}
func TestSourceActivityHTTPBodiesFiltersAndAnonymousBoundary(t *testing.T) {
	s := &spy{}
	h := (&Service{Repository: s, Authority: auth{}, Bind: func(c context.Context, _ authority.Principal) context.Context { return c }}).Handler()
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/activities/sessions/start", `{"manual_id":"editor","type":"coding","catalog_key":"project","visibility":2,"tags":[{"slug":"work","name":"Work"}]}`, "signed-fixture")
	if w.Code != 200 || !s.session || s.q.ManualID == nil || *s.q.ManualID != "editor" || s.q.LeaseMinutes != 5 || s.q.Visibility != 2 || !s.q.VisibilitySpecified {
		t.Fatal(w.Code, w.Body.String(), s.q)
	}
	var v map[string]any
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v["lease_minutes"] != float64(5) || v["account_id"] == nil {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/activities", "/api/activities/sessions/start"} {
		w = call("POST", path, `{"manual_id":"editor"}`, "signed-fixture")
		if w.Code != 200 || s.q.VisibilitySpecified || s.q.Type != nil {
			t.Fatal("omitted renewal fields lost presence information", w.Code, s.q)
		}
		w = call("POST", path, `{"manual_id":"editor","Visibility":0}`, "signed-fixture")
		if w.Code != 200 || !s.q.VisibilitySpecified || s.q.Visibility != Private {
			t.Fatal("accepted field casing lost explicit private audience", w.Code, s.q)
		}
	}
	w = call("GET", "/api/activities?includeExpired=true&provider=steam&referenceId=123&tags=RPG&tags=Gaming&take=2&offset=3", "", "signed-fixture")
	if w.Code != 200 || w.Header().Get("X-Total") != "17" || !s.f.IncludeExpired || s.f.ReferenceID != "123" || len(s.f.Tags) != 2 || s.f.Take != 2 || s.f.Offset != 3 {
		t.Fatal(w.Code, s.f)
	}
	if w = call("GET", "/api/activities/alice?includeExpired=true", "", ""); w.Code != 200 || s.actor != "" || s.f.IncludeExpired {
		t.Fatal("public history boundary", w.Code, s.f)
	}
	for _, p := range []string{"/api/activities", "/api/activities/catalog", "/api/activities/stats", "/api/activities/tags/stats"} {
		n := s.calls
		if w = call("GET", p, "", ""); w.Code != 401 || s.calls != n {
			t.Fatal(p, w.Code)
		}
	}
	for _, p := range []string{"/api/activities?take=0", "/api/activities?take=2&take=3", "/api/activities?visibility=9", "/api/activities?access_token=forged", "/api/activities?from=bad"} {
		if w = call("GET", p, "", "signed-fixture"); w.Code != 400 {
			t.Fatal(p, w.Code)
		}
	}
	for _, body := range []string{`null`, `{} {}`, `{"account_id":"attacker"}`, `{"lease_minutes":"5"}`, `{"visibility":null}`, `{"Visibility":null}`} {
		n := s.calls
		if w = call("POST", "/api/activities", body, "signed-fixture"); w.Code != 400 || n != s.calls {
			t.Fatal(body, w.Code)
		}
	}
	for _, p := range []string{"/api/activities/catalog", "/api/activities/stats", "/api/activities/tags/stats"} {
		if w = call("GET", p, "", "signed-fixture"); w.Code != 200 {
			t.Fatal(p, w.Code)
		}
	}
	if w = call("POST", "/api/activities/sessions/end", `{"manual_id":"editor"}`, "signed-fixture"); w.Code != 200 || s.id != "editor" {
		t.Fatal(w.Code, s.id)
	}
	if w = call("DELETE", "/api/activities?manualId=editor", "", "signed-fixture"); w.Code != 204 || s.id != ":editor" {
		t.Fatal(w.Code, s.id)
	}
}
func TestActivityValidationNormalizationAndSafeLinks(t *testing.T) {
	str := func(s string) *string { return &s }
	q := Request{Content: Content{Type: str(" coding "), Provider: str(" STEAM "), QueryableTerms: []string{" Editor ", "EDITOR", ""}, Tags: []Tag{{" RPG ", "Role"}, {"RPG", "Duplicate"}}}, ManualID: str("game"), LeaseMinutes: 5, Visibility: Public}
	if e := Normalize(&q, true); e != nil || *q.Type != "coding" || *q.Provider != "steam" || len(q.QueryableTerms) != 1 || len(q.Tags) != 1 || q.Tags[0].Name != "Role" {
		t.Fatal(q, e)
	}
	for _, u := range []string{"javascript:alert(1)", "data:image/png;base64,bad", "http://insecure.example/x", "https://user:pass@example.test/x"} {
		q.TitleURL = &u
		if e := Normalize(&q, true); e == nil {
			t.Fatal("unsafe link", u)
		}
	}
	q.TitleURL = nil
	q.LargeImage = str("sha256:" + strings.Repeat("A", 64))
	if e := Normalize(&q, true); e != nil || *q.LargeImage != "sha256:"+strings.Repeat("a", 64) {
		t.Fatal(q, e)
	}
	q.LeaseMinutes = 61
	if Normalize(&q, true) == nil {
		t.Fatal("unbounded lease")
	}
}
