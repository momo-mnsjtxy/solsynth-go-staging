// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func offlineDisposableConfig(t *testing.T) disposableDatabaseConfig {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "run.Abcdef12")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "socket"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".solsynth-disposable"), []byte("synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return disposableDatabaseConfig{root: root, parent: parent, dsn: "host=" + root + "/socket port=55432 user=synthetic dbname=postgres sslmode=disable", candidate: strings.Repeat("a", 40), schemaHash: strings.Repeat("b", 64), ci: "true", privateCI: "true"}
}

func offlineFixtureTemplate(c disposableDatabaseConfig) fixtureTemplate {
	h := sha256.Sum256([]byte(c.root + "\x00" + c.candidate))
	nonce := hex.EncodeToString(h[:])[:24]
	return fixtureTemplate{Version: 1, Candidate: c.candidate, SchemaSHA256: c.schemaHash, Name: "solsynth_template_" + nonce, OID: 42, Cluster: "12345", Comment: "solsynth-fixture-template/v1/" + c.candidate + "/" + c.schemaHash + "/" + nonce, SchemaFiles: []string{"internal/authoritypg/schema.sql"}, Extensions: [][]string{{"plpgsql", "1.0"}}, ApplicationTables: 1}
}

func TestDisposableDatabaseGuardOffline(t *testing.T) {
	c := offlineDisposableConfig(t)
	if err := validateDisposableDatabaseConfig(c); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*disposableDatabaseConfig){
		"outside_ci":        func(v *disposableDatabaseConfig) { v.ci = "false" },
		"public_ci":         func(v *disposableDatabaseConfig) { v.privateCI = "false" },
		"foreign_host":      func(v *disposableDatabaseConfig) { v.dsn = strings.Replace(v.dsn, "host=", "host=unrelated ", 1) },
		"extra_database":    func(v *disposableDatabaseConfig) { v.dsn += " dbname=production" },
		"extra_credentials": func(v *disposableDatabaseConfig) { v.dsn += " password=never-log-this" },
		"wrong_port":        func(v *disposableDatabaseConfig) { v.dsn = strings.Replace(v.dsn, "55432", "5432", 1) },
		"different_parent":  func(v *disposableDatabaseConfig) { v.parent = filepath.Dir(v.parent) },
		"unclean_root":      func(v *disposableDatabaseConfig) { v.root += "/../" + filepath.Base(v.root) },
		"invalid_commit":    func(v *disposableDatabaseConfig) { v.candidate = "main" },
		"invalid_hash":      func(v *disposableDatabaseConfig) { v.schemaHash = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			v := c
			mutate(&v)
			if err := validateDisposableDatabaseConfig(v); err == nil || strings.Contains(err.Error(), "never-log-this") {
				t.Fatal("unsafe config accepted or secret echoed")
			}
		})
	}
	marker := filepath.Join(c.root, ".solsynth-disposable")
	if err := os.WriteFile(marker, []byte("not-synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if validateDisposableDatabaseConfig(c) == nil {
		t.Fatal("wrong marker accepted")
	}
	if err := os.WriteFile(marker, []byte("synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0644); err != nil {
		t.Fatal(err)
	}
	if validateDisposableDatabaseConfig(c) == nil {
		t.Fatal("public marker accepted")
	}
	if err := os.Chmod(marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.root, "socket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(c.root, "socket")); err != nil {
		t.Fatal(err)
	}
	if validateDisposableDatabaseConfig(c) == nil {
		t.Fatal("redirected socket accepted")
	}
}

func TestDisposableDatabaseIdentityOffline(t *testing.T) {
	c := offlineDisposableConfig(t)
	m := offlineFixtureTemplate(c)
	name := "solsynth_fixture_composer_" + strings.Repeat("c", 24)
	saved := isolatedRestartDatabase{Name: name, OID: 43, Comment: "solsynth-fixture/v1/" + c.candidate + "/" + c.schemaHash + "/" + name, Template: m}
	if err := validateIsolatedRestartDatabase(c, m, saved); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*fixtureTemplate){
		"wrong_candidate":     func(v *fixtureTemplate) { v.Candidate = strings.Repeat("c", 40) },
		"wrong_schema":        func(v *fixtureTemplate) { v.SchemaSHA256 = strings.Repeat("c", 64) },
		"missing_oid":         func(v *fixtureTemplate) { v.OID = 0 },
		"different_root":      func(v *fixtureTemplate) { v.Name = "solsynth_template_" + strings.Repeat("d", 24) },
		"baseline_copied":     func(v *fixtureTemplate) { v.BaselineRowsCopied = 1 },
		"missing_inventory":   func(v *fixtureTemplate) { v.SchemaFiles = nil },
		"duplicate_inventory": func(v *fixtureTemplate) { v.SchemaFiles = []string{"schema.sql", "schema.sql"} },
		"path_escape":         func(v *fixtureTemplate) { v.SchemaFiles = []string{"../schema.sql"} },
		"missing_extensions":  func(v *fixtureTemplate) { v.Extensions = nil },
		"wrong_comment":       func(v *fixtureTemplate) { v.Comment = "unrelated" },
	} {
		t.Run(name, func(t *testing.T) {
			v := m
			mutate(&v)
			if validateIsolatedRestartDatabase(c, v, saved) == nil {
				t.Fatal("invalid template accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*isolatedRestartDatabase){
		"foreign_database":      func(v *isolatedRestartDatabase) { v.Name = "postgres" },
		"sql_injection":         func(v *isolatedRestartDatabase) { v.Name = `x";DROP DATABASE postgres;--` },
		"missing_database_oid":  func(v *isolatedRestartDatabase) { v.OID = 0 },
		"replaced_template_oid": func(v *isolatedRestartDatabase) { v.Template.OID++ },
		"foreign_comment":       func(v *isolatedRestartDatabase) { v.Comment = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			v := saved
			mutate(&v)
			if validateIsolatedRestartDatabase(c, m, v) == nil {
				t.Fatal("invalid saved database accepted")
			}
		})
	}
}

// The connector has no network or PostgreSQL implementation. Catalog negatives
// exercise the real database/sql scan and identity comparisons with fixed rows.
type fixtureCatalogConnector struct{ values []driver.Value }
type fixtureCatalogDriver struct{}
type fixtureCatalogConn struct{ values []driver.Value }
type fixtureCatalogRows struct {
	values []driver.Value
	done   bool
}

func (c fixtureCatalogConnector) Connect(context.Context) (driver.Conn, error) {
	return &fixtureCatalogConn{c.values}, nil
}
func (fixtureCatalogConnector) Driver() driver.Driver { return fixtureCatalogDriver{} }
func (fixtureCatalogDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("network forbidden")
}
func (*fixtureCatalogConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare forbidden")
}
func (*fixtureCatalogConn) Begin() (driver.Tx, error) { return nil, errors.New("writes forbidden") }
func (*fixtureCatalogConn) Close() error              { return nil }
func (c *fixtureCatalogConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(q, "SELECT ") {
		return nil, errors.New("writes forbidden")
	}
	return &fixtureCatalogRows{values: c.values}, nil
}
func (r *fixtureCatalogRows) Columns() []string {
	n := len(r.values)
	if n == 0 {
		n = 6
	}
	return make([]string, n)
}
func (*fixtureCatalogRows) Close() error { return nil }
func (r *fixtureCatalogRows) Next(out []driver.Value) error {
	if r.done || len(r.values) == 0 {
		return io.EOF
	}
	r.done = true
	copy(out, r.values)
	return nil
}

func TestDisposableDatabaseCatalogOffline(t *testing.T) {
	c := offlineDisposableConfig(t)
	m := offlineFixtureTemplate(c)
	saved := isolatedRestartDatabase{Name: "solsynth_fixture_composer_" + strings.Repeat("a", 24), OID: 43, Comment: "expected", Template: m}
	cases := []struct {
		name   string
		values []driver.Value
		check  func(context.Context, *sql.DB) error
	}{
		{"template", []driver.Value{int64(m.OID), false, m.Comment, m.Cluster, int64(0)}, func(ctx context.Context, db *sql.DB) error { return verifyTemplateCatalog(ctx, db, m) }},
		{"database", []driver.Value{int64(saved.OID), true, saved.Comment}, func(ctx context.Context, db *sql.DB) error { return verifyIsolatedCatalog(ctx, db, saved) }},
		{"connection", []driver.Value{saved.Name, int64(saved.OID), m.Cluster}, func(ctx context.Context, db *sql.DB) error { return verifyIsolatedConnection(ctx, db, saved) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := sql.OpenDB(fixtureCatalogConnector{tc.values})
			defer db.Close()
			if err := tc.check(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			for i := range tc.values {
				values := append([]driver.Value(nil), tc.values...)
				switch v := values[i].(type) {
				case int64:
					values[i] = v + 1
				case bool:
					values[i] = !v
				case string:
					values[i] = v + "changed"
				}
				bad := sql.OpenDB(fixtureCatalogConnector{values})
				if tc.check(context.Background(), bad) == nil {
					t.Fatalf("changed catalog field %d accepted", i)
				}
				bad.Close()
			}
			missing := sql.OpenDB(fixtureCatalogConnector{})
			defer missing.Close()
			if tc.check(context.Background(), missing) == nil {
				t.Fatal("missing database accepted")
			}
		})
	}
}
