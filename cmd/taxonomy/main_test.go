// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaultsAndSyntheticFlags(t *testing.T) {
	c, e := parseConfig(nil)
	if e != nil || c.publicVisibility != nil {
		t.Fatal(c, e)
	}
	c, e = parseConfig([]string{"--listen", "[::1]:0", "--postgres-dsn-env", "FIXTURE_DSN", "--synthetic-public-visibility", "42"})
	if e != nil || *c.publicVisibility != 42 {
		t.Fatal(c, e)
	}
	for _, args := range [][]string{{"--listen", "0.0.0.0:8080"}, {"--listen", "localhost:8080"}, {"--listen", "127.0.0.1:-1"}, {"--listen", "127.0.0.1:65536"}, {"--postgres-dsn-env", "DSN"}, {"--synthetic-authority-file", "fixture.json"}, {"--synthetic-public-visibility", ""}, {"--synthetic-public-visibility", "2147483648"}, {"--synthetic-public-visibility", "0"}, {"--synthetic-public-visibility", "0", "--postgres-dsn-env", "postgres://secret"}, {"--unknown", "private"}, {"positional"}} {
		if _, e := parseConfig(args); e == nil || strings.Contains(e.Error(), "private") {
			t.Fatal(args, e)
		}
	}
}
func TestDefaultHandlerNeverOpensDatabase(t *testing.T) {
	c, _ := parseConfig(nil)
	h, close, e := makeHandler(context.Background(), c, func(string) string { t.Fatal("read environment"); return "" }, func(string, string) (*sql.DB, error) { t.Fatal("opened database"); return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/posts/tags", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
func TestSyntheticConfigSanitizesDatabaseErrors(t *testing.T) {
	c, _ := parseConfig([]string{"--synthetic-public-visibility", "42", "--postgres-dsn-env", "FIXTURE_DSN"})
	_, _, e := makeHandler(context.Background(), c, func(string) string { return "private-secret" }, func(string, string) (*sql.DB, error) { return nil, errors.New("private-secret") })
	if e == nil || strings.Contains(e.Error(), "private-secret") {
		t.Fatal(e)
	}
	_, _, e = makeHandler(context.Background(), c, func(string) string { return "" }, func(string, string) (*sql.DB, error) { t.Fatal("should not open"); return nil, nil })
	if e == nil {
		t.Fatal("empty DSN accepted")
	}
}
func TestAuthorityFixtureParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authority.json")
	for _, v := range []string{`{"actor_id":"bad"}`, `{"unknown":true}`, `{} {}`, `null`, strings.Repeat(" ", 65537) + `{}`} {
		if e := os.WriteFile(path, []byte(v), 0600); e != nil {
			t.Fatal(e)
		}
		_, e := loadAuthority(path)

		if e == nil {
			t.Fatal("accepted", v[:min(len(v), 50)])
		}
	}
	data := `{"actor_id":"10000000-0000-0000-0000-000000000001","grants":["PostsModerate"],"protected_tag_total":2}`
	os.WriteFile(path, []byte(data), 0600)
	a, e := loadAuthority(path)
	if e != nil || a.ProtectedTagTotal != 2 {
		t.Fatal(a, e)
	}
}

func TestAuthorityFixtureSizeIncludesTrailingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authority.json")
	exact := `{}` + strings.Repeat(" ", (64<<10)-2)
	if err := os.WriteFile(path, []byte(exact), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthority(path); err != nil {
		t.Fatal("exact-limit fixture rejected", err)
	}
	for _, tail := range []string{" ", `{}`} {
		if err := os.WriteFile(path, []byte(exact+tail), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadAuthority(path); err == nil {
			t.Fatal("over-limit trailing data accepted")
		}
	}
}
