// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"testing"
)

type fakeRepo struct {
	calls  int
	target string
}

func (f *fakeRepo) AccountActivityMetrics(context.Context, string) (ActivityMetrics, error) {
	f.calls++
	return ActivityMetrics{}, nil
}
func (f *fakeRepo) AccountContacts(_ context.Context, _, ref string) ([]signin.Contact, error) {
	f.calls++
	f.target = ref
	return []signin.Contact{}, nil
}
func (f *fakeRepo) AccountFactors(context.Context, string, string) ([]signin.FactorSummary, error) {
	f.calls++
	return []signin.FactorSummary{}, nil
}
func (f *fakeRepo) AccountBadges(context.Context, string, string) ([]progression.Badge, error) {
	f.calls++
	return []progression.Badge{}, nil
}
func (f *fakeRepo) GrantAccountBadge(context.Context, string, string, progression.AdminBadge) (progression.Badge, error) {
	f.calls++
	return progression.Badge{}, nil
}
func (f *fakeRepo) ActivateAccountBadge(context.Context, string, string, string) error {
	f.calls++
	return nil
}
func (f *fakeRepo) RevokeAccountBadge(context.Context, string, string, string) error {
	f.calls++
	return nil
}

type fakeAuth struct{}

func (fakeAuth) Authenticate(*http.Request) (string, error) {
	return "d6d7dafe-7b8c-4f00-971a-f27449784ca8", nil
}
func TestAuthoredRoutesAndStrictBodies(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/admin/accounts/metrics/activity", "", 200},
		{"GET", "/api/admin/accounts/alice/contacts", "", 200},
		{"GET", "/api/admin/accounts/alice/factors", "", 200},
		{"GET", "/api/admin/accounts/alice/badges", "", 200},
		{"POST", "/api/admin/accounts/alice/badges", `{"type":"staff","label":"Staff","meta":{"color":"blue"}}`, 200},
		{"POST", "/api/admin/accounts/alice/badges/d6d7dafe-7b8c-4f00-971a-f27449784ca8/activate", "", 200},
		{"DELETE", "/api/admin/accounts/alice/badges/d6d7dafe-7b8c-4f00-971a-f27449784ca8", "", 204},
		{"GET", "/api/admin/accounts/alice/factors?token=forged", "", 400},
		{"GET", "/api/admin/accounts/alice/contacts", `{"unexpected":true}`, 400},
		{"POST", "/api/admin/accounts/alice/badges", `null`, 400},
		{"POST", "/api/admin/accounts/alice/badges", `{"type":"staff","type":"admin"}`, 400},
		{"POST", "/api/admin/accounts/alice/badges", `{"type":"staff","account_id":"forged"}`, 400},
		{"POST", "/api/admin/accounts/alice/badges", `{"type":"staff","meta":[]}`, 400},
		{"DELETE", "/api/admin/accounts/alice/badges/not-a-uuid", "", 400},
		{"PUT", "/api/admin/accounts/alice/badges", "", 405},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			repo := &fakeRepo{}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			NewHandler(repo, fakeAuth{}).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status >= 400 && repo.calls != 0 {
				t.Fatal("invalid request reached repository")
			}
			if tc.status < 400 && repo.calls != 1 {
				t.Fatal("missing repository call")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private response cached")
			}
			if tc.status < 400 && strings.HasSuffix(tc.path, "/activate") && w.Body.Len() != 0 {
				t.Fatal("activation should have an empty response")
			}
		})
	}
}
func TestAccountAdminDoesNotCaptureOtherOwners(t *testing.T) {
	for _, p := range []string{"/api/admin/accounts/alice/verification", "/api/admin/accounts/alice/board", "/api/admin/accounts/alice/spells", "/api/admin/accounts/alice/notifications/night-owl", "/api/admin/accounts/metrics/activity/more", "/api/admin/accounts/alice/factors/"} {
		if Handles(p) {
			t.Fatal("captured unrelated flow", p)
		}
	}
}

func TestBadgeAuthoredFieldLimits(t *testing.T) {
	label, caption := strings.Repeat("界", 1024), strings.Repeat("界", 4096)
	v := progression.AdminBadge{Type: strings.Repeat("界", 1024), Label: &label, Caption: &caption}
	if e := ValidateBadge(v); e != nil {
		t.Fatal("source model character limits rejected", e)
	}
	label += "x"
	if e := ValidateBadge(v); e == nil {
		t.Fatal("oversized label accepted")
	}
	label = "Staff"
	caption += "x"
	if e := ValidateBadge(v); e == nil {
		t.Fatal("oversized caption accepted")
	}
	caption = ""
	v.Type += "x"
	if e := ValidateBadge(v); e == nil {
		t.Fatal("oversized type accepted")
	}
	v.Type = "staff\x00admin"
	if e := ValidateBadge(v); e == nil {
		t.Fatal("NUL type accepted")
	}
}
