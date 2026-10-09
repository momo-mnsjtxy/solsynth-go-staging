// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/messager"
	"strings"
	"testing"
	"time"
)

type missingRepository struct{ Repository }
type deniedAuthority struct{}

func (deniedAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{}, authority.ErrInvalid
}

type keyReplyRepository struct {
	Repository
	keyAction       string
	replyKey        string
	executionAction string
	renewalExpiry   time.Time
	policyAction    string
	policyRequest   *RenewalPolicyRequest
}

func (r *keyReplyRepository) Keys(_ context.Context, _ apps.Scope, bot, ref string, request *KeyRequest, action string) ([]Key, error) {
	r.keyAction = action
	secret := "bk1.synthetic"
	return []Key{{ID: uploadID, BotID: bot, Label: requestLabel(request), Secret: &secret}}, nil
}
func requestLabel(request *KeyRequest) string {
	if request == nil {
		return ""
	}
	return request.Label
}
func (r *keyReplyRepository) Reply(_ context.Context, _ string, key string, request ReplyRequest) (messager.MessageProjection, error) {
	r.replyKey = key
	return messager.MessageProjection{ID: uploadID, ChatRoomID: request.RoomID}, nil
}
func (r *keyReplyRepository) Enrollment(_ context.Context, _ apps.Scope, bot, room string, request *EnrollmentRequest, action string) (Enrollment, error) {
	r.executionAction = action
	if request != nil {
		r.renewalExpiry = request.ExpiresAt
	}
	return Enrollment{BotID: bot, RoomID: room, Active: true, ExpiresAt: r.renewalExpiry}, nil
}
func (r *keyReplyRepository) RoomCommands(context.Context, string, string) (map[string][]develop.BotCommand, error) {
	return map[string][]develop.BotCommand{}, nil
}
func (r *keyReplyRepository) RenewalPolicy(_ context.Context, _ apps.Scope, bot, room string, request *RenewalPolicyRequest, action string) (RenewalPolicy, error) {
	r.policyAction, r.policyRequest = action, request
	value := RenewalPolicy{BotID: bot, RoomID: room, Active: action != "delete"}
	if request != nil {
		value.ExtendBySeconds, value.RenewBeforeSeconds, value.ConsentExpiresAt = request.ExtendBySeconds, request.RenewBeforeSeconds, request.ConsentExpiresAt
	}
	return value, nil
}

type allowedAuthority struct{}

func (allowedAuthority) AuthenticateRequest(*http.Request) (authority.Principal, error) {
	return authority.Principal{Account: authority.Account{ID: uploadID}}, nil
}
func (allowedAuthority) AuthenticatePrincipal(_ context.Context, p authority.Principal) (authority.Principal, error) {
	return p, nil
}
func (allowedAuthority) AuthorizePrincipal(_ context.Context, p authority.Principal, _ ...string) (authority.Principal, error) {
	return p, nil
}

func TestBotKeyCRUDAndCredentialReplyHTTP(t *testing.T) {
	repo := &keyReplyRepository{}
	service := Service{Repository: repo, Authority: allowedAuthority{}}
	base := "/api/private/bots/" + uploadID + "/keys?dev=synthetic&proj=" + uploadID + "&app=" + uploadID
	request := httptest.NewRequest("POST", base, strings.NewReader(`{"label":"automation"}`))
	request.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	service.Handler().ServeHTTP(w, request)
	if w.Code != 200 || repo.keyAction != "create" || !strings.Contains(w.Body.String(), `"secret":"bk1.synthetic"`) {
		t.Fatal(w.Code, w.Body.String(), repo.keyAction)
	}

	reply := httptest.NewRequest("POST", "/api/bots/"+uploadID+"/reply", strings.NewReader(`{"room_id":"`+uploadID+`","content":"pong"}`))
	reply.Header.Set("X-Api-Key", "bk1.synthetic")
	w = httptest.NewRecorder()
	service.Handler().ServeHTTP(w, reply)
	if w.Code != 201 || repo.replyKey != "bk1.synthetic" || !strings.Contains(w.Body.String(), `"chat_room_id":"`+uploadID+`"`) {
		t.Fatal(w.Code, w.Body.String(), repo.replyKey)
	}

	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("X-Api-Key") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer mixed") },
		func(r *http.Request) { r.Header.Add("X-Api-Key", "second") },
	} {
		r := httptest.NewRequest("POST", "/api/bots/"+uploadID+"/reply", strings.NewReader(`{"room_id":"`+uploadID+`","content":"pong"}`))
		r.Header.Set("X-Api-Key", "bk1.synthetic")
		mutate(r)
		w = httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("ambiguous bot identity", w.Code)
		}
	}
}

func TestExecutionRenewalHTTP(t *testing.T) {
	repo := &keyReplyRepository{}
	service := Service{Repository: repo, Authority: allowedAuthority{}}
	expires := "2026-10-10T00:00:00Z"
	path := "/api/private/bots/" + uploadID + "/execution/" + uploadID + "/renew?dev=synthetic&proj=" + uploadID + "&app=" + uploadID
	request := httptest.NewRequest("POST", path, strings.NewReader(`{"expires_at":"`+expires+`"}`))
	request.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	service.Handler().ServeHTTP(w, request)
	if w.Code != 200 || repo.executionAction != "renew" || repo.renewalExpiry.Format(time.RFC3339) != expires {
		t.Fatal(w.Code, w.Body.String(), repo.executionAction, repo.renewalExpiry)
	}
}

func TestExecutionAutomaticRenewalPolicyHTTP(t *testing.T) {
	repo := &keyReplyRepository{}
	service := Service{Repository: repo, Authority: allowedAuthority{}}
	path := "/api/private/bots/" + uploadID + "/execution/" + uploadID + "/renewal?dev=synthetic&proj=" + uploadID + "&app=" + uploadID
	request := httptest.NewRequest("PUT", path, strings.NewReader(`{"extend_by_seconds":600,"renew_before_seconds":120,"consent_expires_at":"2026-10-10T00:00:00Z"}`))
	request.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	service.Handler().ServeHTTP(w, request)
	if w.Code != 200 || repo.policyAction != "set" || repo.policyRequest == nil || repo.policyRequest.ExtendBySeconds != 600 || repo.policyRequest.RenewBeforeSeconds != 120 {
		t.Fatal(w.Code, w.Body.String(), repo.policyAction, repo.policyRequest)
	}
	request = httptest.NewRequest("DELETE", path, nil)
	request.Header.Set("Authorization", "Bearer synthetic")
	w = httptest.NewRecorder()
	service.Handler().ServeHTTP(w, request)
	if w.Code != 204 || repo.policyAction != "delete" {
		t.Fatal(w.Code, w.Body.String(), repo.policyAction)
	}
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
	mls := good
	mls.EnableMLSOpaque = true
	mls.MLSEncryptionMode = 3
	if e := mls.Validate(); e != nil {
		t.Fatal("valid opaque MLS configuration", e)
	}
	mls.MLSEncryptionMode = mls.NoneEncryptionMode
	if mls.Validate() == nil {
		t.Fatal("ambiguous MLS mode accepted")
	}
	for _, s := range []string{"https://synthetic.invalid", "foo/bar", "UPPER", "", " space"} {
		if ValidAdapterName(s) {
			t.Fatal("user destination accepted", s)
		}
	}
}
