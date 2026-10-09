// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestQuotaShapeAndMediaReferenceValidation(t *testing.T) {
	raw, e := json.Marshal(DeveloperQuota{3, 2})
	if e != nil || string(raw) != `{"total":3,"used":2}` {
		t.Fatal(string(raw), e)
	}
	for _, id := range []string{"", "http://untrusted", "not-an-id"} {
		if ValidateApp(AppPatch{PictureID: &id}, false) != ErrInvalid || ValidateProduct(ProductPatch{BackgroundID: &id}, false) != ErrInvalid {
			t.Fatal(id)
		}
	}
	id := "00000000-0000-4000-8000-000000000001"
	if ValidateApp(AppPatch{PictureID: &id}, false) != nil || ValidateProduct(ProductPatch{BackgroundID: &id}, false) != nil {
		t.Fatal("canonical UUID rejected")
	}
}
func TestQuotaRequiresAuthenticationAndConfiguredPolicy(t *testing.T) {
	s := &Service{}
	w := httptest.NewRecorder()
	if !s.serveQuota(w, httptest.NewRequest("GET", "/api/develop/quota", nil)) || w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	if !s.serveQuota(w, httptest.NewRequest("POST", "/api/develop/quota", nil)) || w.Code != 405 {
		t.Fatal(w.Code)
	}
}
