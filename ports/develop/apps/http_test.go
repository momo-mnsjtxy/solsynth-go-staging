// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"encoding/json"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/passport/board"
	"strings"
	"testing"
	"time"
)

func TestDurationBounds(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		want  time.Duration
		valid bool
	}{{"01:02:03", time.Hour + 2*time.Minute + 3*time.Second, true}, {"1.00:00:00", 24 * time.Hour, true}, {"3h", 3 * time.Hour, true}, {"0s", 0, false}, {"-1h", 0, false}, {"31.00:00:00", 0, false}, {"99999h", 0, false}, {"nonsense", 0, false}} {
		got, e := Duration(&tt.raw, time.Hour, 30*24*time.Hour)
		if (e == nil) != tt.valid || e == nil && got != tt.want {
			t.Errorf("%q: %s %v", tt.raw, got, e)
		}
	}
}
func TestAppCredentialChannelsAndExactRoutes(t *testing.T) {
	for _, tt := range []struct {
		path, method, auth, secret, cookie string
		valid                              bool
	}{{"/api/private/apps/a/board/widgets", "GET", "Bearer test", "", "", true}, {"/api/private/apps/a/board/widgets", "GET", "", "test", "", true}, {"/api/private/apps/a/board/widgets", "GET", "Bearer test", "test", "", false}, {"/api/private/apps/a/board/widgets?token=x", "GET", "Bearer test", "", "", false}, {"/api/private/apps/a/board/widgets", "GET", "Bearer test", "", "x", false}, {"/api/private/apps/a/board/widgets", "GET", "Bearer  test", "", "", false}} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		if tt.auth != "" {
			r.Header.Set("Authorization", tt.auth)
		}
		if tt.secret != "" {
			r.Header.Set("X-App-Secret", tt.secret)
		}
		if tt.cookie != "" {
			r.Header.Set("Cookie", tt.cookie)
		}
		_, e := appCredential(r)
		if (e == nil) != tt.valid {
			t.Errorf("channel acceptance mismatch %s", tt.path)
		}
	}
	for _, path := range []string{"/api/private/apps/x", "/api/private/apps/x/secrets", "/api/private/apps/x/board/widgets/extra", "/api/private/apps/x/board"} {
		if IsAppCredentialRoute(httptest.NewRequest("GET", path, nil)) {
			t.Fatal("broad app bypass", path)
		}
	}
}
func TestAppCredentialDispatchRequiresExactMethodAndAppID(t *testing.T) {
	path := "/api/private/apps/11111111-1111-4111-8111-111111111111/board/widgets"
	if !IsAppCredentialRoute(httptest.NewRequest("GET", path, nil)) {
		t.Fatal("valid app route missing")
	}
	if IsAppCredentialRoute(httptest.NewRequest("POST", path, nil)) {
		t.Fatal("wrong method bypassed account middleware")
	}
	if IsAppCredentialRoute(httptest.NewRequest("GET", "/api/private/apps/not-an-id/board/widgets", nil)) {
		t.Fatal("invalid app id bypassed account middleware")
	}
}

func TestValidationRejectsUnavailableProvidersAndScopeEscalation(t *testing.T) {
	bad := "unknown.admin"
	if ValidateApp(AppPatch{OAuthConfig: &OAuthConfig{AllowedScopes: []string{bad}}}, false) == nil {
		t.Fatal("unknown scope")
	}
	for _, scope := range []string{BoardScope, "orders.create", "orders.status.manage", "orders.metrics.read"} {
		if !SupportedClientScope(scope) {
			t.Fatal(scope)
		}
	}
	if SupportedClientScope("*") || SupportedClientScope("openid") {
		t.Fatal("client became account")
	}
	negative := json.Number("-0.01")
	if ValidateProduct(ProductPatch{Price: &negative}, false) == nil {
		t.Fatal("negative price")
	}
	exp := json.Number("1e30")
	if ValidateProduct(ProductPatch{Price: &exp}, false) == nil {
		t.Fatal("exponent price")
	}
	recurrence := "monthly"
	if e := ValidateProduct(ProductPatch{Recurrence: &recurrence}, false); e != nil {
		t.Fatal("owned recurrence rejected", e)
	}
	unknown := "external-card"
	if ValidateProduct(ProductPatch{Recurrence: &unknown}, false) == nil {
		t.Fatal("unknown recurrence")
	}
	w := board.Manifest{Key: "weather", Name: "Weather", PayloadType: "object", RendererType: "script"}
	if ValidateWidget(w) == nil {
		t.Fatal("active renderer accepted")
	}
}
func TestHTTPRejectsUnknownFieldsAndMultipleBodies(t *testing.T) {
	for _, body := range []string{`null`, `{"name":"x","publisher_id":"spoof"}`, `{} {}`, `{"status":"production"}`} {
		r := httptest.NewRequest("POST", "/api/private/apps", strings.NewReader(body))
		var p AppPatch
		if decode(httptest.NewRecorder(), r, &p) == nil {
			t.Fatal("accepted ambiguous body", body)
		}
	}
}
