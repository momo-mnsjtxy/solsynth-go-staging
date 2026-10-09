// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"strings"
	"testing"
	"time"
)

type missingRepository struct{ Repository }
type deniedAuthority struct{}

func (deniedAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func (deniedAuthority) AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func (deniedAuthority) AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}
func TestHTTPRejectsCredentialsSpoofingAndAmbiguousPaths(t *testing.T) {
	s := Service{Repository: missingRepository{}, Authority: deniedAuthority{}}
	for _, c := range []struct {
		path, header, value string
		status              int
	}{{"/api/private/bots", "", "", 401}, {"/api/bots/00000000-0000-4000-8000-000000000001", "Authorization", "", 401}, {"/api/bots/00000000-0000-4000-8000-000000000001", "X-Account-ID", "asserted", 403}, {"/api/private/bots?dev=a&dev=b", "", "", 400}, {"/api/bots/00000000-0000-4000-8000-000000000001?token=x", "", "", 400}, {"/api/private/bots/../bots", "", "", 400}} {
		r := httptest.NewRequest("GET", c.path, nil)
		if c.header != "" {
			r.Header[c.header] = []string{c.value}
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != c.status {
			t.Fatal(c, w.Code, w.Body.String())
		}
	}
}
func TestDecodeRejectsNestedDuplicatesUnknownFieldsAndExecution(t *testing.T) {
	for _, raw := range []string{`{"commands":[{"name":"ping","description":"one","description":"two"}]}`, `{"commands":[{"name":"ping","Name":"other","description":"help"}]}`, `{"commands":[{"name":"ping","description":"help","execute":"sh"}]}`, `{"commands":[]} {"commands":[]}`, `null`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		var v ManifestPatch
		if decode(w, r, &v) == nil {
			t.Fatal("ambiguous or executable extension accepted", raw)
		}
	}
}
func TestBoundedDocumentedCommandsAndMiniManifest(t *testing.T) {
	commands := []develop.BotCommand{{Name: "ping", Description: "Synthetic help", Parameters: []develop.BotCommandParameter{}}}
	if e := ValidateCommands(commands); e != nil {
		t.Fatal(e)
	}
	if e := ValidateCommands(append(commands, commands[0])); e == nil {
		t.Fatal("duplicate command")
	}
	commands[0].Parameters = make([]develop.BotCommandParameter, 33)
	if e := ValidateCommands(commands); e == nil {
		t.Fatal("parameter bound")
	}
	var p MiniPatch
	if e := json.Unmarshal([]byte(`{"slug":"synthetic-plugin","manifest":{"id":"test.synthetic.plugin","name":"Synthetic plugin"}}`), &p); e != nil {
		t.Fatal(e)
	}
	if p.Manifest.Version != "1.0.0" || p.Manifest.Entry != "main.js" || ValidateMini(p, true) != nil {
		t.Fatal("documented defaults", p)
	}
	p.Manifest.Entry = "../escape.js"
	if ValidateMini(p, true) == nil {
		t.Fatal("unsafe entry")
	}
}

func TestExecutionRoutesRequireCanonicalAuthentication(t *testing.T) {
	s := Service{Repository: missingRepository{}, Authority: deniedAuthority{}}
	for _, p := range []string{"/api/chat/00000000-0000-4000-8000-000000000001/bots/commands", "/api/private/bots/00000000-0000-4000-8000-000000000001/execution/00000000-0000-4000-8000-000000000002?dev=test&proj=00000000-0000-4000-8000-000000000003&app=00000000-0000-4000-8000-000000000004"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 401 {
			t.Fatal(p, w.Code)
		}
	}
	if RoomCommandsRoute("/api/chat/room/bots/commands/execute") {
		t.Fatal("invented dispatch route")
	}
}
func TestExecutionConfigurationBounds(t *testing.T) {
	good := ExecutionConfig{NoneEncryptionMode: 77, SendTimeout: time.Second, EventLifetime: time.Minute, MaxAttempts: 3, MaxPendingPerRoom: 100}
	if e := good.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*ExecutionConfig){func(c *ExecutionConfig) { c.SendTimeout = 0 }, func(c *ExecutionConfig) { c.EventLifetime = 25 * time.Hour }, func(c *ExecutionConfig) { c.MaxAttempts = 0 }, func(c *ExecutionConfig) { c.MaxPendingPerRoom = 10001 }} {
		bad := good
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("unbounded execution configuration")
		}
	}
	for _, s := range []string{"https://synthetic.invalid", "foo/bar", "UPPER", "", " space"} {
		if ValidAdapterName(s) {
			t.Fatal("user destination accepted", s)
		}
	}
}
