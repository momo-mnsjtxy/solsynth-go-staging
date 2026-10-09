// SPDX-License-Identifier: AGPL-3.0-only
package federationhttp

import (
	"context"
	"net/http/httptest"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"testing"
)

type publicAdmissionRepositoryFixture struct {
	fixtureRepo
	calls int
}

func (r *publicAdmissionRepositoryFixture) RequestPublicAdmission(_ context.Context, _, _ string, t f.DiscoveryTarget) (f.DiscoveryJob, error) {
	r.calls++
	return f.DiscoveryJob{Resource: t.Resource}, nil
}
func (r *publicAdmissionRepositoryFixture) PublicAdmission(_ context.Context, _, _ string, t f.DiscoveryTarget) (f.DiscoveryJob, error) {
	r.calls++
	return f.DiscoveryJob{Resource: t.Resource}, nil
}
func TestPublicAdmissionExactFieldsAndMethods(t *testing.T) {
	valid := `{"resource":"acct:alice@public.example.test","actor":"https://public.example.test/actors/alice"}`
	for _, tc := range []struct {
		method, query, body, media string
		want                       int
	}{{"POST", "", valid, "application/json", 200}, {"POST", "", valid, "text/plain", 400}, {"POST", "", strings.Replace(valid, `"actor"`, `"Actor"`, 1), "application/json", 400}, {"POST", "", strings.Replace(valid, `"actor":`, `"actor":"bad","actor":`, 1), "application/json", 400}, {"POST", "?extra=1", valid, "application/json", 400}, {"GET", "?resource=acct%3Aalice%40public.example.test&actor=https%3A%2F%2Fpublic.example.test%2Factors%2Falice", "", "", 200}, {"GET", "?resource=one&resource=two&actor=a", "", "", 400}, {"PUT", "", valid, "application/json", 405}} {
		repo := &publicAdmissionRepositoryFixture{}
		h := Handler{Repository: repo}
		r := httptest.NewRequest(tc.method, "/api/activitypub/publishers/p/public-admissions"+tc.query, strings.NewReader(tc.body))
		if tc.media != "" {
			r.Header.Set("Content-Type", tc.media)
		}
		w := httptest.NewRecorder()
		h.publicAdmission(w, r, "a", "p")
		if w.Code != tc.want || tc.want != 200 && repo.calls != 0 {
			t.Fatal(tc, w.Code, w.Body.String(), repo.calls)
		}
	}
}
