// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"solsynth.local/go-migration/internal/developpg"
	"solsynth.local/go-migration/ports/develop"
)

const packageStageFixture int32 = 71 // Deliberately not the upstream enum number.

func packageProject(t *testing.T, db *sql.DB, prefix int) string {
	t.Helper()
	developer, project := watcherID(prefix, 1), watcherID(prefix, 2)
	query(t, db, `INSERT INTO developers(id,publisher_id) VALUES($1,$1)`, developer)
	query(t, db, `INSERT INTO dev_projects(id,developer_id) VALUES($1,$2)`, project, developer)
	return project
}

func packageInsert(t *testing.T, db *sql.DB, id, slug, project string, key, url *string, stage int32, deleted bool) {
	t.Helper()
	query(t, db, `INSERT INTO mini_apps(id,slug,project_id,package_storage_key,package_url,stage,deleted_at) VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $7 THEN now() ELSE NULL END)`, id, slug, project, key, url, stage, deleted)
}

func packageCounter(t *testing.T, db *sql.DB, id string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT download_count FROM mini_apps WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func packageHandler(db *sql.DB, storage develop.PackageStorage) http.Handler {
	return develop.NewSyntheticCompatibilityPackageHandler(develop.NewSyntheticCompatibilityPackageService(developpg.NewSyntheticCompatibilityPackageRepository(db, packageStageFixture), storage))
}

func packageFixtureZIP(t *testing.T) ([]byte, *develop.LocalPackageStorage) {
	t.Helper()
	root := t.TempDir()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	file, err := writer.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte(`{"id":"synthetic.package","name":"Synthetic package"}`)); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "plugins", "fixture.zip"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	storage, err := develop.NewSyntheticLocalPackageStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Close() })
	return buf.Bytes(), storage
}

func TestPGDevelopPackageDownloadBranches(t *testing.T) {
	db := database(t)
	project := packageProject(t, db, 0xa5000000)
	id := func(n int) string { return watcherID(0xa5000000, n) }
	zipData, storage := packageFixtureZIP(t)
	handler := packageHandler(db, storage)
	packageInsert(t, db, id(10), "package-local", project, stringPointer("plugins/fixture.zip"), nil, packageStageFixture, false)
	w := developRequest(handler, "/api/miniapps/PACKAGE-LOCAL/package")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), zipData) || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != "attachment; filename=package-local.zip" || w.Header().Get("Content-Length") != fmt.Sprint(len(zipData)) || w.Header().Get("X-Total") != "" || packageCounter(t, db, id(10)) != 1 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
		t.Fatal(err)
	}
	url := "https://packages.example.invalid/fixture.zip?v=1"
	packageInsert(t, db, id(11), "package-redirect", project, stringPointer("object-does-not-need-local-file.zip"), &url, packageStageFixture, false)
	w = developRequest(packageHandler(db, nil), "/api/miniapps/package-redirect/package")
	if w.Code != 302 || w.Header().Get("Location") != url || w.Body.Len() != 0 || packageCounter(t, db, id(11)) != 1 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for n, key := range []*string{nil, stringPointer(""), stringPointer(" \t")} {
		slug := fmt.Sprintf("package-empty-%d", n)
		packageInsert(t, db, id(20+n), slug, project, key, &url, packageStageFixture, false)
		w = developRequest(handler, "/api/miniapps/"+slug+"/package")
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"DEV_MINI_APP_NO_PACKAGE"`) || !strings.Contains(w.Body.String(), `"message":"Mini app has no published package"`) || packageCounter(t, db, id(20+n)) != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = developRequest(handler, "/api/miniapps/package-absent/package")
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"DEV_MINI_APP_NOT_FOUND"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPGDevelopPackagePolicyAndFailures(t *testing.T) {
	db := database(t)
	project := packageProject(t, db, 0xa6000000)
	id := func(n int) string { return watcherID(0xa6000000, n) }
	_, storage := packageFixtureZIP(t)
	packageInsert(t, db, id(10), "package-policy", project, stringPointer("plugins/fixture.zip"), nil, packageStageFixture, false)
	repo := developpg.NewSyntheticCompatibilityPackageRepository(db, packageStageFixture)
	candidate := develop.NewSyntheticCompatibilityPackageService(repo, storage)
	for _, handler := range []http.Handler{
		develop.NewPackageHandler(candidate),
		develop.NewSyntheticCompatibilityPackageHandler(develop.NewPackageService(repo, storage)),
		develop.NewSyntheticCompatibilityPackageHandler(develop.NewSyntheticCompatibilityPackageService(developpg.NewPackageRepository(db), storage)),
	} {
		if w := developRequest(handler, "/api/miniapps/package-policy/package"); w.Code != 503 || packageCounter(t, db, id(10)) != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	handler := packageHandler(db, storage)
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account"} {
		r := httptest.NewRequest(http.MethodGet, "/api/miniapps/package-policy/package", nil)
		r.Header[header] = []string{"fixture"}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 || packageCounter(t, db, id(10)) != 0 {
			t.Fatal(header, w.Code)
		}
	}
	for n, tc := range []struct {
		slug    string
		stage   int32
		deleted bool
	}{
		{"package-unpublished", 70, false}, {"package-deleted", packageStageFixture, true},
	} {
		packageInsert(t, db, id(20+n), tc.slug, project, stringPointer("plugins/fixture.zip"), nil, tc.stage, tc.deleted)
		if w := developRequest(handler, "/api/miniapps/"+tc.slug+"/package"); w.Code != 404 || packageCounter(t, db, id(20+n)) != 0 {
			t.Fatal(tc, w.Code)
		}
	}
	deletedProject := watcherID(0xa6000000, 3)
	query(t, db, `INSERT INTO dev_projects(id,developer_id,deleted_at) VALUES($1,$2,now())`, deletedProject, id(1))
	packageInsert(t, db, id(25), "package-deleted-project", deletedProject, stringPointer("plugins/fixture.zip"), nil, packageStageFixture, false)
	if w := developRequest(handler, "/api/miniapps/package-deleted-project/package"); w.Code != 404 || packageCounter(t, db, id(25)) != 0 {
		t.Fatal(w.Code)
	}
	for n, tc := range []struct {
		slug, key string
		url       *string
	}{
		{"package-storage-missing", "plugins/absent.zip", nil},
		{"package-key-invalid", "../outside.zip", nil},
		{"package-redirect-invalid", "plugins/fixture.zip", stringPointer("mailto:someone@example.invalid")},
	} {
		packageInsert(t, db, id(30+n), tc.slug, project, &tc.key, tc.url, packageStageFixture, false)
		w := developRequest(handler, "/api/miniapps/"+tc.slug+"/package")
		if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"packages_unavailable"}` || packageCounter(t, db, id(30+n)) != 1 {
			t.Fatal(w.Code, w.Body.String(), "counter", packageCounter(t, db, id(30+n)))
		}
	}
	// The increment is a separate committed statement: storage failure cannot
	// roll it back, even when observed through an independently opened pool.
	if packageCounter(t, database(t), id(30)) != 1 {
		t.Fatal("storage failure increment was not committed")
	}
	query(t, db, `UPDATE mini_apps SET download_count=$1 WHERE id=$2`, int64(math.MaxInt64), id(10))
	w := developRequest(handler, "/api/miniapps/package-policy/package")
	if w.Code != 503 || packageCounter(t, db, id(10)) != math.MaxInt64 || strings.Contains(w.Body.String(), "overflow") {
		t.Fatal(w.Code, w.Body.String())
	}
	query(t, db, `UPDATE mini_apps SET download_count=0 WHERE id=$1`, id(10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := candidate.Download(ctx, "package-policy"); !errors.Is(err, context.Canceled) || packageCounter(t, db, id(10)) != 0 {
		t.Fatal(err)
	}
	readonly := database(t)
	readonly.SetMaxOpenConns(1)
	readonly.SetMaxIdleConns(1)
	query(t, readonly, `SET default_transaction_read_only=on`)
	w = developRequest(packageHandler(readonly, storage), "/api/miniapps/package-policy/package")
	if w.Code != 503 || packageCounter(t, db, id(10)) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	broken := database(t)
	broken.Close()
	w = developRequest(packageHandler(broken, storage), "/api/miniapps/package-policy/package")
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"packages_unavailable"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	// A concurrently disappeared/deleted row produces zero updates; the source
	// ignores affected rows rather than interpreting it as a storage failure.
	if err := repo.IncrementPackageDownloads(context.Background(), watcherID(0xa6000000, 99)); err != nil {
		t.Fatal(err)
	}
	if err := repo.IncrementPackageDownloads(context.Background(), id(21)); err != nil || packageCounter(t, db, id(21)) != 0 {
		t.Fatal(err)
	}
}

func TestPGDevelopPackageAtomicCounter(t *testing.T) {
	db := database(t)
	project := packageProject(t, db, 0xa7000000)
	id := watcherID(0xa7000000, 10)
	packageInsert(t, db, id, "package-concurrent", project, stringPointer("object.zip"), stringPointer("https://packages.example.invalid/object.zip"), packageStageFixture, false)
	handlers := []http.Handler{packageHandler(db, nil), packageHandler(database(t), nil)}
	const count = 32
	start := make(chan struct{})
	results := make(chan int, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- developRequest(handlers[i%len(handlers)], "/api/miniapps/package-concurrent/package").Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for status := range results {
		if status != 302 {
			t.Fatal(status)
		}
	}
	if got := packageCounter(t, db, id); got != count {
		t.Fatalf("lost atomic increments: %d want %d", got, count)
	}
}

func TestPGDevelopPackageRestartPersistence(t *testing.T) {
	db := database(t)
	id := watcherID(0xa8000000, 10)
	phase := os.Getenv("PG_TEST_PHASE")
	if phase == "before" {
		project := packageProject(t, db, 0xa8000000)
		packageInsert(t, db, id, "package-restart", project, stringPointer("object.zip"), stringPointer("https://packages.example.invalid/restart.zip"), packageStageFixture, false)
	} else if phase != "after" {
		t.Fatal("missing restart phase")
	}
	want := int64(0)
	if phase == "after" {
		want = 1
	}
	if got := packageCounter(t, db, id); got != want {
		t.Fatalf("persisted counter %d want %d", got, want)
	}
	w := developRequest(packageHandler(db, nil), "/api/miniapps/package-restart/package")
	if w.Code != 302 || w.Header().Get("Location") != "https://packages.example.invalid/restart.zip" || packageCounter(t, db, id) != want+1 {
		t.Fatal(w.Code, w.Header())
	}
}
