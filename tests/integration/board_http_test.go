// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/ports/passport/board"
)

func boardHTTPConfig(prefix int) board.SyntheticConfig {
	id := func(n int) string { return watcherID(prefix, n) }
	return board.SyntheticConfig{Synthetic: true, AllowPublicReads: true, Accounts: []board.Account{{ID: id(1), Name: "synthetic-alice"}, {ID: id(2), Name: "synthetic-bob"}}, Principals: []board.SyntheticPrincipal{{Name: "owner", AccountID: id(1), Permissions: []string{board.SelfManage}}, {Name: "admin", AccountID: id(2), Permissions: []string{board.AccountsView, board.BoardManage}}, {Name: "app", AppID: id(3)}}, Apps: []board.SyntheticApp{{ID: id(3), BoardCapability: true, ConsentingAccounts: []string{id(1)}, Widgets: []board.Manifest{{Key: "Status", IsEnabled: true, PayloadType: "object", RequiredFields: []string{"title"}, FieldTypes: []board.FieldType{{Name: "title", Type: "string"}}}}}}}
}
func boardHTTPCall(t *testing.T, h http.Handler, method, path, principal, body string, status int) []byte {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if principal != "" {
		r.Header.Set("X-Synthetic-Principal", principal)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s wanted %d got %d: %s", method, path, status, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}
func boardHTTPHandler(t *testing.T, prefix int) (http.Handler, *board.SyntheticFixture, *boardpg.Repository) {
	t.Helper()
	r := boardCandidate(database(t))
	f, err := board.NewSyntheticFixture(boardHTTPConfig(prefix))
	if err != nil {
		t.Fatal(err)
	}
	return f.HTTPHandler(r, r), f, r
}
func TestPGBoardHTTPFamily(t *testing.T) {
	h, f, r := boardHTTPHandler(t, 0xbb000000)
	id := func(n int) string { return watcherID(0xbb000000, n) }
	body := fmt.Sprintf(`[{"id":%q,"order":1,"kind":1,"custom_app_id":%q,"custom_app_widget_key":"status","payload":{"ignored":true}},{"id":%q,"order":0,"widget_key":"memo","payload":{"content":"client raw text"}}]`, id(10), id(3), id(11))
	boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", body, 200)
	boardHTTPCall(t, h, "POST", "/api/private/apps/"+id(3)+"/board/payload", "app", fmt.Sprintf(`{"account_id":%q,"widget_key":"status","payload":{"title":{"value":"app-persisted","label":"Title"}}}`, id(1)), 200)
	boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", body, 200)
	raw := boardHTTPCall(t, h, "GET", "/api/accounts/synthetic-alice/board", "", "", 200)
	if !bytes.Contains(raw, []byte("app-persisted")) {
		t.Fatal(string(raw))
	}
	// Independent SQL pool sees the app-owned payload after a user layout replace.
	got, err := boardCandidate(database(t)).FindByID(context.Background(), id(1), id(10))
	if err != nil || got.Payload["title"].(map[string]any)["value"] != "app-persisted" {
		t.Fatal(got, err)
	}
	boardHTTPCall(t, h, "POST", "/api/admin/accounts/synthetic-alice/board/items/"+id(11)+"/payload", "admin", `{"payload":{"counter":{"value":900719925474099312345,"label":"Counter"}}}`, 200)
	got, err = r.FindByID(context.Background(), id(1), id(11))
	if err != nil || got.Payload["counter"].(map[string]any)["value"] != json.Number("900719925474099312345") {
		t.Fatal(got, err)
	}
	boardHTTPCall(t, h, "GET", "/api/admin/accounts/synthetic-alice/board", "owner", "", 403)
	boardHTTPCall(t, h, "POST", "/api/private/apps/"+id(3)+"/board/payload", "app", fmt.Sprintf(`{"account_id":%q,"widget_key":"status","payload":{}}`, id(2)), 403)
	_, _ = f.Resolve(context.Background(), "synthetic-alice")
	boardHTTPCall(t, h, "DELETE", "/api/admin/accounts/synthetic-bob/board/items/"+id(11), "admin", "", 404)
	boardHTTPCall(t, h, "DELETE", "/api/admin/accounts/synthetic-alice/board/items/"+id(11), "admin", "", 204)
	if _, ok := f.Cache.Get("account:"+id(1), "synthetic-alice"); !ok {
		t.Fatal("delete invented an account-cache purge")
	}
	boardHTTPCall(t, h, "DELETE", "/api/admin/accounts/synthetic-alice/board/items/"+id(11), "admin", "", 404)
}
func TestPGBoardHTTPValidationRollback(t *testing.T) {
	h, _, r := boardHTTPHandler(t, 0xbb100000)
	id := func(n int) string { return watcherID(0xbb100000, n) }
	body := fmt.Sprintf(`[{"id":%q,"kind":1,"custom_app_id":%q,"custom_app_widget_key":"Status"}]`, id(10), id(3))
	boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", body, 200)
	boardHTTPCall(t, h, "POST", "/api/admin/accounts/synthetic-alice/board/items/"+id(10)+"/payload", "admin", `{"payload":{"title":{"value":false,"label":"Title"}}}`, 400)
	boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", `[{"order":0},{"order":0}]`, 400)
	// A retained tombstone participates in the original unfiltered unique index.
	db := database(t)
	query(t, db, `INSERT INTO account_board_items(id,account_id,"order",kind,is_enabled,payload,created_at,updated_at,deleted_at) VALUES($1,$2,9,0,true,'{}',now(),now(),now())`, id(12), id(1))
	boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", `[{"order":9}]`, 500)
	got, err := r.List(context.Background(), id(1))
	if err != nil || len(got) != 1 || got[0].ID != id(10) || len(got[0].Payload) != 0 {
		t.Fatal("rejected HTTP mutation changed stored board", got, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM account_board_items WHERE id=$1 AND deleted_at IS NOT NULL`, id(12)).Scan(&count); err != nil || count != 1 {
		t.Fatal("tombstone lost", count, err)
	}
}
func TestPGBoardHTTPRestartPersistence(t *testing.T) {
	h, _, _ := boardHTTPHandler(t, 0xbb300000)
	id := func(n int) string { return watcherID(0xbb300000, n) }
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		body := fmt.Sprintf(`[{"id":%q,"kind":1,"custom_app_id":%q,"custom_app_widget_key":"Status"}]`, id(10), id(3))
		boardHTTPCall(t, h, "PUT", "/api/accounts/me/board", "owner", body, 200)
		body = fmt.Sprintf(`{"account_id":%q,"widget_key":"Status","payload":{"title":{"value":"restart-sentinel","label":"Title"}}}`, id(1))
		boardHTTPCall(t, h, "POST", "/api/private/apps/"+id(3)+"/board/payload", "app", body, 200)
	case "after":
	default:
		t.Fatal("missing restart phase")
	}
	raw := boardHTTPCall(t, h, "GET", "/api/accounts/synthetic-alice/board", "", "", 200)
	var items []board.Item
	if err := json.Unmarshal(raw, &items); err != nil || len(items) != 1 || items[0].ID != id(10) || !bytes.Contains(raw, []byte("restart-sentinel")) {
		t.Fatal("HTTP persistence across database restart", string(raw), err)
	}
}

// TestPGBoardHTTPProcessRestart uses an actual compiled host and real HTTP/PG.
// The integration runner must build cmd/passport-board and supply its absolute
// path through PASSPORT_BOARD_BINARY. It never builds or listens in ordinary tests.
func TestPGBoardHTTPProcessRestart(t *testing.T) {
	database(t)
	config := boardHTTPConfig(0xbb200000)
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "synthetic-board.json")
	if err = os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	start := func(synthetic bool) *domainProcess {
		t.Helper()
		var args []string
		if synthetic {
			args = []string{"--synthetic-fixture=" + fixturePath, "--postgres-dsn-env=PG_TEST_DSN"}
		}
		return startDomainProcess(t, "PASSPORT_BOARD_BINARY", "Passport board listening on ", args...)
	}
	headers := map[string]string{"X-Synthetic-Principal": "owner"}
	process := start(false)
	process.call(t, "GET", "/api/accounts/me/board", "", 503, headers)
	process.stop()
	process = start(true)
	id := watcherID(0xbb200000, 10)
	body := fmt.Sprintf(`[{"id":%q,"widget_key":"memo","payload":{"content":"process-persisted"}}]`, id)
	process.call(t, "PUT", "/api/accounts/me/board", body, 200, headers)
	process.stop()
	process = start(true)
	result, _ := process.call(t, "GET", "/api/accounts/me/board", "", 200, headers)
	if !bytes.Contains(result, []byte("process-persisted")) {
		t.Fatal(string(result))
	}
	process.stop()
}
