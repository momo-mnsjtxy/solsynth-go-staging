package contract_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/watchstore"
	"solsynth.local/go-migration/ports/sphere"
)

// This is a trusted synthetic adapter, not a production credential validator.
type fixtureWatchAuth struct {
	account string
	denied  bool
}

func (a fixtureWatchAuth) AccountID(*http.Request) (string, error) { return a.account, nil }
func (a fixtureWatchAuth) CanManagePostSubscriptions(*http.Request, string) error {
	if a.denied {
		return errors.New("denied")
	}
	return nil
}

func TestPersistentWatchHTTPContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.json")
	newHandler := func(auth sphere.WatchAuth) http.Handler {
		store, err := watchstore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return sphere.NewWatchHandler(&sphere.WatchService{Repository: store}, auth)
	}
	call := func(handler http.Handler, method, body string, want int) []sphere.WatchPreference {
		t.Helper()
		r := httptest.NewRequest(method, "/api/posts/watch", strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status=%d want=%d: %s", method, w.Code, want, w.Body.String())
		}
		if want != 200 {
			return nil
		}
		if strings.Contains(w.Body.String(), "account_id") {
			t.Fatal("account identity leaked in response")
		}
		var rows []sphere.WatchPreference
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 || rows[0].Source != sphere.WatchBookmark || rows[1].Source != sphere.WatchReaction || rows[2].Source != sphere.WatchReply {
			t.Fatalf("unexpected source rows: %+v", rows)
		}
		return rows
	}
	a := newHandler(fixtureWatchAuth{account: "synthetic-a"})
	initial := call(a, "GET", "", 200)
	if !initial[0].NotifyReplies || initial[1].NotifyReplies || !initial[2].NotifyEdits {
		t.Fatal("incorrect defaults")
	}
	changed := call(a, "PUT", `{"bookmark":{"replies":false},"reaction":{"chains":false},"reply":{"edits":false}}`, 200)
	if changed[0].NotifyReplies || changed[1].NotifyChains || changed[2].NotifyEdits {
		t.Fatal("updates not applied")
	}
	// Recreate both the store and service to exercise persisted bytes, not memory.
	reopened := newHandler(fixtureWatchAuth{account: "synthetic-a"})
	rows := call(reopened, "GET", "", 200)
	if rows[0].NotifyReplies || rows[1].NotifyChains || rows[2].NotifyEdits {
		t.Fatal("updates lost on reopen")
	}
	rows = call(reopened, "PUT", `{"bookmark":{"replies":null},"reaction":null}`, 200)
	if rows[0].NotifyReplies || rows[1].NotifyChains || !rows[0].NotifyReactions {
		t.Fatal("null/omitted values changed state")
	}
	other := newHandler(fixtureWatchAuth{account: "synthetic-b"})
	rows = call(other, "GET", "", 200)
	if !rows[0].NotifyReplies || !rows[1].NotifyChains || !rows[2].NotifyEdits {
		t.Fatal("cross-account preference leakage")
	}
	call(newHandler(fixtureWatchAuth{}), "GET", "", 401)
	call(newHandler(fixtureWatchAuth{}), "PUT", `{}`, 401)
	call(newHandler(fixtureWatchAuth{account: "synthetic-a", denied: true}), "PUT", `{"bookmark":{"replies":true}}`, 403)
	if call(newHandler(fixtureWatchAuth{account: "synthetic-a"}), "GET", "", 200)[0].NotifyReplies {
		t.Fatal("denied write persisted")
	}
	call(newHandler(nil), "GET", "", 503)
	call(reopened, "PUT", `null`, 400)
	call(reopened, "PUT", `{} {}`, 400)
}
