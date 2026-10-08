// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigDefaultsAndExplicitSyntheticMode(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.listen != "127.0.0.1:8081" || cfg.syntheticProductionStatus != nil || cfg.dsnEnv != "" {
		t.Fatalf("default config %#v, %v", cfg, err)
	}
	for _, status := range []string{"0", "42", "-2147483648", "2147483647"} {
		cfg, err := parseConfig([]string{"--listen", "[::1]:0", "--postgres-dsn-env", "PG_TEST_DSN", "--synthetic-production-status", status})
		if err != nil || cfg.syntheticProductionStatus == nil || cfg.dsnEnv != "PG_TEST_DSN" {
			t.Fatalf("status %s: %#v %v", status, cfg, err)
		}
	}
}

func TestRejectInvalidConfigurationWithoutLeakingInput(t *testing.T) {
	for _, args := range [][]string{
		{"--listen", "0.0.0.0:8080"}, {"--listen", "[::]:8080"},
		{"--listen", "192.0.2.1:8080"}, {"--listen", "localhost:8080"},
		{"--listen", "127.0.0.1:65536"}, {"--listen", "127.0.0.1:-1"},
		{"--listen", "127.0.0.1:http"}, {"--listen", "127.0.0.1:"},
		{"--listen", "127.0.0.1:+80"}, {"--listen", "sensitive-secret"},
		{"--postgres-dsn-env", "PG_TEST_DSN"},
		{"--synthetic-production-status", "0"},
		{"--synthetic-production-status", ""},
		{"--synthetic-production-status", "2147483648", "--postgres-dsn-env", "PG_TEST_DSN"},
		{"--synthetic-production-status", "-2147483649", "--postgres-dsn-env", "PG_TEST_DSN"},
		{"--synthetic-production-status", "sensitive-secret"},
		{"--synthetic-production-status", "0", "--postgres-dsn-env", "postgres://sensitive-secret@localhost/db"},
		{"--synthetic-production-status", "0", "--postgres-dsn-env", "9BAD_ENV"},
		{"--synthetic-production-status", "0", "--postgres-dsn-env", "ENV\nBAD"},
		{"--unknown=sensitive-secret"}, {"sensitive-secret"},
	} {
		_, err := parseConfig(args)
		if err == nil {
			t.Fatalf("accepted invalid configuration: %q", args)
		}
		if strings.Contains(err.Error(), "sensitive-secret") {
			t.Fatal("configuration error leaked supplied input")
		}
	}
}

func TestDefaultDiscoveryRequiresNoDatabaseAndIgnoresIdentityClaims(t *testing.T) {
	cfg, _ := parseConfig(nil)
	handler, closeDB, err := makeHandler(context.Background(), cfg,
		func(string) string { t.Fatal("default configuration read a DSN"); return "" },
		func(string, string) (*sql.DB, error) { t.Fatal("default configuration opened a DB"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	for _, header := range []string{"", "Authorization", "X-Test-Account", "X-Account-ID"} {
		r := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
		if header != "" {
			r.Header.Set(header, "claimed-identity")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("header %s: expected fail-closed 503, got %d", header, w.Code)
		}
	}
}

func TestAddressValidationBeforeDSNOrDatabase(t *testing.T) {
	status := int32(42)
	_, _, err := makeHandler(context.Background(), config{listen: "0.0.0.0:8080", dsnEnv: "PG_TEST_DSN", syntheticProductionStatus: &status},
		func(string) string { t.Fatal("read DSN before address validation"); return "" },
		func(string, string) (*sql.DB, error) { t.Fatal("opened DB before address validation"); return nil, nil })
	if err == nil {
		t.Fatal("accepted non-loopback address")
	}
}

func TestDatabaseErrorsDoNotExposeConnectionData(t *testing.T) {
	cfg, err := parseConfig([]string{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN"})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "postgres://sensitive-secret@localhost/synthetic"
	getenv := func(name string) string {
		if name != "PG_TEST_DSN" {
			t.Fatal("wrong environment variable")
		}
		return secret
	}
	_, _, err = makeHandler(context.Background(), cfg, func(string) string { return "" }, func(string, string) (*sql.DB, error) {
		t.Fatal("empty DSN must not fall back to PostgreSQL environment defaults")
		return nil, nil
	})
	if err == nil {
		t.Fatal("empty DSN accepted")
	}
	_, _, err = makeHandler(context.Background(), cfg, getenv, func(name, dsn string) (*sql.DB, error) {
		if name != "pgx" || dsn != secret {
			t.Fatal("wrong database open arguments")
		}
		return nil, errors.New(secret)
	})
	if err == nil || strings.Contains(err.Error(), "sensitive-secret") {
		t.Fatalf("database open error was not suppressed: %v", err)
	}
	conn := &failingPingConn{err: errors.New(secret)}
	db := sql.OpenDB(failingPingConnector{conn: conn})
	_, _, err = makeHandler(context.Background(), cfg, getenv, func(string, string) (*sql.DB, error) { return db, nil })
	if err == nil || strings.Contains(err.Error(), "sensitive-secret") || !conn.closed {
		t.Fatalf("ping failure handling: err %v closed %v", err, conn.closed)
	}
}

// A failure-only SQL test double verifies startup cleanup and error redaction;
// it cannot execute queries or stand in for successful repository behavior.
type failingPingConn struct {
	err    error
	closed bool
}

func (c *failingPingConn) Prepare(string) (driver.Stmt, error) { return nil, c.err }
func (c *failingPingConn) Begin() (driver.Tx, error)           { return nil, c.err }
func (c *failingPingConn) Close() error                        { c.closed = true; return nil }
func (c *failingPingConn) Ping(context.Context) error          { return c.err }

type failingPingConnector struct{ conn *failingPingConn }

func (c failingPingConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c failingPingConnector) Driver() driver.Driver                        { return failingPingDriver{} }

type failingPingDriver struct{}

func (failingPingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("test driver has no direct open")
}
