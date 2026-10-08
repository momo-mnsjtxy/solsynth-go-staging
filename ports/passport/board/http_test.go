// SPDX-License-Identifier: AGPL-3.0-only
package board

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const otherAccount = "00000000-0000-0000-0000-000000000009"

func httpFixture(t *testing.T) *SyntheticFixture {
	t.Helper()
	limit := 2048
	f, err := NewSyntheticFixture(SyntheticConfig{Synthetic: true, AllowPublicReads: true, Accounts: []Account{{owner, "alice"}, {otherAccount, "bob"}}, Principals: []SyntheticPrincipal{{Name: "alice", AccountID: owner, Permissions: []string{SelfManage}}, {Name: "reader", AccountID: owner, Permissions: []string{AccountsView}}, {Name: "admin", AccountID: otherAccount, Permissions: []string{AccountsView, BoardManage}}, {Name: "app", AppID: app}}, Apps: []SyntheticApp{{ID: app, BoardCapability: true, ConsentingAccounts: []string{owner}, Widgets: []Manifest{{Key: "Status", Name: "Status", IsEnabled: true, PayloadType: "object", RendererType: "data", RequiredFields: []string{"title"}, FieldTypes: []FieldType{{Name: "title", Type: "string"}}, MaxPayloadBytes: &limit}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func boardRequest(h http.Handler, method, path, principal, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if principal != "" {
		r.Header.Set("X-Synthetic-Principal", principal)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("want %d got %d: %s", status, w.Code, w.Body.String())
	}
}
func TestHTTPBoardDefaultAuthorityDeniesAll(t *testing.T) {
	h := NewHTTPHandler(HTTPOptions{})
	for _, route := range []struct{ method, path string }{{"GET", "/api/accounts/me/board"}, {"PUT", "/api/accounts/me/board"}, {"GET", "/api/accounts/alice/board"}, {"GET", "/api/admin/accounts/alice/board"}, {"PUT", "/api/admin/accounts/alice/board"}, {"POST", "/api/admin/accounts/alice/board/items/" + itemID + "/payload"}, {"DELETE", "/api/admin/accounts/alice/board/items/" + itemID}, {"POST", "/api/private/apps/" + app + "/board/payload"}, {"GET", "/api/private/apps/" + app + "/board/widgets"}} {
		expectStatus(t, boardRequest(h, route.method, route.path, "alice", "[]"), 503)
	}
}
func TestHTTPBoardLayoutDefaultsBindingAndPreservation(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{}
	h := f.HTTPHandler(r, nil)
	w := boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[{"id":"`+itemID+`","widget_key":"memo","payload":{"large":9007199254740993}}]`)
	expectStatus(t, w, 200)
	if len(r.items) != 1 || !r.items[0].IsEnabled || r.items[0].Payload["large"] != json.Number("9007199254740993") || r.items[0].AccountID != owner {
		t.Fatal(r.items)
	}
	// Prebuilt layout payload is client controlled in the actual owned service,
	// despite a conflicting broad claim in ACCOUNT_BOARD.md.
	w = boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[{"id":"`+itemID+`","kind":1,"custom_app_id":"`+app+`","custom_app_widget_key":"status","is_enabled":false,"payload":{"title":"client ignored"}}]`)
	expectStatus(t, w, 200)
	if r.items[0].IsEnabled || len(r.items[0].Payload) != 0 || *r.items[0].CustomAppWidgetKey != "Status" {
		t.Fatal(r.items)
	}
	w = boardRequest(h, "POST", "/api/private/apps/"+app+"/board/payload", "app", `{"account_id":"`+owner+`","widget_key":"status","payload":{"title":{"value":"server","label":"Title"}}}`)
	expectStatus(t, w, 200)
	w = boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[{"id":"`+itemID+`","kind":1,"custom_app_id":"`+app+`","custom_app_widget_key":"STATUS","payload":{"title":"overwrite"}}]`)
	expectStatus(t, w, 200)
	if r.items[0].Payload["title"].(map[string]any)["value"] != "server" {
		t.Fatal(r.items)
	}
	expectStatus(t, boardRequest(h, "GET", "/api/accounts/alice/board", "", ""), 200)
	expectStatus(t, boardRequest(h, "GET", "/api/admin/accounts/ALICE/board", "admin", ""), 404)
}
func TestHTTPBoardPermissionAndPayloadBranches(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{items: []Item{{ID: itemID, AccountID: owner, Kind: CustomApp, CustomAppID: ptr(app), CustomAppWidgetKey: ptr("Status"), Payload: Payload{}}}}
	h := f.HTTPHandler(r, nil)
	expectStatus(t, boardRequest(h, "GET", "/api/accounts/me/board", "", ""), 401)
	expectStatus(t, boardRequest(h, "GET", "/api/accounts/me/board", "reader", ""), 403) // Same account never unions another principal's grants.
	expectStatus(t, boardRequest(h, "GET", "/api/admin/accounts/alice/board", "alice", ""), 403)
	expectStatus(t, boardRequest(h, "GET", "/api/admin/accounts/alice/board", "reader", ""), 200)
	expectStatus(t, boardRequest(h, "GET", "/api/private/apps/"+app+"/board/widgets", "app", ""), 200)
	expectStatus(t, boardRequest(h, "GET", "/api/private/apps/"+app+"/board/widgets", "alice", ""), 403)
	for _, body := range []string{`{"account_id":"` + owner + `","widget_key":"status","payload":{"title":"raw"}}`, `{"account_id":"` + owner + `","widget_key":"status","payload":{"title":{"value":3,"label":"T"}}}`, `{"account_id":"` + owner + `","widget_key":"status","payload":{"other":{"value":"x","label":"T"}}}`} {
		expectStatus(t, boardRequest(h, "POST", "/api/private/apps/"+app+"/board/payload", "app", body), 400)
	}
	expectStatus(t, boardRequest(h, "POST", "/api/private/apps/"+app+"/board/payload", "app", `{"account_id":"`+otherAccount+`","widget_key":"status","payload":{}}`), 403)
	expectStatus(t, boardRequest(h, "POST", "/api/admin/accounts/alice/board/items/"+itemID+"/payload", "admin", `{"payload":{"title":{"value":"admin","label":"Title"}}}`), 200)
	if r.items[0].Payload["title"].(map[string]any)["value"] != "admin" {
		t.Fatal(r.items)
	}
	expectStatus(t, boardRequest(h, "POST", "/api/admin/accounts/alice/board/items/"+otherAccount+"/payload", "admin", `{"payload":{}}`), 404)
	if r.saves != 1 {
		t.Fatal("failed payloads were persisted", r.saves)
	}
}

type deleteRecorder struct{ calls int }

func (d *deleteRecorder) Delete(context.Context, string, string) error { d.calls++; return nil }
func TestHTTPBoardRealCacheInvalidationAndDeleteException(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{}
	d := &deleteRecorder{}
	h := f.HTTPHandler(r, d)
	_, _ = f.Resolve(context.Background(), "alice")
	if _, ok := f.Cache.Get("account:"+owner, "alice"); !ok {
		t.Fatal("resolver did not cache")
	}
	expectStatus(t, boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[]`), 200)
	if _, ok := f.Cache.Get("account:"+owner, "alice"); ok {
		t.Fatal("committed replacement did not invalidate")
	}
	expectStatus(t, boardRequest(h, "DELETE", "/api/admin/accounts/alice/board/items/"+itemID, "admin", ""), 204)
	if d.calls != 1 {
		t.Fatal(d.calls)
	}
	if _, ok := f.Cache.Get("account:"+owner, "alice"); !ok {
		t.Fatal("delete invented cache purge absent in source")
	}
}
func TestHTTPBoardInfrastructureErrorsDoNotBecomeValidation(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{replaceErr: errors.New("private database secret should not leak")}
	h := f.HTTPHandler(r, nil)
	w := boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[]`)
	expectStatus(t, w, 500)
	if strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Body.String())
	}
	h = NewHTTPHandler(HTTPOptions{Service: Service{Repository: r, Cache: f.Cache}, Authority: f, Accounts: f})
	expectStatus(t, boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[]`), 503)
}
func TestHTTPBoardRequestLimitsAndRouteConstraints(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{}
	h := f.HTTPHandler(r, nil)
	for _, body := range []string{`null`, `{}`, `[null]`, `[{"order":null}]`, `[{"is_enabled":null}]`, `[{"order":2147483648}]`, `[{"id":"invalid"}]`, `[{"kind":2}]`, `[{"order":0},{"order":0}]`, `[] []`, `[{"widget_key":"` + strings.Repeat("a", 257) + `"}]`, `[{"payload":[]}]`} {
		expectStatus(t, boardRequest(h, "PUT", "/api/accounts/me/board", "alice", body), 400)
	}
	expectStatus(t, boardRequest(h, "POST", "/api/accounts/me/board", "alice", `[]`), 405)
	expectStatus(t, boardRequest(h, "POST", "/api/admin/accounts/alice/board/items/not-uuid/payload", "admin", `{}`), 404)
	limited := NewHTTPHandler(HTTPOptions{Service: Service{Repository: r, Cache: f.Cache}, Authority: f, AccountCache: f.Cache, BodyLimit: 8})
	expectStatus(t, boardRequest(limited, "PUT", "/api/accounts/me/board", "alice", `[{"kind":0}]`), 400)
}
func TestDocumentedEnvelopeAndManifestValidation(t *testing.T) {
	n := DocumentedEnvelope{}
	for _, raw := range []string{`{"x":1}`, `{"x":{"label":"L"}}`, `{"x":{"value":1,"label":" "}}`, `{"x":{"value":1,"label":"L","format":4}}`, `{"x":{"value":1,"label":"L","extra":true}}`} {
		var p Payload
		_ = json.Unmarshal([]byte(raw), &p)
		if _, err := n.Normalize(context.Background(), p); !errors.Is(err, ErrInvalidOperation) {
			t.Fatal(raw, err)
		}
	}
	p := Payload{"x": map[string]any{"value": json.Number("900719925474099312345"), "label": "X", "format": "currency"}, "image": map[string]any{"value": "url", "label": "Image"}}
	got, err := n.Normalize(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p["x"].(map[string]any)["value"] = "mutated"
	if got["x"].(map[string]any)["value"] != json.Number("900719925474099312345") {
		t.Fatal(got)
	}
	w := Manifest{IsEnabled: true, PayloadType: "object", RequiredFields: []string{"required"}}
	if _, err := ValidateManifestPayload(context.Background(), w, Payload{}, nil); err != nil {
		t.Fatal("empty payload should bypass content validators", err)
	}
	if _, err := ValidateManifestPayload(context.Background(), w, got, n); err == nil {
		t.Fatal("missing required field")
	}
	w.RequiredFields = nil
	w.FieldTypes = []FieldType{{Name: "x", Type: "null"}}
	if _, err := ValidateManifestPayload(context.Background(), w, got, n); err == nil {
		t.Fatal("non-null matched null")
	}
	got["x"].(map[string]any)["value"] = nil
	w.FieldTypes[0].Type = "string"
	if _, err := ValidateManifestPayload(context.Background(), w, got, n); err != nil {
		t.Fatal("source matcher permits null for every type", err)
	}
	max := 1
	w.MaxPayloadBytes = &max
	if _, err := ValidateManifestPayload(context.Background(), w, got, n); err == nil {
		t.Fatal("size ignored")
	}
}
func TestSyntheticFixtureConfigurationAndGroupCacheRace(t *testing.T) {
	if _, err := NewSyntheticFixture(SyntheticConfig{}); err == nil {
		t.Fatal("implicit synthetic mode")
	}
	if _, err := DecodeSyntheticConfig(strings.NewReader(`{"synthetic":true,"unknown":true}`)); err == nil {
		t.Fatal("unknown fixture field")
	}
	f := httpFixture(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer alice")
	if _, err := f.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("real bearer accepted")
	}
	r.Header.Add("X-Synthetic-Principal", "alice")
	r.Header.Add("X-Synthetic-Principal", "admin")
	if _, err := f.Authenticate(r); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("ambiguous header")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				_, _ = f.Resolve(context.Background(), "alice")
				_ = f.Cache.InvalidateGroup(context.Background(), "account:"+owner)
			}
		}()
	}
	wg.Wait()
}

type failingAccountPurge struct{}

func (failingAccountPurge) PurgeAccount(context.Context, Account) error {
	return errors.New("synthetic account cache failure")
}
func TestHTTPBoardPostCommitCacheFailureIsNotRollback(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{}
	h := NewHTTPHandler(HTTPOptions{Service: Service{Repository: r, Catalog: f, Normalizer: DocumentedEnvelope{}, Cache: f.Cache}, Authority: f, Accounts: f, AccountCache: failingAccountPurge{}})
	w := boardRequest(h, "PUT", "/api/accounts/me/board", "alice", `[{"id":"`+itemID+`","payload":{"saved":true}}]`)
	expectStatus(t, w, 500)
	if r.replacements != 1 || len(r.items) != 1 || r.items[0].Payload["saved"] != true {
		t.Fatal("post-commit purge failure disguised or reverted persistence", r.items)
	}
	if strings.Contains(w.Body.String(), "synthetic account cache failure") {
		t.Fatal("cache details leaked")
	}
}

func TestSyntheticConfigSizeIncludesTrailingData(t *testing.T) {
	prefix := `{"synthetic":true}`
	exact := prefix + strings.Repeat(" ", (2<<20)-len(prefix))
	if _, err := DecodeSyntheticConfig(strings.NewReader(exact)); err != nil {
		t.Fatal("exact-limit fixture rejected", err)
	}
	for _, tail := range []string{" ", `{}`} {
		if _, err := DecodeSyntheticConfig(strings.NewReader(exact + tail)); err == nil {
			t.Fatal("over-limit trailing data accepted")
		}
	}
}

func TestHTTPBoardHeadIsNotImplicitGetAndNeverWrites(t *testing.T) {
	f := httpFixture(t)
	r := &fakeRepo{items: []Item{{ID: itemID, AccountID: owner, Payload: Payload{}}}}
	d := &deleteRecorder{}
	h := f.HTTPHandler(r, d)
	for _, tc := range []struct{ path, principal, allow string }{
		{"/api/accounts/me/board", "alice", "GET, PUT"},
		{"/api/accounts/alice/board", "", "GET"},
		{"/api/admin/accounts/alice/board", "reader", "GET, PUT"},
		{"/api/private/apps/" + app + "/board/widgets", "app", "GET"},
	} {
		request := httptest.NewRequest(http.MethodHead, tc.path, nil)
		if tc.principal != "" {
			request.Header.Set("X-Synthetic-Principal", tc.principal)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request)
		if w.Code != 405 || w.Header().Get("Allow") != tc.allow || w.Body.Len() != 0 || request.Method != http.MethodHead {
			t.Fatal(tc.path, w.Code, w.Body.String(), request.Method)
		}
	}
	if len(r.items) != 1 || r.items[0].ID != itemID || r.saves != 0 || d.calls != 0 || len(f.Cache.groups) != 0 {
		t.Fatal("HEAD changed board/cache state", r.items, r.saves, d.calls, f.Cache.groups)
	}
}
