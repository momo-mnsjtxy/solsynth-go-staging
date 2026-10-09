// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"solsynth.local/go-migration/ports/authority"
)

const actor = "10000000-0000-0000-0000-000000000001"

type testIdentity struct{ err error }

func (i testIdentity) Account(*http.Request) (string, error) { return actor, i.err }

type testRepo struct {
	calls        int
	account, app string
	id           string
	action       string
}

func (r *testRepo) List(_ context.Context, a, app string, take, offset int) ([]Notification, int64, error) {
	r.calls++
	r.account, r.app = a, app
	return []Notification{}, 12, nil
}
func (r *testRepo) Unread(_ context.Context, a, app string) (int64, error) {
	r.calls++
	r.account, r.app = a, app
	return 3, nil
}
func (r *testRepo) MarkRead(_ context.Context, a, app, id string) error {
	r.calls++
	r.account, r.app, r.id, r.action = a, app, id, "read"
	return nil
}
func (r *testRepo) Dismiss(_ context.Context, a, app, id string) error {
	r.calls++
	r.account, r.app, r.id, r.action = a, app, id, "dismiss"
	return nil
}
func TestHTTPInboxScopeAndRoutes(t *testing.T) {
	repo := &testRepo{}
	h := HTTP{Repository: repo, Identity: testIdentity{}, App: DefaultApp}
	for _, path := range []string{"/api/notifications", "/metoer/notifications?offset=0&take=5"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("X-Total") != "12" || w.Body.String() != "[]\n" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/notifications?app=foreign", "/api/notifications?take=0", "/api/notifications?take=101", "/api/notifications?offset=-1", "/api/notifications?take=1&take=2", "/api/notifications?tk=secret", "/api/notifications?account_id=" + actor, "/api/notifications?app=%zz"} {
		before := repo.calls
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 || repo.calls != before {
			t.Fatal(path, w.Code, repo.calls)
		}
	}
	for _, action := range []string{"read", "dismiss"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/notifications/"+actor+"/"+action, nil))
		if w.Code != 204 || repo.action != action || repo.account != actor || repo.app != DefaultApp {
			t.Fatal(action, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/notifications/all/read", nil))
	if w.Code != 204 || repo.id != "" {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/notifications/count", nil))
	if w.Code != 200 || w.Body.String() != "3\n" {
		t.Fatal(w.Code, w.Body.String())
	}
	h.Identity = testIdentity{authority.ErrRevoked}
	before := repo.calls
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/notifications", nil))
	if w.Code != 401 || repo.calls != before {
		t.Fatal("revoked request", w.Code)
	}
}
func TestHTTPDoesNotExposeDependencyError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, errors.New("credential=never-echo"))
	if w.Code != 503 || w.Body.String() != "{\"code\":\"NOTIFICATION_UNAVAILABLE\"}\n" {
		t.Fatal(w.Body.String())
	}
}
