package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/developpg"
	"solsynth.local/go-migration/ports/develop"
)

// All statuses, data, and active-row policy in this file are synthetic fixtures.
// These tests establish candidate SQL/HTTP behavior, not shared policy parity.
const developProductionFixture int32 = 42

func developHandler(db *sql.DB) http.Handler {
	return develop.NewSyntheticCompatibilityHandler(developpg.NewSyntheticCompatibilityRepository(db, developProductionFixture))
}
func developRequest(handler http.Handler, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}
func developApps(t *testing.T, handler http.Handler, params url.Values, total string) []develop.AppDiscovery {
	t.Helper()
	response := developRequest(handler, "/api/apps?"+params.Encode())
	if response.Code != http.StatusOK || response.Header().Get("X-Total") != total {
		t.Fatalf("response code=%d total=%q body=%s", response.Code, response.Header().Get("X-Total"), response.Body.String())
	}
	var apps []develop.AppDiscovery
	if err := json.Unmarshal(response.Body.Bytes(), &apps); err != nil {
		t.Fatal(err)
	}
	if apps == nil {
		t.Fatal("array serialized as null")
	}
	return apps
}
func developInsert(t *testing.T, db *sql.DB, id, slug, name string, description *string, status int32, deleted bool) {
	t.Helper()
	query(t, db, `INSERT INTO custom_apps(id,slug,name,description,status,deleted_at) VALUES($1,$2,$3,$4,$5,CASE WHEN $6 THEN now() ELSE NULL END)`, id, slug, name, description, status, deleted)
}

func TestPGDevelopDiscoveryPagingAndCounts(t *testing.T) {
	db := database(t)
	handler := developHandler(db)
	id := func(n int) string { return watcherID(0x93000000, n) }
	developInsert(t, db, id(1), "develop-page-a", "Develop page same", nil, 42, false)
	developInsert(t, db, id(2), "develop-page-b", "Develop page same", stringPointer(""), 42, false)
	developInsert(t, db, id(3), "develop-page-c", "Develop page Z", stringPointer("description"), 42, false)
	developInsert(t, db, id(4), "develop-page-inactive", "Develop page inactive", nil, 41, false)
	developInsert(t, db, id(5), "develop-page-deleted", "Develop page deleted", nil, 42, true)
	for n := 10; n < 13; n++ {
		query(t, db, `INSERT INTO app_products(id,app_id,deleted_at) VALUES($1,$2,CASE WHEN $3 THEN now() ELSE NULL END)`, id(n), id(1), n == 12)
	}
	for n := 20; n < 23; n++ {
		query(t, db, `INSERT INTO board_widgets(id,app_id,is_enabled,deleted_at) VALUES($1,$2,$3,CASE WHEN $4 THEN now() ELSE NULL END)`, id(n), id(1), n == 20, n == 22)
	}
	query(t, db, `INSERT INTO app_products(id,app_id) VALUES($1,$2)`, id(30), id(2))
	params := url.Values{"search": {"develop-page"}}
	apps := developApps(t, handler, params, "3")
	// PostgreSQL fixture cluster uses C collation: uppercase Z precedes lowercase s.
	if len(apps) != 3 || apps[0].ID != id(3) || apps[1].ID != id(1) || apps[2].ID != id(2) {
		t.Fatalf("name/slug ordering: %+v", apps)
	}
	if apps[1].Description != nil || apps[2].Description == nil || *apps[2].Description != "" || apps[1].ProductsCount != 2 || apps[1].WidgetsCount != 2 || apps[2].ProductsCount != 1 || apps[2].WidgetsCount != 0 {
		t.Fatalf("null/count/cross-app isolation: %+v", apps)
	}
	params.Set("take", "1")
	params.Set("offset", "1")
	page := developApps(t, handler, params, "3")
	if len(page) != 1 || page[0].ID != id(1) {
		t.Fatal(page)
	}
	response := developRequest(handler, "/api/apps?"+params.Encode())
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw[0]) != 6 || string(raw[0]["description"]) != "null" {
		t.Fatalf("projection leaked fields/null changed: %s", response.Body.String())
	}
	for _, key := range []string{"id", "slug", "title", "description", "products_count", "widgets_count"} {
		if _, ok := raw[0][key]; !ok {
			t.Fatal("missing", key)
		}
	}
	params.Set("offset", "10")
	if len(developApps(t, handler, params, "3")) != 0 {
		t.Fatal("offset")
	}
	params.Set("offset", "0")
	params.Set("take", "0")
	if len(developApps(t, handler, params, "3")) != 0 {
		t.Fatal("take zero")
	}
	params.Set("search", "develop-page-absent")
	if len(developApps(t, handler, params, "0")) != 0 {
		t.Fatal("missing search")
	}
}

func TestPGDevelopDiscoverySearch(t *testing.T) {
	db := database(t)
	handler := developHandler(db)
	id := func(n int) string { return watcherID(0x94000000, n) }
	developInsert(t, db, id(1), "develop-search-percent%", "Search percentage", nil, 42, false)
	developInsert(t, db, id(2), "develop-search-under_score", "Search underscore", nil, 42, false)
	developInsert(t, db, id(3), "develop-search-plain", "Search ordinary", stringPointer("O'Reilly description-token"), 42, false)
	for _, tc := range []struct {
		search, total string
		ids           []string
	}{
		{"  DEVELOP-SEARCH-PLAIN  ", "1", []string{id(3)}},
		{"o'reilly", "1", []string{id(3)}},
		{"description-token", "1", []string{id(3)}},
		{"Search ordinary", "1", []string{id(3)}},
		{"develop-search-%", "3", []string{id(3), id(1), id(2)}},
		{`develop-search-percent\%`, "1", []string{id(1)}},
		{`develop-search-under\_score`, "1", []string{id(2)}},
		{"develop-search-under_score", "1", []string{id(2)}},
	} {
		apps := developApps(t, handler, url.Values{"search": {tc.search}}, tc.total)
		ids := make([]string, len(apps))
		for i, app := range apps {
			ids[i] = app.ID
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("search %q ids=%v want=%v", tc.search, ids, tc.ids)
		}
	}
	// Whitespace search is equivalent to absent search, including other tests' rows.
	absent := developRequest(handler, "/api/apps?take=100")
	blank := developRequest(handler, "/api/apps?take=100&search=%20%09%20")
	if absent.Code != 200 || blank.Code != 200 || absent.Body.String() != blank.Body.String() || absent.Header().Get("X-Total") != blank.Header().Get("X-Total") {
		t.Fatal("whitespace search differs")
	}
}

func TestPGDevelopDiscoveryPolicyAndReadOnly(t *testing.T) {
	db := database(t)
	id := "95000000-0000-0000-0000-000000000001"
	developInsert(t, db, id, "develop-readonly", "Develop readonly", nil, 42, false)
	candidate := developpg.NewSyntheticCompatibilityRepository(db, 42)
	for _, handler := range []http.Handler{develop.NewHandler(candidate), develop.NewSyntheticCompatibilityHandler(developpg.New(db))} {
		response := developRequest(handler, "/api/apps")
		if response.Code != 503 || response.Header().Get("X-Total") != "" || strings.Contains(response.Body.String(), "develop-readonly") {
			t.Fatalf("default policy allowed data: %d %s", response.Code, response.Body.String())
		}
	}
	// The repository works through an independent real read-only PostgreSQL session.
	readonly := database(t)
	readonly.SetMaxOpenConns(1)
	readonly.SetMaxIdleConns(1)
	query(t, readonly, "SET default_transaction_read_only = on")
	apps := developApps(t, developHandler(readonly), url.Values{"search": {"develop-readonly"}}, "1")
	if len(apps) != 1 || apps[0].ID != id {
		t.Fatal(apps)
	}
	if _, err := readonly.Exec("UPDATE custom_apps SET name='forbidden-write' WHERE id=$1", id); err == nil {
		t.Fatal("read-only fixture permits writes")
	} else {
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "25006" {
			t.Fatalf("expected PostgreSQL read-only SQLSTATE 25006: %v", err)
		}
	}
	var name string
	if err := db.QueryRow("SELECT name FROM custom_apps WHERE id=$1", id).Scan(&name); err != nil || name != "Develop readonly" {
		t.Fatalf("read altered row %q %v", name, err)
	}
	handler := developHandler(db)
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account"} {
		request := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
		request.Header[header] = []string{"synthetic"}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 403 || response.Header().Get("X-Total") != "" {
			t.Fatal(header, response.Code)
		}
	}
	for _, target := range []string{"/api/apps?take=-1", "/api/apps?take=101", "/api/apps?offset=-1", "/api/apps?take=2147483648", "/api/apps?take=1&take=2"} {
		if response := developRequest(handler, target); response.Code != 400 {
			t.Fatalf("invalid query %s => %d", target, response.Code)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := candidate.Discover(cancelled, develop.Query{Take: 20}); err == nil {
		t.Fatal("cancellation ignored")
	}
	broken := database(t)
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	response := developRequest(developHandler(broken), "/api/apps")
	if response.Code != 503 || strings.TrimSpace(response.Body.String()) != `{"error":"discovery_unavailable"}` || response.Header().Get("X-Total") != "" {
		t.Fatalf("DB error not sanitized: %d %s", response.Code, response.Body.String())
	}
}

func TestPGDevelopDiscoveryRestartPersistence(t *testing.T) {
	db := database(t)
	id := "96000000-0000-0000-0000-000000000001"
	if os.Getenv("PG_TEST_PHASE") == "before" {
		developInsert(t, db, id, "develop-restart-sentinel", "develop-restart-sentinel", nil, 42, false)
	} else if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	apps := developApps(t, developHandler(db), url.Values{"search": {"develop-restart-sentinel"}}, "1")
	if len(apps) != 1 || apps[0].ID != id || apps[0].Slug != "develop-restart-sentinel" || apps[0].Description != nil {
		t.Fatalf("restart fixture lost: %+v", apps)
	}
}
