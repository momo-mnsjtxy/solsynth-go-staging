package accounts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type authFixture struct {
	principal Principal
	err       error
}

func (a authFixture) Authenticate(*http.Request) (Principal, error) { return a.principal, a.err }
func TestHTTPRecorderContractsAndFailClosed(t *testing.T) {
	s, r, _, _, p := serviceFixture()
	for _, auth := range []Authenticator{nil, authFixture{err: ErrUnauthorized}, authFixture{principal: Principal{AccountID: accountA}}} {
		h := NewHandler(s, auth)
		request := httptest.NewRequest("GET", "/api/wallets", nil)
		request.Header.Set("X-Test-Account", accountA)
		request.Header.Set("X-Account-ID", accountA)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, request)
		if out.Code != 401 && out.Code != 503 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	h := NewHandler(s, authFixture{principal: p})
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets", nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"pockets":[]`) || !strings.Contains(out.Body.String(), `"realm_id":null`) {
		t.Fatal(out.Code, out.Body.String())
	}
	body := `{"amount":9007199254740993.1239,"currency":"points","payee_wallet_id":"` + walletB + `","pin_code":null}`
	request := httptest.NewRequest("POST", "/api/wallets/transfer", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "synthetic-repeat")
	out = httptest.NewRecorder()
	h.ServeHTTP(out, request)
	if out.Code != 200 || r.last.Amount.String() != "9007199254740993.123" {
		t.Fatal(out.Code, out.Body.String())
	}
	parseAuthoredClientTransaction(t, out.Body.Bytes())
	for _, body := range []string{`{"amount":"1"}`, `{"amount":null}`, `{} {}`, `{"amount":1e999999999}`, strings.Repeat("x", 70000)} {
		out = httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("POST", "/api/wallets/transfer", strings.NewReader(body)))
		if out.Code != 400 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
}

// Independently authored parser for the evidenced numeric/status projection,
// not copied SDK schema/generated code. It is not a full existing-client E2E.
func parseAuthoredClientTransaction(t *testing.T, b []byte) {
	t.Helper()
	var raw struct {
		ID       string      `json:"id"`
		Amount   json.Number `json:"amount"`
		Status   int         `json:"status"`
		Type     int         `json:"type"`
		Payer    string      `json:"payer_wallet_id"`
		Payee    string      `json:"payee_wallet_id"`
		Currency string      `json:"currency"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e := d.Decode(&raw); e != nil {
		t.Fatal(e)
	}
	if !ValidID(raw.ID) || raw.Amount.String() != "9007199254740993.123" || raw.Status != 2 || raw.Type != 0 || raw.Payer != walletA || raw.Payee != walletB || raw.Currency != "points" {
		t.Fatal(raw)
	}
}
func TestHTTPDoesNotLeakInternalError(t *testing.T) {
	s, _, _, _, _ := serviceFixture()
	h := NewHandler(s, authFixture{err: ErrUnauthorized})
	out := httptest.NewRecorder()
	h.ServeHTTP(out, httptest.NewRequest("GET", "/api/wallets", nil))
	if out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("Content-Type") != "application/json" {
		t.Fatal(out.Header())
	}
}
