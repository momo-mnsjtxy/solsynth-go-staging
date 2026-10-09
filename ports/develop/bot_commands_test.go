// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

type botCommandsFixtureReader struct {
	commands []BotCommand
	err      error
	calls    int
	id       string
}

func (r *botCommandsFixtureReader) ReadBotCommands(ctx context.Context, id string) ([]BotCommand, error) {
	r.calls++
	r.id = id
	return r.commands, r.err
}

const botFixtureID = "ab000000-0000-0000-0000-000000000001"

func TestBotCommandsDocumentedModelFixture(t *testing.T) {
	data, err := os.ReadFile("../../inventory/product-client-evidence/bot-command-oracle/results.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Input    string
		Accepted bool
		Output   *string
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 19 {
		t.Fatal("incomplete synthetic fixture")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			commands, err := DecodeSyntheticBotCommands([]byte(tc.Input))
			if !tc.Accepted {
				if !errors.Is(err, ErrBotCommandsUnsupportedJSON) {
					t.Fatal(err, commands)
				}
				return
			}
			if err != nil || commands == nil || tc.Output == nil {
				t.Fatal(err, commands)
			}
			got, err := json.Marshal(commands)
			if err != nil {
				t.Fatal(err)
			}
			var wantJSON, gotJSON any
			if err = json.Unmarshal([]byte(*tc.Output), &wantJSON); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(got, &gotJSON); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wantJSON, gotJSON) {
				t.Fatalf("documented fixture semantics differ: got %s want %s", got, *tc.Output)
			}
		})
	}
	if _, err := DecodeSyntheticBotCommands([]byte(strings.Repeat(" ", MaxSyntheticBotCommandsJSONBytes+1))); !errors.Is(err, ErrBotCommandsUnsupportedJSON) {
		t.Fatal(err)
	}
	for _, input := range []string{"", "1", "true", "[] []", "[", `[{"name":"n","description":null}]`, `[{"name":"n","description":"d","parameters":{}}]`} {
		if _, err := DecodeSyntheticBotCommands([]byte(input)); !errors.Is(err, ErrBotCommandsUnsupportedJSON) {
			t.Fatal(input, err)
		}
	}
}

func TestBotCommandsServicePolicyAndGuards(t *testing.T) {
	reader := &botCommandsFixtureReader{}
	for _, service := range []*BotCommandsService{nil, {}, NewBotCommandsService(reader)} {
		if _, err := service.Read(context.Background(), botFixtureID); !errors.Is(err, ErrBotCommandsPolicyUnverified) || reader.calls != 0 {
			t.Fatal(err, reader.calls)
		}
	}
	svc := NewSyntheticCompatibilityBotCommandsService(reader)
	for _, id := range []string{"", "not-a-uuid", "ab000000000000000000000000000001", "{ab000000-0000-0000-0000-000000000001}", "gg000000-0000-0000-0000-000000000001"} {
		if _, err := svc.Read(context.Background(), id); !errors.Is(err, ErrBotCommandsInvalidID) {
			t.Fatal(id, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Read(ctx, botFixtureID); !errors.Is(err, context.Canceled) || reader.calls != 0 {
		t.Fatal(err, reader.calls)
	}
	commands, err := svc.Read(context.Background(), strings.ToUpper(botFixtureID))
	if err != nil || commands == nil || len(commands) != 0 || reader.id != botFixtureID {
		t.Fatal(err, commands, reader.id)
	}
	if _, err := NewSyntheticCompatibilityBotCommandsService(nil).Read(context.Background(), botFixtureID); !errors.Is(err, ErrBotCommandsUnavailable) {
		t.Fatal(err)
	}
}

func TestBotCommandsHTTPGuardsAndPayload(t *testing.T) {
	reader := &botCommandsFixtureReader{commands: []BotCommand{{Name: "help", Description: "Help", Parameters: []BotCommandParameter{}}}}
	svc := NewSyntheticCompatibilityBotCommandsService(reader)
	route := "/api/bots/" + botFixtureID + "/commands"
	for _, handler := range []http.Handler{NewBotCommandsHandler(svc), NewSyntheticCompatibilityBotCommandsHandler(NewBotCommandsService(reader)), NewSyntheticCompatibilityBotCommandsHandler(nil)} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 503 || reader.calls != 0 {
			t.Fatal(w.Code, reader.calls)
		}
	}
	handler := NewSyntheticCompatibilityBotCommandsHandler(svc)
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/bots/public/" + botFixtureID + "/commands", 404}, {"GET", "/api/bots/bad/commands", 404}, {"GET", route + "/", 404}, {"HEAD", route, 405}, {"POST", route, 405}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || reader.calls != 0 {
			t.Fatal(tc, w.Code, reader.calls)
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Test-Account", "authorization"} {
		r := httptest.NewRequest("GET", route, nil)
		r.Header[header] = nil
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 || reader.calls != 0 {
			t.Fatal(header, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", route+"?ignored=1", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `[{"name":"help","description":"Help","usage":null,"parameters":[]}]` || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("X-Total") != "" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, tc := range []struct {
		err    error
		status int
		body   string
	}{{ErrBotNotFound, 404, `{"code":"DEV_BOT_ACCOUNT_NOT_FOUND","message":"Bot not found","status":404}`}, {errors.New("fixture-secret-dsn"), 503, `{"error":"bot_commands_unavailable"}`}} {
		reader.err = tc.err
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
