// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/nodeinfopg"
	"solsynth.local/go-migration/ports/sphere/nodeinfo"
)

// All data, enum numbers, visibility rules and the schema are synthetic.
const nodeinfoFediverseFixture int32 = 73

func nodeinfoDatabase(t *testing.T) *sql.DB {
	t.Helper()
	// Reuse the shared disposable-cluster guard before opening any additional
	// pool. Search path is configured on every connection, not via a pooled SET.
	guard := database(t)
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(os.Getenv("PG_TEST_DSN"))
	if err != nil {
		t.Fatal("invalid synthetic PostgreSQL test configuration")
	}
	config.RuntimeParams["search_path"] = "nodeinfo_candidate"
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	var schema string
	if err := db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil || schema != "nodeinfo_candidate" {
		t.Fatal("required isolated NodeInfo fixture schema not loaded")
	}
	return db
}

func nodeinfoRepository(t *testing.T, db *sql.DB) *nodeinfopg.Repository {
	t.Helper()
	r, err := nodeinfopg.NewSyntheticCompatibilityRepository(db, nodeinfoFediverseFixture, nodeinfopg.CandidateExcludeDeletedRows)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func nodeinfoID(n int) string { return fmt.Sprintf("a7000000-0000-0000-0000-%012d", n) }

func nodeinfoCleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Cleanup(func() {
		query(t, db, "DELETE FROM posts WHERE id::text LIKE 'a7000000-%'")
		query(t, db, "DELETE FROM publishers WHERE id::text LIKE 'a7000000-%'")
	})
}

func nodeinfoInsertPublisher(t *testing.T, db *sql.DB, n int, kind int32, uri *string, deleted bool) {
	t.Helper()
	query(t, db, `INSERT INTO publishers(id,type,uri,deleted_at) VALUES($1,$2,$3,CASE WHEN $4 THEN now() ELSE NULL END)`, nodeinfoID(n), kind, uri, deleted)
}

func nodeinfoRequest(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestPGNodeInfoCountsAndHTTP(t *testing.T) {
	db := nodeinfoDatabase(t)
	nodeinfoCleanup(t, db)
	repo := nodeinfoRepository(t, db)
	if stats, err := repo.ReadStats(context.Background()); err != nil || stats != (nodeinfo.Stats{}) {
		t.Fatal(stats, err)
	}
	uri := "https://synthetic.invalid/actor"
	empty := ""
	nodeinfoInsertPublisher(t, db, 1, 11, nil, false)
	nodeinfoInsertPublisher(t, db, 2, 11, &uri, false)
	nodeinfoInsertPublisher(t, db, 3, 12, &empty, false) // Empty string is non-null.
	nodeinfoInsertPublisher(t, db, 4, nodeinfoFediverseFixture, &uri, false)
	nodeinfoInsertPublisher(t, db, 5, nodeinfoFediverseFixture, nil, false)
	nodeinfoInsertPublisher(t, db, 6, 11, &uri, true)
	for _, p := range []struct {
		id, publisher int
		deleted       bool
	}{
		{10, 1, false}, {11, 4, false}, {12, 6, false}, {13, 999, false}, {14, 2, true},
	} {
		// No publisher join is added: posts attached to a remote/deleted/missing
		// publisher count under this intentionally minimal SQL projection.
		query(t, db, `INSERT INTO posts(id,publisher_id,deleted_at) VALUES($1,$2,CASE WHEN $3 THEN now() ELSE NULL END)`, nodeinfoID(p.id), nodeinfoID(p.publisher), p.deleted)
	}
	want := nodeinfo.Stats{TotalPublishers: 5, ActivePublishers: 2, TotalPosts: 4}
	if stats, err := repo.ReadStats(context.Background()); err != nil || stats != want {
		t.Fatal(stats, err)
	}
	h := nodeinfo.NewSyntheticCompatibilityHandler(repo, nil)
	for _, version := range []string{"2.0", "2.1"} {
		w := nodeinfoRequest(h, "/.well-known/nodeinfo/"+version)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var doc nodeinfo.Document
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Usage.Users.Total != 5 || doc.Usage.Users.ActiveMonth != 2 || doc.Usage.Users.ActiveHalfyear != 2 || doc.Usage.LocalPosts != 4 || doc.Usage.LocalComments != 0 {
			t.Fatal(doc)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["open_registrations"]; !ok {
			t.Fatal("missing snake_case property")
		}
		if version == "2.0" && (raw["version"] != nil || raw["metadata"] != nil || doc.Configuration.URLs == nil) {
			t.Fatal("2.0 shape", w.Body.String())
		}
		if version == "2.1" && (string(raw["version"]) != `"2.1"` || string(raw["metadata"]) != `{}` || doc.Configuration.URLs != nil) {
			t.Fatal("2.1 shape", w.Body.String())
		}
	}
	// A different caller-supplied enum number changes the exclusion. It is never
	// silently replaced by a guessed production mapping.
	other, err := nodeinfopg.NewSyntheticCompatibilityRepository(db, 11, nodeinfopg.CandidateExcludeDeletedRows)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := other.ReadStats(context.Background()); err != nil || stats.ActivePublishers != 2 {
		t.Fatal(stats, err)
	}
	other, err = nodeinfopg.NewSyntheticCompatibilityRepository(db, -1, nodeinfopg.CandidateExcludeDeletedRows)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := other.ReadStats(context.Background()); err != nil || stats.ActivePublishers != 3 {
		t.Fatal(stats, err)
	}
	// The licensed current snapshot makes PublisherId non-null. Do not relax
	// the real-schema nullability merely to invent a NULL integration fixture.
	if _, err := db.Exec(`INSERT INTO posts(id,publisher_id) VALUES($1,NULL)`, nodeinfoID(15)); err == nil {
		t.Fatal("publisher_id nullability changed")
	}
}

func TestPGNodeInfoPolicyAndReadOnly(t *testing.T) {
	db := nodeinfoDatabase(t)
	nodeinfoCleanup(t, db)
	uri := "https://synthetic.invalid/local"
	nodeinfoInsertPublisher(t, db, 20, 11, &uri, false)
	repo := nodeinfoRepository(t, db)
	for _, h := range []http.Handler{nodeinfo.NewHandler(repo, nil), nodeinfo.NewSyntheticCompatibilityHandler(nodeinfopg.New(db), nil)} {
		w := nodeinfoRequest(h, "/.well-known/nodeinfo/2.1")
		if w.Code != 503 || strings.Contains(w.Body.String(), "total") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := nodeinfopg.NewSyntheticCompatibilityRepository(db, nodeinfoFediverseFixture, ""); !errors.Is(err, nodeinfopg.ErrCandidatePolicyRequired) {
		t.Fatal(err)
	}
	readonly := nodeinfoDatabase(t)
	readonly.SetMaxOpenConns(1)
	readonly.SetMaxIdleConns(1)
	query(t, readonly, "SET default_transaction_read_only = on")
	if stats, err := nodeinfoRepository(t, readonly).ReadStats(context.Background()); err != nil || stats != (nodeinfo.Stats{TotalPublishers: 1, ActivePublishers: 1}) {
		t.Fatal(stats, err)
	}
	if _, err := readonly.Exec("UPDATE publishers SET uri = NULL"); err == nil {
		t.Fatal("read-only fixture allows writes")
	} else {
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "25006" {
			t.Fatalf("expected read-only SQLSTATE: %v", err)
		}
	}
	if stats, err := repo.ReadStats(context.Background()); err != nil || stats.ActivePublishers != 1 {
		t.Fatal(stats, err)
	}
}

func TestPGNodeInfoFailurePropagation(t *testing.T) {
	db := nodeinfoDatabase(t)
	nodeinfoCleanup(t, db)
	uri := "https://synthetic.invalid/secret"
	nodeinfoInsertPublisher(t, db, 30, 11, &uri, false)
	for _, fault := range []struct{ name, breakSQL, restoreSQL string }{
		{"first_count", "ALTER TABLE publishers RENAME TO temporarily_missing_publishers", "ALTER TABLE temporarily_missing_publishers RENAME TO publishers"},
		{"second_count", "ALTER TABLE posts RENAME TO temporarily_missing_posts", "ALTER TABLE temporarily_missing_posts RENAME TO posts"},
		{"third_count", "ALTER TABLE publishers RENAME COLUMN type TO temporarily_missing_type", "ALTER TABLE publishers RENAME COLUMN temporarily_missing_type TO type"},
	} {
		t.Run(fault.name, func(t *testing.T) {
			query(t, db, fault.breakSQL)
			defer query(t, db, fault.restoreSQL)
			repo := nodeinfoRepository(t, db)
			if stats, err := repo.ReadStats(context.Background()); err == nil || stats != (nodeinfo.Stats{}) {
				t.Fatal("partial success returned", stats, err)
			}
			w := nodeinfoRequest(nodeinfo.NewSyntheticCompatibilityHandler(repo, nil), "/.well-known/nodeinfo/2.1")
			if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"nodeinfo_unavailable"}` {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	// A real PostgreSQL lock forces an in-flight query to observe cancellation.
	lock, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec("LOCK TABLE publishers IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if stats, err := nodeinfoRepository(t, db).ReadStats(ctx); err == nil || ctx.Err() != context.DeadlineExceeded || stats != (nodeinfo.Stats{}) {
		t.Fatal(stats, err, ctx.Err())
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	closed := nodeinfoDatabase(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	w := nodeinfoRequest(nodeinfo.NewSyntheticCompatibilityHandler(nodeinfoRepository(t, closed), nil), "/.well-known/nodeinfo/2.0")
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"nodeinfo_unavailable"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	// Discovery remains independent of database availability by source design.
	if w := nodeinfoRequest(nodeinfo.NewSyntheticCompatibilityHandler(nodeinfoRepository(t, closed), nil), "/.well-known/nodeinfo"); w.Code != 200 {
		t.Fatal(w)
	}
}

func TestPGNodeInfoRestartPersistence(t *testing.T) {
	db := nodeinfoDatabase(t)
	if os.Getenv("PG_TEST_PHASE") == "before" {
		query(t, db, `INSERT INTO publishers(id,type,uri) VALUES('a8000000-0000-0000-0000-000000000001',$1,'https://synthetic.invalid/restart')`, int32(11))
		query(t, db, `INSERT INTO posts(id,publisher_id) VALUES('a8000000-0000-0000-0000-000000000002','a8000000-0000-0000-0000-000000000001')`)
	} else if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("missing restart phase")
	}
	stats, err := nodeinfoRepository(t, db).ReadStats(context.Background())
	if err != nil || stats != (nodeinfo.Stats{TotalPublishers: 1, ActivePublishers: 1, TotalPosts: 1}) {
		t.Fatal(stats, err)
	}
}
