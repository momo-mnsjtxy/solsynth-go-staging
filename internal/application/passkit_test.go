// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/passport/passkit"
)

type passkitHostRepository struct {
	bundle       passkit.Bundle
	actor, token string
	calls        int
}

func (r *passkitHostRepository) Member(_ context.Context, actor string) (passkit.Bundle, error) {
	r.actor = actor
	r.calls++
	return r.bundle, nil
}
func (r *passkitHostRepository) Latest(_ context.Context, typ, serial, token string) (passkit.Bundle, error) {
	r.token = token
	r.calls++
	return r.bundle, nil
}
func (r *passkitHostRepository) Register(_ context.Context, typ, serial, token, device, push string) (bool, error) {
	r.token = token
	r.calls++
	return true, nil
}
func (r *passkitHostRepository) Unregister(_ context.Context, typ, serial, token, device string) error {
	r.token = token
	r.calls++
	return nil
}
func (r *passkitHostRepository) Updates(_ context.Context, typ, device, token, since string) (passkit.Serials, error) {
	r.token = token
	r.calls++
	return passkit.Serials{SerialNumbers: []string{hostAccountA}, LastUpdated: "1"}, nil
}
func TestPasskitCanonicalHostAndAppleProtocol(t *testing.T) {
	issuer, v, identities := hostIdentity(t)
	bearer := hostToken(t, issuer, hostAccountA, "*")
	f := testpasskit.New(t)
	g, e := passkit.NewGenerator(f.Config)
	if e != nil {
		t.Fatal(e)
	}
	files, _, e := g.Files(passkit.Member{ID: hostAccountA, Name: "synthetic", CreatedAt: time.Now()}, strings.Repeat("a", 48))
	if e != nil {
		t.Fatal(e)
	}
	data, e := g.Generate(context.Background(), files)
	if e != nil {
		t.Fatal(e)
	}
	repo := &passkitHostRepository{bundle: passkit.Bundle{Data: data, Tag: "1", UpdatedAt: time.Now()}}
	var logs bytes.Buffer
	service := (&passkit.Service{Repository: repo, Authority: proofIdentity{identity{v}}, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}).Handler()
	h := router{modules{passkit: passkitModules{member: bindIdentity(v, service), callbacks: service}}}
	w := hostRequest(h, "GET", "/api/accounts/me/passbook/member", bearer, "")
	if w.Code != 200 || repo.actor != hostAccountA || w.Header().Get("Content-Type") != "application/vnd.apple.pkpass" || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal(w.Code, repo.actor)
	}
	before := repo.calls
	if w = hostRequest(h, "GET", "/api/accounts/me/passbook/member", "", ""); w.Code != 401 || repo.calls != before {
		t.Fatal("anonymous member", w.Code)
	}
	identities.accounts[hostAccountA].account.Disabled = true
	if w = hostRequest(h, "GET", "/api/accounts/me/passbook/member", bearer, ""); w.Code != 423 || repo.calls != before {
		t.Fatal("disabled member", w.Code)
	}
	call := func(method, path, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const typ = "pass.example.synthetic.member"
	url := "/passport/passkit/v1/passes/" + typ + "/" + hostAccountA
	if w = call("GET", url, "ApplePass "+strings.Repeat("b", 48), ""); w.Code != 200 || repo.token != strings.Repeat("b", 48) {
		t.Fatal("ApplePass bearer routing", w.Code)
	}
	updates := "/api/passkit/v1/devices/synthetic-device/registrations/" + typ
	if w = call("GET", updates, "", ""); w.Code != 200 || repo.token != "" || !bytes.Contains(w.Body.Bytes(), []byte(`"serialNumbers"`)) {
		t.Fatal("device protocol", w.Code, w.Body.String())
	}
	before = repo.calls
	if w = call("GET", updates, "Bearer invalid", ""); w.Code != 401 || repo.calls != before {
		t.Fatal("invalid header downgraded")
	}
	register := updates + "/" + hostAccountA
	for _, body := range []string{`{"pushToken":"a","pushToken":"b"}`, `{"pushToken":"a","push_token":"b"}`, `{"pushToken":"a"} {}`, `{"pushToken":""}`, `{"unknown":"a"}`} {
		if w = call("POST", register, "ApplePass "+strings.Repeat("b", 48), body); w.Code != 400 || repo.calls != before {
			t.Fatal("malformed registration", body, w.Code)
		}
	}
	if w = call("POST", "/api/passkit/v1/log", "", `{"logs":["private-opaque-value"]}`); w.Code != 200 || !strings.Contains(logs.String(), `"entries":1`) || strings.Contains(logs.String(), "private-opaque-value") {
		t.Fatal("diagnostics record or redaction", w.Code, logs.String())
	}
	defaultService := (&passkit.Service{}).Handler()
	w = httptest.NewRecorder()
	defaultService.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/passkit/v1", nil))
	if w.Code != 503 {
		t.Fatal("default signing was enabled")
	}
}

func TestPasskitRequiresCanonicalSamePool(t *testing.T) {
	visibility := int32(0)
	_, e := NewPostgres(new(sql.DB), new(sql.DB), Config{PublicVisibility: &visibility, MessagerPolicy: &messagerpg.IndependentPolicy{}, AppleWallet: &passkit.Config{}})
	if e == nil || e.Error() != "Apple Wallet requires canonical same-pool authority" {
		t.Fatal("separate pool accepted", e)
	}
}
