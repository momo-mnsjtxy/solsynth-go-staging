// SPDX-License-Identifier: AGPL-3.0-only
package automod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fixtureID = "00000000-0000-4000-8000-000000000001"

type fakeRepo struct {
	calls   int
	enabled *bool
	action  string
}

func (f *fakeRepo) Rules(_ context.Context, _ string, v *bool) ([]Rule, error) {
	f.calls++
	f.enabled = v
	return []Rule{}, nil
}
func (f *fakeRepo) Rule(context.Context, string, string) (Rule, error) {
	f.calls++
	return basic(), nil
}
func (f *fakeRepo) CreateRule(context.Context, string, Patch) (Rule, error) {
	f.calls++
	f.action = "create"
	r := basic()
	r.ID = fixtureID
	return r, nil
}
func (f *fakeRepo) UpdateRule(context.Context, string, string, Patch) (Rule, error) {
	f.calls++
	f.action = "update"
	return basic(), nil
}
func (f *fakeRepo) DeleteRule(context.Context, string, string) error {
	f.calls++
	f.action = "delete"
	return nil
}
func (f *fakeRepo) BulkRules(context.Context, string, []Patch) ([]Rule, error) {
	f.calls++
	f.action = "bulk"
	return []Rule{}, nil
}
func (f *fakeRepo) TestRule(context.Context, string, string, string) ([]Result, error) {
	f.calls++
	f.action = "test"
	return []Result{}, nil
}

type fakeAuth struct{}

func (fakeAuth) Authenticate(*http.Request) (string, error) { return fixtureID, nil }
func TestAutomodRoutesAndBoundedBody(t *testing.T) {
	f := &fakeRepo{}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{{"GET", "/api/automod/rules?enabledOnly=true", "", 200}, {"GET", "/api/automod/rules?enabled_only=false", "", 200}, {"GET", "/api/automod/rules?enabled_only=true&enabledOnly=false", "", 400}, {"GET", "/api/automod/rules?enabled_only=true&bad=%zz", "", 400}, {"POST", "/api/automod/rules", `{"name":"synthetic","type":2,"default_action":3,"pattern":"test"}`, 201}, {"PUT", "/api/automod/rules/" + fixtureID, `{"is_enabled":false}`, 200}, {"POST", "/api/automod/rules/bulk", `[]`, 200}, {"POST", "/api/automod/rules/" + fixtureID + "/test", `{"content":"synthetic text"}`, 200}, {"POST", "/api/automod/rules/" + fixtureID + "/test", `{"content":"x","content":"y"}`, 400}, {"POST", "/api/automod/rules", `{"name":"x","expires_at":"2027-01-01"}`, 400}, {"DELETE", "/api/automod/rules/" + fixtureID, "", 204}} {
		before := f.calls
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewHandler(f, fakeAuth{}).ServeHTTP(w, r)
		if w.Code != tt.status || tt.status == 400 && f.calls != before {
			t.Fatal(tt, w.Code, w.Body.String())
		}
		if tt.status == 201 && w.Header().Get("Location") != "/api/automod/rules/"+fixtureID {
			t.Fatal("missing created resource location")
		}
	}
}
