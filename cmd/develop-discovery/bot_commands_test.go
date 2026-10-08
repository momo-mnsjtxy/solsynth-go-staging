// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
)

func TestBotCommandsRequireIndependentOptIn(t *testing.T) {
	if _, err := parseConfig([]string{"--synthetic-bot-commands"}); err == nil {
		t.Fatal("accepted bot commands without DB opt-in")
	}
	cfg, err := parseConfig([]string{"--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN", "--synthetic-bot-commands"})
	if err != nil || !cfg.syntheticBotCommands {
		t.Fatal(cfg, err)
	}
	cfg, _ = parseConfig(nil)
	h, closeDB, err := makeHandler(context.Background(), cfg, func(string) string { t.Fatal("default read DSN"); return "" }, func(string, string) (*sql.DB, error) { t.Fatal("default opened DB"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/bots/a0000000-0000-0000-0000-000000000001/commands", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
