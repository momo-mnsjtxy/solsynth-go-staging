// SPDX-License-Identifier: AGPL-3.0-only
package realmmembers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
)

const testID = "11111111-1111-4111-8111-111111111111"

type fake struct {
	Repository
	calls int
	label LabelRequest
}

func (f *fake) CreateLabel(_ context.Context, actor, ref, key string, q LabelRequest) (social.RealmLabel, error) {
	f.calls++
	f.label = q
	return social.RealmLabel{ID: testID, Name: q.Name}, nil
}
func (f *fake) Labels(context.Context, string, string) ([]social.RealmLabel, error) {
	f.calls++
	return []social.RealmLabel{}, nil
}
func (f *fake) History(context.Context, string, string, string, int) ([]b.Experience, error) {
	return []b.Experience{}, nil
}
func TestStrictAuthoredLabelAndAssignmentJSON(t *testing.T) {
	for _, s := range []string{`null`, `[]`, `{}`, `{"name":null}`, `{"Name":"x"}`, `{"name":"a","name":"b"}`, `{"name":"a","\u006eame":"b"}`, `{"name":" ","color":"x"}`, `{"name":"x","picture_id":"` + testID + `"}`, `{"name":"x"} {}`} {
		var v LabelRequest
		if json.Unmarshal([]byte(s), &v) == nil {
			t.Fatal("invalid label accepted", s)
		}
	}
	for _, q := range []LabelRequest{{Name: strings.Repeat("x", 1025)}, {Name: "x", Icon: str(strings.Repeat("😀", 129))}, {Name: "x", Color: str(strings.Repeat("x", 65))}, {Name: "x", Description: str(strings.Repeat("x", 4097))}, {Name: "x\x00"}} {
		if ValidateLabel(q) == nil {
			t.Fatal("field bound not applied")
		}
	}
	var q LabelRequest
	if e := json.Unmarshal([]byte(`{"name":"Makers","description":null,"color":"#ffcc00","icon":"✨"}`), &q); e != nil || q.Icon == nil || *q.Icon != "✨" {
		t.Fatal(q, e)
	}
	for _, s := range []string{`{}`, `{"label_id":"bad"}`, `{"label_id":null,"Label_ID":null}`, `{"label_id":null,"label_id":null}`} {
		var v AssignmentRequest
		if json.Unmarshal([]byte(s), &v) == nil {
			t.Fatal("invalid assignment accepted", s)
		}
	}
	var clear AssignmentRequest
	if json.Unmarshal([]byte(`{"label_id":null}`), &clear) != nil || clear.LabelID != nil {
		t.Fatal("explicit clear failed")
	}
	for _, s := range []string{`{"take":null}`, `{"take":0}`, `{"take":201}`, `{"after":"bad"}`, `{"day":"2026-10-08"}`, `{"delta":100000}`, `{"take":1,"take":2}`} {
		var v TenureRequest
		if json.Unmarshal([]byte(s), &v) == nil {
			t.Fatal("caller-selected tenure accepted", s)
		}
	}
}
func str(s string) *string { return &s }
func TestHTTPAuthorityStrictBodiesAndAuthoredExperienceRoute(t *testing.T) {
	f := &fake{}
	auth := func(r *http.Request, required bool) (string, error) {
		if r.Header.Get("Authorization") == "Bearer invalid" {
			return "", authority.ErrInvalid
		}
		if required && r.Header.Get("Authorization") == "" {
			return "", authority.ErrInvalid
		}
		if required {
			return testID, nil
		}
		return "", nil
	}
	h := NewHandler(f, auth)
	tests := []struct {
		method, path, body, token string
		want                      int
	}{{"GET", "/api/realms/demo/labels", "", "", 200}, {"POST", "/passport/realms/demo/labels", `{"name":"x"}`, "", 401}, {"POST", "/api/realms/demo/labels", `{"name":"x"}`, "ok", 200}, {"POST", "/api/realms/demo/labels", `{"name":"x","Name":"hidden"}`, "ok", 400}, {"GET", "/api/realms/demo/labels", "", "invalid", 401}, {"GET", "/api/realms/demo/members/" + testID + "/experience", "", "", 200}, {"GET", "/api/realms/demo/members?label_id=%zz", "", "", 400}, {"GET", "/api/realms/demo/members?label_id=" + testID + "&label_id=" + testID, "", "", 400}, {"HEAD", "/api/realms/demo/labels", "", "", 405}, {"GET", "/api/realms/demo/labels?token=x", "", "", 400}}
	for _, c := range tests {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			r.Header.Set("Authorization", "Bearer "+c.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Fatalf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if f.calls != 2 || f.label.Name != "x" {
		t.Fatal("invalid request reached write", f.calls)
	}
	for _, path := range []string{"/api/realms/demo/labels/../x", "/api/realms/%2F/labels", "/api/realms/demo/labels/"} {
		if Handles(path) {
			t.Fatal("unsafe/foreign route", path)
		}
	}
	r := httptest.NewRequest("POST", "/api/realms/demo/labels", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("X-Account-ID", testID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || f.calls != 2 {
		t.Fatal("trusted caller identity")
	}
}
