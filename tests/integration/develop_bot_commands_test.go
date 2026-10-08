// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/developpg"
	"solsynth.local/go-migration/ports/develop"
)

func botCommandsHandler(db *sql.DB) http.Handler {
	return develop.NewSyntheticCompatibilityBotCommandsHandler(develop.NewSyntheticCompatibilityBotCommandsService(developpg.NewSyntheticCompatibilityBotCommandsRepository(db)))
}
func botInsert(t *testing.T, db *sql.DB, id, project string, active, deleted bool) {
	t.Helper()
	query(t, db, `INSERT INTO bot_accounts(id,slug,is_active,project_id,deleted_at)VALUES($1,'fixture-bot',$2,$3,CASE WHEN $4 THEN now() ELSE NULL END)`, id, active, project, deleted)
}
func botCommandConfig(t *testing.T, db *sql.DB, id, commands string, deleted bool) {
	t.Helper()
	query(t, db, `INSERT INTO bot_chat_configs(id,commands,deleted_at)VALUES($1,$2::jsonb,CASE WHEN $3 THEN now() ELSE NULL END)`, id, commands, deleted)
}
func botCommandRoute(id string) string { return "/api/bots/" + id + "/commands" }

func TestPGDevelopBotCommandsPayloads(t *testing.T) {
	db := database(t)
	project := packageProject(t, db, 0xa9000000)
	id := func(n int) string { return watcherID(0xa9000000, n) }
	handler := botCommandsHandler(db)
	for n := 10; n <= 14; n++ {
		botInsert(t, db, id(n), project, n != 14, false)
	}
	// Missing config, JSON null, and empty arrays all follow source [] fallback.
	botCommandConfig(t, db, id(11), "null", false)
	botCommandConfig(t, db, id(12), "[]", false)
	for n := 10; n <= 12; n++ {
		w := developRequest(handler, botCommandRoute(id(n)))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Fatal(n, w.Code, w.Body.String())
		}
	}
	fixture := `[{"name":"help","description":"Help","usage":"/help [topic]","parameters":[{"name":"topic","required":false},{"name":"count","description":"Count","required":true,"type":"int"}]},{"name":"empty","description":"","parameters":[]},{"name":"nil","description":"Null parameters","parameters":null}]`
	botCommandConfig(t, db, id(13), fixture, false)
	botCommandConfig(t, db, id(14), fixture, false)
	for n := 13; n <= 14; n++ {
		w := developRequest(handler, botCommandRoute(strings.ToUpper(id(n))))
		if w.Code != 200 || w.Header().Get("X-Total") != "" {
			t.Fatal(w.Code, w.Body.String())
		}
		var commands []develop.BotCommand
		if err := json.Unmarshal(w.Body.Bytes(), &commands); err != nil {
			t.Fatal(err)
		}
		if len(commands) != 3 || commands[0].Name != "help" || len(commands[0].Parameters) != 2 || commands[0].Parameters[0].Required || commands[0].Parameters[0].Description != nil || commands[0].Parameters[0].Type != nil || !commands[0].Parameters[1].Required || commands[1].Parameters == nil || len(commands[1].Parameters) != 0 || commands[2].Parameters != nil {
			t.Fatalf("null/order/active candidate contract: %+v", commands)
		}
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw[0]) != 4 || string(raw[1]["parameters"]) != "[]" || string(raw[2]["parameters"]) != "null" {
			t.Fatal(w.Body.String())
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM bot_chat_configs WHERE id=$1`, id(10)).Scan(&count); err != nil || count != 0 {
		t.Fatal("read created missing config", count, err)
	}
	w := developRequest(handler, botCommandRoute(id(99)))
	if w.Code != 404 || strings.TrimSpace(w.Body.String()) != `{"code":"DEV_BOT_ACCOUNT_NOT_FOUND","message":"Bot not found","status":404}` {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPGDevelopBotCommandsPolicyAndErrors(t *testing.T) {
	db := database(t)
	project := packageProject(t, db, 0xaa000000)
	id := func(n int) string { return watcherID(0xaa000000, n) }
	botInsert(t, db, id(10), project, true, false)
	botCommandConfig(t, db, id(10), `[{"name":"help","description":"Help"}]`, false)
	repo := developpg.NewSyntheticCompatibilityBotCommandsRepository(db)
	svc := develop.NewSyntheticCompatibilityBotCommandsService(repo)
	for _, h := range []http.Handler{develop.NewBotCommandsHandler(svc), develop.NewSyntheticCompatibilityBotCommandsHandler(develop.NewBotCommandsService(repo)), develop.NewSyntheticCompatibilityBotCommandsHandler(develop.NewSyntheticCompatibilityBotCommandsService(developpg.NewBotCommandsRepository(db)))} {
		if w := developRequest(h, botCommandRoute(id(10))); w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	h := botCommandsHandler(db)
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account"} {
		r := httptest.NewRequest("GET", botCommandRoute(id(10)), nil)
		r.Header[header] = []string{"synthetic"}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	botInsert(t, db, id(11), project, true, true)
	botCommandConfig(t, db, id(11), `[]`, false)
	if w := developRequest(h, botCommandRoute(id(11))); w.Code != 404 {
		t.Fatal(w.Code)
	}
	deletedProject := id(3)
	query(t, db, `INSERT INTO dev_projects(id,developer_id,deleted_at)VALUES($1,$2,now())`, deletedProject, id(1))
	botInsert(t, db, id(12), deletedProject, true, false)
	if w := developRequest(h, botCommandRoute(id(12))); w.Code != 404 {
		t.Fatal(w.Code)
	}
	botInsert(t, db, id(13), project, true, false)
	botCommandConfig(t, db, id(13), `[{"name":"hidden","description":"Deleted config"}]`, true)
	if w := developRequest(h, botCommandRoute(id(13))); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
	for n, bad := range []string{`[{"name":"help","description":"Help","extra":"private-fixture"}]`, `[{"Name":"help","Description":"Help"}]`, `[{"name":"help"}]`, `[{"name":"help","description":"Help","parameters":[{"name":"x","required":null}]}]`, `[null]`, `{"commands":[]}`} {
		botInsert(t, db, id(20+n), project, true, false)
		botCommandConfig(t, db, id(20+n), bad, false)
		w := developRequest(h, botCommandRoute(id(20+n)))
		if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"bot_commands_unavailable"}` {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	readonly := database(t)
	readonly.SetMaxOpenConns(1)
	readonly.SetMaxIdleConns(1)
	query(t, readonly, `SET default_transaction_read_only=on`)
	if w := developRequest(botCommandsHandler(readonly), botCommandRoute(id(10))); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.ReadBotCommands(ctx, id(10)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	closed := database(t)
	closed.Close()
	if w := developRequest(botCommandsHandler(closed), botCommandRoute(id(10))); w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"error":"bot_commands_unavailable"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	var configs int
	if err := db.QueryRow(`SELECT count(*) FROM bot_chat_configs WHERE id=$1`, id(10)).Scan(&configs); err != nil || configs != 1 {
		t.Fatal(configs, err)
	}
}

func TestPGDevelopBotCommandsRestartPersistence(t *testing.T) {
	db := database(t)
	id := watcherID(0xab000000, 10)
	phase := os.Getenv("PG_TEST_PHASE")
	if phase == "before" {
		project := packageProject(t, db, 0xab000000)
		botInsert(t, db, id, project, false, false)
		botCommandConfig(t, db, id, `[{"name":"persist","description":"Persisted command","usage":null,"parameters":[]}]`, false)
	} else if phase != "after" {
		t.Fatal("missing restart phase")
	}
	w := developRequest(botCommandsHandler(db), botCommandRoute(id))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `[{"name":"persist","description":"Persisted command","usage":null,"parameters":[]}]` {
		t.Fatal(w.Code, w.Body.String())
	}
}
