// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultHostNeverReadsOrOpensDatabase(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.listen != "127.0.0.1:8085" {
		t.Fatal(cfg, err)
	}
	h, closeDB, err := makeHandler(context.Background(), cfg, func(string) string { t.Fatal("read DSN"); return "" }, func(string, string) (*sql.DB, error) { t.Fatal("opened DB"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/chat/00000000-0000-0000-0000-000000000001/sync", nil))
	if w.Code != 503 {
		t.Fatal(w)
	}
}
func TestSyntheticHostRequiresExplicitOptions(t *testing.T) {
	for _, args := range [][]string{{"--listen=0.0.0.0:80"}, {"--listen=localhost:80"}, {"--listen=127.0.0.1:+80"}, {"--listen=127.0.0.1:65536"}, {"--postgres-dsn-env=PG_TEST_DSN"}, {"--synthetic-account=00000000-0000-0000-0000-000000000001"}, {"--synthetic-projection"}, {"--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN"}, {"--synthetic-projection", "--postgres-dsn-env=password=secret", "--synthetic-none-encryption-mode=77"}, {"--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-none-encryption-mode=2147483648"}, {"secret-positional"}} {
		if _, err := parseConfig(args); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(args, err)
		}
	}
	cfg, err := parseConfig([]string{"--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-none-encryption-mode=77", "--synthetic-account=00000000-0000-0000-0000-000000000001", "--listen=[::1]:0"})
	if err != nil || cfg.noneMode == nil || *cfg.noneMode != 77 {
		t.Fatal(cfg, err)
	}
	_, _, err = makeHandler(context.Background(), cfg, func(string) string { return "" }, func(string, string) (*sql.DB, error) { t.Fatal("opened empty DSN"); return nil, nil })
	if err == nil {
		t.Fatal("missing DSN")
	}
	_, _, err = makeHandler(context.Background(), cfg, func(string) string { return "password=secret" }, func(string, string) (*sql.DB, error) { return nil, errors.New("password=secret") })
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
func TestUnreadMappingsAreExplicitAndIndependent(t *testing.T) {
	base := []string{"--synthetic-projection", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-none-encryption-mode=77"}
	for _, extra := range [][]string{{"--synthetic-notify-all=11"}, {"--synthetic-notify-all=11", "--synthetic-notify-mentions=11", "--synthetic-notify-none=33"}, {"--synthetic-notify-all=2147483648", "--synthetic-notify-mentions=22", "--synthetic-notify-none=33"}} {
		if _, err := parseConfig(append(append([]string{}, base...), extra...)); err == nil {
			t.Fatal(extra)
		}
	}
	cfg, err := parseConfig(append(base, "--synthetic-notify-all=11", "--synthetic-notify-mentions=22", "--synthetic-notify-none=33"))
	if err != nil || cfg.unreadPolicy == nil || cfg.unreadPolicy.Mentions != 22 {
		t.Fatal(cfg, err)
	}
}
