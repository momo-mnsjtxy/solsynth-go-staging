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
	"net/url"
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
	local := c
	local.mode, local.ci, local.privateCI = "local-disposable", "", ""
	if err := validateDisposableDatabaseConfig(local); err != nil {
		t.Fatal("explicit local disposable configuration rejected", err)
	}
	localTCP := local
	localTCP.localTransport, localTCP.localPort, localTCP.localPassword = "loopback-tcp", "55432", strings.Repeat("c", 64)
	localTCP.dsn = "host=127.0.0.1 port=55432 user=synthetic dbname=postgres sslmode=disable password=" + localTCP.localPassword
	if err := validateDisposableDatabaseConfig(localTCP); err != nil {
		t.Fatal("explicit local loopback configuration rejected", err)
	}
	t.Run("local_loopback_transport", func(t *testing.T) {
		for _, port := range []string{"1024", "65535"} {
			v := localTCP
			v.localPort = port
			v.dsn = "host=127.0.0.1 port=" + port + " user=synthetic dbname=postgres sslmode=disable password=" + v.localPassword
			if err := validateDisposableDatabaseConfig(v); err != nil {
				t.Fatal("valid local loopback port boundary rejected", err)
			}
		}
		for name, mutate := range map[string]func(*disposableDatabaseConfig){
			"low_port":          func(v *disposableDatabaseConfig) { v.localPort = "1023" },
			"high_port":         func(v *disposableDatabaseConfig) { v.localPort = "65536" },
			"nondecimal_port":   func(v *disposableDatabaseConfig) { v.localPort = "+55432" },
			"padded_port":       func(v *disposableDatabaseConfig) { v.localPort = "055432" },
			"spaced_port":       func(v *disposableDatabaseConfig) { v.localPort = "55432 " },
			"empty_port":        func(v *disposableDatabaseConfig) { v.localPort = "" },
			"empty_password":    func(v *disposableDatabaseConfig) { v.localPassword = "" },
			"short_password":    func(v *disposableDatabaseConfig) { v.localPassword = strings.Repeat("c", 63) },
			"long_password":     func(v *disposableDatabaseConfig) { v.localPassword = strings.Repeat("c", 65) },
			"uppercase_secret":  func(v *disposableDatabaseConfig) { v.localPassword = strings.Repeat("C", 64) },
			"nonhex_secret":     func(v *disposableDatabaseConfig) { v.localPassword = strings.Repeat("g", 64) },
			"secret_injection":  func(v *disposableDatabaseConfig) { v.localPassword = "never-log-this dbname=production" },
			"unknown_transport": func(v *disposableDatabaseConfig) { v.localTransport = "tcp" },
			"implicit_transport": func(v *disposableDatabaseConfig) {
				v.localTransport = ""
			},
			"ci_transport": func(v *disposableDatabaseConfig) {
				v.mode, v.ci, v.privateCI = "", "true", "true"
			},
			"foreign_address":  func(v *disposableDatabaseConfig) { v.dsn = strings.Replace(v.dsn, "127.0.0.1", "127.0.0.2", 1) },
			"unspecified_bind": func(v *disposableDatabaseConfig) { v.dsn = strings.Replace(v.dsn, "127.0.0.1", "0.0.0.0", 1) },
			"missing_secret":   func(v *disposableDatabaseConfig) { v.dsn = strings.Split(v.dsn, " password=")[0] },
			"different_secret": func(v *disposableDatabaseConfig) {
				v.dsn = strings.ReplaceAll(v.dsn, strings.Repeat("c", 64), strings.Repeat("d", 64))
			},
			"extra_parameter": func(v *disposableDatabaseConfig) { v.dsn += " application_name=untrusted" },
		} {
			t.Run(name, func(t *testing.T) {
				v := localTCP
				mutate(&v)
				if err := validateDisposableDatabaseConfig(v); err == nil || strings.Contains(err.Error(), "never-log-this") || strings.Contains(err.Error(), localTCP.localPassword) {
					t.Fatal("unsafe local transport accepted or connection secret echoed")
				}
			})
		}
		for _, original := range []disposableDatabaseConfig{c, local} {
			for _, field := range []string{"transport", "port", "password"} {
				v := original
				switch field {
				case "transport":
					v.localTransport = "loopback-tcp"
				case "port":
					v.localPort = "55432"
				case "password":
					v.localPassword = localTCP.localPassword
				}
				if validateDisposableDatabaseConfig(v) == nil {
					t.Fatal("Unix or private CI accepted extra local transport settings")
				}
			}
		}
	})
	t.Run("backend_process_connection_urls", func(t *testing.T) {
		for _, config := range []disposableDatabaseConfig{c, local, localTCP} {
			t.Run(config.mode+"_"+config.localTransport, func(t *testing.T) {
				// These are offline inputs: no PostgreSQL connection is opened.
				for _, entry := range os.Environ() {
					name, _, _ := strings.Cut(entry, "=")
					if strings.HasPrefix(name, "GITHUB_") {
						t.Setenv(name, "")
					}
				}
				for key, value := range map[string]string{
					"SOLSYNTH_DISPOSABLE_PG": config.root, "PG_TEST_ROOT": config.parent,
					"PG_TEST_DSN": config.dsn, "PG_TEST_CANDIDATE": config.candidate,
					"PG_TEST_SCHEMA_SHA256": config.schemaHash, "PG_TEST_MODE": config.mode,
					"GITHUB_ACTIONS": config.ci, "PG_TEST_PRIVATE_CI": config.privateCI, "CI": config.genericCI,
					"PG_TEST_LOCAL_TRANSPORT": config.localTransport, "PG_TEST_LOCAL_PORT": config.localPort,
					"PG_TEST_LOCAL_PASSWORD": config.localPassword,
				} {
					t.Setenv(key, value)
				}
				parsed, err := url.Parse(backendProcessDSN(t))
				if err != nil || parsed.Scheme != "postgres" || parsed.Path != "/postgres" || parsed.User == nil || parsed.User.Username() != "synthetic" || parsed.Query().Get("sslmode") != "disable" {
					t.Fatal("child process PostgreSQL URL changed connection identity")
				}
				password, hasPassword := parsed.User.Password()
				if config.localTransport == "loopback-tcp" {
					if parsed.Host != "127.0.0.1:"+config.localPort || !hasPassword || password != config.localPassword || len(parsed.Query()) != 1 {
						t.Fatal("child process PostgreSQL URL changed explicit loopback credentials")
					}
				} else if parsed.Host != "localhost:55432" || hasPassword || parsed.Query().Get("host") != filepath.Join(config.root, "socket") || len(parsed.Query()) != 2 {
					t.Fatal("child process PostgreSQL URL changed private Unix socket")
				}
			})
		}
	})
	t.Run("local_execution_identity", func(t *testing.T) {
		for name, mutate := range map[string]func(*disposableDatabaseConfig){
			"github_actions":      func(v *disposableDatabaseConfig) { v.ci = "false" },
			"generic_ci":          func(v *disposableDatabaseConfig) { v.genericCI = "false" },
			"private_ci":          func(v *disposableDatabaseConfig) { v.privateCI = "false" },
			"github_environment":  func(v *disposableDatabaseConfig) { v.githubEnvironment = true },
			"unknown_mode":        func(v *disposableDatabaseConfig) { v.mode = "local" },
			"implicit_local_mode": func(v *disposableDatabaseConfig) { v.mode = "" },
		} {
			t.Run(name, func(t *testing.T) {
				v := local
				mutate(&v)
				if validateDisposableDatabaseConfig(v) == nil {
					t.Fatal("local execution accepted an ambiguous or CI identity")
				}
			})
		}
	})
	t.Run("all_nonempty_github_variables", func(t *testing.T) {
		for _, name := range []string{"GITHUB_ACTIONS", "GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_UNRECOGNIZED_VARIABLE"} {
			if !hasGithubEnvironment([]string{name + "=false"}) || hasGithubEnvironment([]string{name + "="}) {
				t.Fatal("GitHub environment prefix or empty value was misclassified")
			}
		}
		if hasGithubEnvironment([]string{"CI=true", "NOT_GITHUB_VALUE=true"}) {
			t.Fatal("unrelated environment variable was classified as GitHub identity")
		}
	})
	t.Run("provider_socket_guards", func(t *testing.T) {
		private := c
		private.genericCI = "true"
		for _, config := range []disposableDatabaseConfig{private, local, localTCP} {
			if err := validateVerificationSocketConfig(config, "1"); err != nil {
				t.Fatal("valid required disposable provider context rejected", err)
			}
			for _, required := range []string{"", "0", "true"} {
				if validateVerificationSocketConfig(config, required) == nil {
					t.Fatal("provider socket accepted optional PostgreSQL execution")
				}
			}
			config.dsn += " dbname=production"
			if validateVerificationSocketConfig(config, "1") == nil {
				t.Fatal("provider socket bypassed complete disposable connection validation")
			}
		}
		if validateVerificationSocketConfig(c, "1") == nil {
			t.Fatal("private CI provider socket accepted missing CI=true")
		}
	})
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
			for _, original := range []disposableDatabaseConfig{c, local, localTCP} {
				v := original
				mutate(&v)
				if err := validateDisposableDatabaseConfig(v); err == nil || strings.Contains(err.Error(), "never-log-this") {
					t.Fatal("unsafe config accepted or secret echoed")
				}
			}
		})
	}
	rejectFilesystem := func() {
		t.Helper()
		for _, config := range []disposableDatabaseConfig{c, local, localTCP} {
			if validateDisposableDatabaseConfig(config) == nil || validateVerificationSocketConfig(config, "1") == nil {
				t.Fatal("invalid disposable filesystem accepted")
			}
		}
	}
	marker := filepath.Join(c.root, ".solsynth-disposable")
	if err := os.WriteFile(marker, []byte("not-synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rejectFilesystem()
	if err := os.WriteFile(marker, []byte("synthetic-only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0644); err != nil {
		t.Fatal(err)
	}
	rejectFilesystem()
	if err := os.Chmod(marker, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{c.root, filepath.Join(c.root, "socket")} {
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
		rejectFilesystem()
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(c.root, "socket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(c.root, "socket")); err != nil {
		t.Fatal(err)
	}
	rejectFilesystem()
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
	localTCP := c
	localTCP.mode, localTCP.ci, localTCP.privateCI, localTCP.localTransport = "local-disposable", "", "", "loopback-tcp"
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
		{"local_data_directory", []driver.Value{filepath.Join(c.root, "data")}, func(ctx context.Context, db *sql.DB) error {
			return verifyDisposableServerDirectory(ctx, db, localTCP)
		}},
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
	t.Run("local_data_directory_requires_exact_server_identity", func(t *testing.T) {
		for _, directory := range []driver.Value{"", "data", c.root + "/data/", c.root + "/data/.", c.root + "/data/../data", "/unrelated/never-log-this", nil} {
			db := sql.OpenDB(fixtureCatalogConnector{[]driver.Value{directory}})
			err := verifyDisposableServerDirectory(context.Background(), db, localTCP)
			db.Close()
			if err == nil || strings.Contains(err.Error(), "never-log-this") || strings.Contains(err.Error(), c.root) {
				t.Fatal("different server directory accepted or identity details echoed")
			}
		}
		db := sql.OpenDB(fixtureCatalogConnector{[]driver.Value{filepath.Join(c.root, "data")}})
		defer db.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := verifyDisposableServerDirectory(ctx, db, localTCP); err == nil || strings.Contains(err.Error(), c.root) {
			t.Fatal("unverified server accepted after read failure")
		}
	})
	t.Run("unix_and_private_ci_keep_existing_transport_identity", func(t *testing.T) {
		// Missing rows would fail if these modes unexpectedly queried the server.
		db := sql.OpenDB(fixtureCatalogConnector{})
		defer db.Close()
		localUnix := c
		localUnix.mode, localUnix.ci, localUnix.privateCI = "local-disposable", "", ""
		for _, config := range []disposableDatabaseConfig{c, localUnix} {
			if err := verifyDisposableServerDirectory(context.Background(), db, config); err != nil {
				t.Fatal("original Unix transport identity changed", err)
			}
		}
	})
}
