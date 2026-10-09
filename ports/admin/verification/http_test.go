// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"context"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
	"testing"
)

type fakeRepo struct {
	calls int
	kind  Kind
	mark  *accounts.VerificationMark
}

func (f *fakeRepo) SetVerification(_ context.Context, _ string, k Kind, _ string, m *accounts.VerificationMark) (any, error) {
	f.calls++
	f.kind = k
	f.mark = m
	return m, nil
}

type fakeAuth struct{}

func (fakeAuth) Authenticate(*http.Request) (string, error) {
	return "00000000-0000-4000-8000-000000000001", nil
}
func TestVerificationStrictNumericContract(t *testing.T) {
	f := &fakeRepo{}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{{"POST", "/api/admin/accounts/synthetic/verification", `{"type":0,"verified_by":"Synthetic documentation fixture"}`, 200}, {"POST", "/api/admin/publishers/synthetic/verification", `{"type":6}`, 200}, {"POST", "/api/admin/realms/synthetic/verification", `{"type":7}`, 400}, {"POST", "/api/admin/accounts/synthetic/verification", `{"type":"creator"}`, 400}, {"POST", "/api/admin/accounts/synthetic/verification", `null`, 400}, {"POST", "/api/admin/accounts/synthetic/verification", `{"type":0,"type":1}`, 400}, {"POST", "/api/admin/accounts/synthetic/verification", `{"type":0,"is_superuser":true}`, 400}, {"DELETE", "/api/admin/accounts/synthetic/verification", "", 204}} {
		before := f.calls
		r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		NewHandler(f, fakeAuth{}).ServeHTTP(w, r)
		if w.Code != tt.status || tt.status == 400 && f.calls != before {
			t.Fatal(tt, w.Code, w.Body.String())
		}
	}
}
