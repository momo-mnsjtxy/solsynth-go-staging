package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/sphere"
)

type bindingRepository struct {
	loads, saves int
	rows         []sphere.WatchPreference
}

func (r *bindingRepository) LoadPreferences(context.Context, []string) ([]sphere.WatchPreference, error) {
	r.loads++
	return append([]sphere.WatchPreference{}, r.rows...), nil
}
func (r *bindingRepository) SavePreferences(_ context.Context, _ string, rows []sphere.WatchPreference) error {
	r.saves++
	r.rows = append([]sphere.WatchPreference{}, rows...)
	return nil
}

type bindingAuth struct {
	anonymous, denied bool
	permissions       int
}

func (a *bindingAuth) AccountID(*http.Request) (string, error) {
	if a.anonymous {
		return "", errors.New("synthetic missing identity")
	}
	return "synthetic-binding-account", nil
}
func (a *bindingAuth) CanManagePostSubscriptions(*http.Request, string) error {
	a.permissions++
	if a.denied {
		return errors.New("synthetic denial")
	}
	return nil
}

func TestWatchHTTPBindingSubset(t *testing.T) {
	cases := []struct {
		name, body      string
		status          int
		bookmarkReplies bool
		saves           int
	}{
		{"case insensitive", `{"BOOKMARK":{"REPLIES":false}}`, 200, false, 1},
		{"null group", `{"bookmark":null}`, 200, true, 0},
		{"null bool", `{"bookmark":{"replies":null}}`, 200, true, 1},
		{"empty group", `{"bookmark":{}}`, 200, true, 1},
		{"empty object", `{}`, 200, true, 0},
		{"unknown fields ignored", `{"unexpected":true,"bookmark":{"other":false}}`, 200, true, 1},
		{"false is update", `{"bookmark":{"replies":false}}`, 200, false, 1},
		{"string bool", `{"bookmark":{"replies":"false"}}`, 400, false, 0},
		{"number bool", `{"bookmark":{"replies":0}}`, 400, false, 0},
		{"object bool", `{"bookmark":{"replies":{}}}`, 400, false, 0},
		{"array bool", `{"bookmark":{"replies":[]}}`, 400, false, 0},
		{"array group", `{"bookmark":[]}`, 400, false, 0},
		{"scalar group", `{"bookmark":true}`, 400, false, 0},
		{"array root", `[]`, 400, false, 0},
		{"null root", `null`, 400, false, 0},
		{"empty body", "", 400, false, 0},
		{"trailing value", `{} {}`, 400, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repository := &bindingRepository{}
			auth := &bindingAuth{}
			r := httptest.NewRequest(http.MethodPut, "/api/posts/watch", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			sphere.NewWatchHandler(&sphere.WatchService{Repository: repository}, auth).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if repository.saves != tc.saves {
				t.Fatalf("saves %d, want %d", repository.saves, tc.saves)
			}
			if tc.status != 200 {
				if repository.loads != 0 {
					t.Fatal("invalid body reached storage")
				}
				return
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 {
				t.Fatalf("rows %d", len(rows))
			}
			for i, source := range []string{"bookmark", "reaction", "reply"} {
				if len(rows[i]) != 6 {
					t.Fatalf("unexpected response fields %v", rows[i])
				}
				var actual string
				if err := json.Unmarshal(rows[i]["source"], &actual); err != nil || actual != source {
					t.Fatalf("source %s, %v", actual, err)
				}
				for _, key := range []string{"notify_reactions", "notify_replies", "notify_chains", "notify_forwards", "notify_edits"} {
					var value bool
					raw, ok := rows[i][key]
					if !ok || string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
						t.Fatalf("nonboolean %s", key)
					}
				}
			}
			var replies bool
			if err := json.Unmarshal(rows[0]["notify_replies"], &replies); err != nil || replies != tc.bookmarkReplies {
				t.Fatalf("bookmark replies %v: %v", replies, err)
			}
		})
	}
}

func TestWatchHTTPAuthorizationBeforeBody(t *testing.T) {
	for _, anonymous := range []bool{true, false} {
		repository := &bindingRepository{}
		auth := &bindingAuth{anonymous: anonymous, denied: true}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/posts/watch", strings.NewReader("{invalid"))
		sphere.NewWatchHandler(&sphere.WatchService{Repository: repository}, auth).ServeHTTP(w, r)
		want := 403
		permissionCalls := 1
		if anonymous {
			want = 401
			permissionCalls = 0
		}
		if w.Code != want || repository.loads != 0 || repository.saves != 0 || auth.permissions != permissionCalls {
			t.Fatalf("status=%d loads=%d saves=%d permission=%d", w.Code, repository.loads, repository.saves, auth.permissions)
		}
	}
	repository := &bindingRepository{}
	auth := &bindingAuth{denied: true}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/posts/watch", nil)
	sphere.NewWatchHandler(&sphere.WatchService{Repository: repository}, auth).ServeHTTP(w, r)
	if w.Code != 200 || auth.permissions != 0 {
		t.Fatal("GET incorrectly requested write permission")
	}
}
