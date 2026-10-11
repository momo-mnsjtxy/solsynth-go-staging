// SPDX-License-Identifier: AGPL-3.0-only
package pets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/authority"
)

const httpTestActor = "10000000-0000-4000-8000-000000000001"

type httpTestAuthority struct {
	err   error
	calls int
}

func (a *httpTestAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	a.calls++
	if a.err != nil {
		return authority.Principal{}, a.err
	}
	return authority.Principal{Account: authority.Account{ID: httpTestActor}}, nil
}

type httpTestRepository struct {
	state       State
	err         error
	getCalls    int
	mutateCalls int
	actor       string
	command     Command
}

func (r *httpTestRepository) Get(_ context.Context, actor string) (State, error) {
	r.getCalls++
	r.actor = actor
	return clone(r.state), r.err
}

func (r *httpTestRepository) Mutate(_ context.Context, actor string, command Command) (State, error) {
	r.mutateCalls++
	r.actor = actor
	r.command = command
	return clone(r.state), r.err
}

func httpTestService(repo *httpTestRepository, auth *httpTestAuthority, bound *bool) http.Handler {
	return (&Service{
		Repository: repo,
		Authority:  auth,
		Bind: func(ctx context.Context, principal authority.Principal) context.Context {
			if principal.Account.ID == httpTestActor {
				*bound = true
			}
			return ctx
		},
	}).Handler()
}

func petRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestHTTPRequiresAuthenticationAndBindsPrincipal(t *testing.T) {
	repo := &httpTestRepository{state: adopt(t)}
	denied := &httpTestAuthority{err: authority.ErrForbidden}
	bound := false
	w := petRequest(httpTestService(repo, denied, &bound), http.MethodGet, "/api/pets/catalog", "")
	if w.Code != http.StatusForbidden || denied.calls != 1 || repo.getCalls != 0 || bound {
		t.Fatalf("unauthenticated catalog: status=%d auth=%d gets=%d bound=%v body=%s", w.Code, denied.calls, repo.getCalls, bound, w.Body.String())
	}

	auth := &httpTestAuthority{}
	w = petRequest(httpTestService(repo, auth, &bound), http.MethodGet, "/api/pets/me", "")
	if w.Code != http.StatusOK || auth.calls != 1 || repo.getCalls != 1 || repo.actor != httpTestActor || !bound {
		t.Fatalf("authenticated state: status=%d auth=%d gets=%d actor=%q bound=%v body=%s", w.Code, auth.calls, repo.getCalls, repo.actor, bound, w.Body.String())
	}
}

func TestHTTPRejectsAuthoritySpoofingBeforeAuthentication(t *testing.T) {
	repo := &httpTestRepository{state: adopt(t)}
	auth := &httpTestAuthority{}
	bound := false
	h := httpTestService(repo, auth, &bound)
	for _, header := range []string{"X-Account-ID", "X-User-ID", "X-Role", "Proxy-Authorization", "X-Test-Actor", "X-Synthetic-User"} {
		req := httptest.NewRequest(http.MethodGet, "/api/pets/me", nil)
		req.Header[header] = []string{"spoofed"}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status=%d body=%s", header, w.Code, w.Body.String())
		}
	}
	if auth.calls != 0 || repo.getCalls != 0 || bound {
		t.Fatalf("spoofed requests reached authority/domain: auth=%d gets=%d bound=%v", auth.calls, repo.getCalls, bound)
	}
}

func TestHTTPDecoderRequiresExactUniqueFields(t *testing.T) {
	base := `"request_id":"request-00000000-http","expected_version":1`
	cases := []string{
		`{` + base + `,"Kind":"feed"}`,
		`{` + base + `,"kind":"feed","Kind":"play"}`,
		`{"Request_ID":"request-00000000-http","expected_version":1,"kind":"feed"}`,
		`{` + base + `,"kind":"feed","unknown":1}`,
		`{` + base + `,"kind":null}`,
		`{` + base + `,"kind":"feed"}{}`,
		`{` + base + `,"kind":"feed","kind":"play"}`,
	}
	for i, body := range cases {
		repo := &httpTestRepository{state: adopt(t)}
		auth := &httpTestAuthority{}
		bound := false
		w := petRequest(httpTestService(repo, auth, &bound), http.MethodPost, "/api/pets/me/commands", body)
		if w.Code != http.StatusBadRequest || repo.mutateCalls != 0 {
			t.Errorf("case %d: status=%d mutations=%d body=%s", i, w.Code, repo.mutateCalls, w.Body.String())
		}
	}
}

func TestHTTPDecoderBoundsEncodingAndContentType(t *testing.T) {
	valid := `{"request_id":"request-00000000-http","expected_version":1,"kind":"rest"}`
	cases := []struct {
		body        string
		contentType string
	}{
		{valid, "text/plain"},
		{"{\xff}", "application/json"},
		{valid + strings.Repeat(" ", 4096), "application/json"},
		{"", "application/json"},
	}
	for i, tc := range cases {
		repo := &httpTestRepository{state: adopt(t)}
		auth := &httpTestAuthority{}
		bound := false
		req := httptest.NewRequest(http.MethodPost, "/api/pets/me/commands", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		w := httptest.NewRecorder()
		httpTestService(repo, auth, &bound).ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || repo.mutateCalls != 0 {
			t.Errorf("case %d: status=%d mutations=%d body=%s", i, w.Code, repo.mutateCalls, w.Body.String())
		}
	}

	repo := &httpTestRepository{state: adopt(t)}
	auth := &httpTestAuthority{}
	bound := false
	w := petRequest(httpTestService(repo, auth, &bound), http.MethodPost, "/api/pets/me/commands", valid)
	if w.Code != http.StatusOK || repo.mutateCalls != 1 || repo.command.Kind != "rest" {
		t.Fatalf("valid unified command: status=%d mutations=%d command=%#v body=%s", w.Code, repo.mutateCalls, repo.command, w.Body.String())
	}
}

func TestHTTPDedicatedRoutesSelectServerControlledCommandFields(t *testing.T) {
	cases := []struct {
		method string
		path   string
		body   string
		want   Command
	}{
		{http.MethodPost, "/api/pets/me/adopt", `{"request_id":"request-00000000-adopt","expected_version":0,"name":"Mochi","species":"foxlet"}`, Command{RequestID: "request-00000000-adopt", Kind: "adopt", Name: "Mochi", Species: "foxlet"}},
		{http.MethodPatch, "/api/pets/me", `{"request_id":"request-00000000-rename","expected_version":1,"name":"Miso"}`, Command{RequestID: "request-00000000-rename", ExpectedVersion: 1, Kind: "rename", Name: "Miso"}},
		{http.MethodPost, "/api/pets/me/actions", `{"request_id":"request-00000000-action","expected_version":1,"kind":"rest"}`, Command{RequestID: "request-00000000-action", ExpectedVersion: 1, Kind: "rest"}},
		{http.MethodPost, "/api/pets/me/purchases", `{"request_id":"request-00000000-purchase","expected_version":1,"item_id":"snack"}`, Command{RequestID: "request-00000000-purchase", ExpectedVersion: 1, Kind: "buy", ItemID: "snack"}},
		{http.MethodPut, "/api/pets/me/equipment/head", `{"request_id":"request-00000000-equip","expected_version":1,"item_id":"leaf_hat"}`, Command{RequestID: "request-00000000-equip", ExpectedVersion: 1, Kind: "equip", ItemID: "leaf_hat", Slot: "head"}},
		{http.MethodDelete, "/api/pets/me/equipment/neck", `{"request_id":"request-00000000-unequip","expected_version":1}`, Command{RequestID: "request-00000000-unequip", ExpectedVersion: 1, Kind: "equip", Slot: "neck"}},
		{http.MethodPost, "/api/pets/me/tasks/breakfast/claim", `{"request_id":"request-00000000-claim","expected_version":1}`, Command{RequestID: "request-00000000-claim", ExpectedVersion: 1, Kind: "claim", TaskID: "breakfast"}},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			repo := &httpTestRepository{state: adopt(t)}
			auth := &httpTestAuthority{}
			bound := false
			w := petRequest(httpTestService(repo, auth, &bound), tc.method, tc.path, tc.body)
			if w.Code != http.StatusOK || repo.mutateCalls != 1 || repo.actor != httpTestActor || repo.command != tc.want || !bound {
				t.Fatalf("status=%d calls=%d actor=%q\n got %#v\nwant %#v\nbound=%v body=%s", w.Code, repo.mutateCalls, repo.actor, repo.command, tc.want, bound, w.Body.String())
			}
		})
	}
}

func TestHTTPDedicatedRoutesRejectClientSelectedFields(t *testing.T) {
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/pets/me/adopt", `{"request_id":"request-00000000-adopt","expected_version":0,"kind":"adopt","name":"Mochi","species":"foxlet"}`},
		{http.MethodPut, "/api/pets/me/equipment/head", `{"request_id":"request-00000000-equip","expected_version":1,"item_id":"leaf_hat","slot":"head"}`},
		{http.MethodDelete, "/api/pets/me/equipment/head", `{"request_id":"request-00000000-remove","expected_version":1,"item_id":"leaf_hat"}`},
		{http.MethodPost, "/api/pets/me/tasks/breakfast/claim", `{"request_id":"request-00000000-claim","expected_version":1,"task_id":"breakfast"}`},
		{http.MethodPost, "/api/pets/me/actions", `{"request_id":"request-00000000-buy","expected_version":1,"kind":"buy","item_id":"snack"}`},
	}
	for _, tc := range cases {
		repo := &httpTestRepository{state: adopt(t)}
		auth := &httpTestAuthority{}
		bound := false
		w := petRequest(httpTestService(repo, auth, &bound), tc.method, tc.path, tc.body)
		if w.Code != http.StatusBadRequest || repo.mutateCalls != 0 {
			t.Errorf("%s %s: status=%d calls=%d body=%s", tc.method, tc.path, w.Code, repo.mutateCalls, w.Body.String())
		}
	}
}

func TestHTTPReadProjectionsAndCatalogDoNotLeakMutableDefinitions(t *testing.T) {
	state := adopt(t)
	state.Inventory["blue_scarf"] = 1
	state.Equipment["neck"] = "blue_scarf"
	state.Daily.Care["feed"] = 1
	repo := &httpTestRepository{state: state}
	auth := &httpTestAuthority{}
	bound := false
	h := httpTestService(repo, auth, &bound)

	for _, target := range []string{"/api/pets/catalog", "/api/pets/shop", "/api/pets/me/inventory", "/api/pets/me/equipment", "/api/pets/me/tasks"} {
		w := petRequest(h, http.MethodGet, target, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "application/json") || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: status=%d headers=%v body=%s", target, w.Code, w.Header(), w.Body.String())
		}
		var decoded any
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Errorf("%s returned invalid JSON: %v", target, err)
		}
	}
	if repo.getCalls != 3 {
		t.Fatalf("catalog/shop should not read account state; gets=%d", repo.getCalls)
	}
}

func TestHTTPRejectsQueryBodiesMethodsAndUnknownRoutes(t *testing.T) {
	cases := []struct {
		method string
		target string
		body   string
		status int
	}{
		{http.MethodGet, "/api/pets/me?account=" + httpTestActor, "", http.StatusBadRequest},
		{http.MethodGet, "/api/pets/me", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/api/pets/catalog", `{}`, http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/pets/me/tasks/unknown/claim", "", http.StatusNotFound},
		{http.MethodGet, "/api/pets/me/equipment/body", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		repo := &httpTestRepository{state: adopt(t)}
		auth := &httpTestAuthority{}
		bound := false
		w := petRequest(httpTestService(repo, auth, &bound), tc.method, tc.target, tc.body)
		if w.Code != tc.status || repo.getCalls != 0 || repo.mutateCalls != 0 {
			t.Errorf("%s %s: status=%d want=%d gets=%d mutations=%d body=%s", tc.method, tc.target, w.Code, tc.status, repo.getCalls, repo.mutateCalls, w.Body.String())
		}
	}
}

func TestHTTPMapsDomainAndUnknownFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrConflict, http.StatusConflict, "PET_CONFLICT"},
		{ErrCooldown, http.StatusTooManyRequests, "PET_COOLDOWN"},
		{errors.New("database detail that must not leak"), http.StatusServiceUnavailable, "PET_UNAVAILABLE"},
	} {
		repo := &httpTestRepository{state: adopt(t), err: tc.err}
		auth := &httpTestAuthority{}
		bound := false
		w := petRequest(httpTestService(repo, auth, &bound), http.MethodGet, "/api/pets/me", "")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "database detail") {
			t.Errorf("error=%v status=%d body=%s", tc.err, w.Code, w.Body.String())
		}
	}
}
