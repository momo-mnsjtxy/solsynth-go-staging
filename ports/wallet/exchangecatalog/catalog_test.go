// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }
func TestDecimalRangeScaleAndRounding(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{".5", "0.5"}, {"1.", "1"}, {"0001.00", "1.00"},
		{"79228162514264337593543950335", "79228162514264337593543950335"},
		{"79228162514264337593543950335.4", "79228162514264337593543950335"},
		{"0.00000000000000000000000000015", "0.0000000000000000000000000002"},
		{"0.00000000000000000000000000025", "0.0000000000000000000000000002"},
		{"1.23456789012345678901234567885", "1.2345678901234567890123456788"},
	} {
		got, ok := parseDecimal(tc.in)
		if !ok || got.String() != tc.want {
			t.Fatalf("%s -> %s (%v), want %s", tc.in, got, ok, tc.want)
		}
		b, _ := json.Marshal(got)
		if string(b) != tc.want {
			t.Fatal(string(b))
		}
	}
	for _, input := range []string{"", "0", "-1", "+1", "1e2", "1,000", "1..2", ".", "١", "79228162514264337593543950336", "79228162514264337593543950335.5", "0.00000000000000000000000000005"} {
		if d, ok := parseDecimal(input); ok {
			t.Fatal(input, d)
		}
	}
	var zero Decimal
	if stringMustJSON(t, zero) != "0" {
		t.Fatal("zero value")
	}
}
func stringMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestPrefixGrammarAndArbitraryCurrency(t *testing.T) {
	for _, tc := range []struct{ in, amount, currency string }{{"1e3 USD", "1", "e3 USD"}, {"1,000 USD", "1", ",000 USD"}, {"1\x00coin", "1", "\x00coin"}, {"\u20031.00 \u00a0Coin\u3000", "1.00", "Coin"}, {"1💰 coins", "1", "💰 coins"}} {
		d, c, ok := parseCurrencyAmount(ptr(tc.in))
		if !ok || d.String() != tc.amount || c != tc.currency {
			t.Fatal(tc, d, c, ok)
		}
	}
	for _, s := range []*string{nil, ptr("1"), ptr("USD"), ptr("١ USD"), ptr("𝟙 USD"), ptr("1.٢ USD"), ptr("0 x"), ptr("\t ")} {
		if _, _, ok := parseCurrencyAmount(s); ok {
			t.Fatal(s)
		}
	}
}

func TestCatalogSkipSourcesFailWholeTargetAndSnapshot(t *testing.T) {
	value := "2.00 EUR"
	p := Provider{{Key: section + "invalid", Value: nil}, {Key: section + "1 USD", Value: &value}}
	c, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	value = "3 GBP"
	p[1].Key = section + "9 other"
	got, err := c.Options()
	if err != nil || len(got) != 1 || got[0].TargetCurrency != "EUR" || got[0].TargetAmount.String() != "2.00" {
		t.Fatal(got, err)
	}
	c, _ = New(Provider{{Key: section + "1 A", Value: ptr("2 B")}, {Key: section + "2 C", Value: ptr("bad")}})
	if got, err := c.Options(); !errors.Is(err, ErrInvalidConfiguration) || got != nil {
		t.Fatal(got, err)
	}
	c, _ = New(Provider{{Key: section + "1 usd", Value: ptr("2 USD")}})
	if _, err = c.Options(); err == nil {
		t.Fatal("same currency accepted")
	}
	if c, err := New(); err != nil {
		t.Fatal(err)
	} else if got, _ := c.Options(); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
}

func TestJSONProviderAndLayering(t *testing.T) {
	first, err := LoadJSON(strings.NewReader(`{/*comment*/"Payment":{"CurrencyExchange":{"2 Z":"5 M","10 A":"4 N","1 USD":"2 EUR",},},"other":true,}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadJSON(strings.NewReader(`{"payment:currencyexchange:1 usd":"9.00 GBP"}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(first, second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Options()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SourceCurrency != "usd" || got[0].TargetAmount.String() != "9.00" || got[1].SourceCurrency != "A" || got[2].SourceCurrency != "Z" {
		t.Fatal(got)
	}
	p, err := LoadJSON(strings.NewReader(`{"a":{},"b":[],"c":null,"n":2.10,"bool":false,"arr":["x",{}],"":{"x":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*string{}
	for _, e := range p {
		m[e.Key] = e.Value
	}
	if m["a"] != nil || m["c"] != nil || *m["b"] != "" || *m["n"] != "2.10" || *m["bool"] != "False" || *m["arr:0"] != "x" || m["arr:1"] != nil || *m[":x"] != "v" {
		t.Fatal(m)
	}
	for _, input := range []string{`[]`, `{"a":1,"A":2}`, `{"a":{"x":1},"a:x":2}`, `{"x":"\uD800"}`, `{"x":"\uDC00"}`, `{"x":"\uD800\u0041"}`, `{/* unfinished`, `{} {}`, `{"x":wrong}`} {
		if _, err := LoadJSON(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if _, err := New(Provider{{Key: "x", Value: nil}, {Key: "X", Value: nil}}); err == nil {
		t.Fatal("duplicate provider keys")
	}
	if _, err := New(Provider{{Key: string([]byte{255})}}); err == nil {
		t.Fatal("invalid UTF8")
	}
	// Empty-container assignment overwrites, unlike duplicate scalar rejection.
	if _, err := LoadJSON(strings.NewReader(`{"x":null,"X":{}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinalRulesAndSorting(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{{"usd", "USD", true}, {"ı", "I", false}, {"ſ", "S", false}, {"K", "k", false}, {"ß", "ẞ", false}, {"µ", "Μ", true}, {"\U00010d70", "\U00010d50", true}, {"ɤ", "Ɤ", true}} {
		if equalOrdinal(tc.a, tc.b) != tc.equal {
			t.Fatal(tc)
		}
	}
	keys := []string{}
	for i := 80; i >= 0; i-- {
		keys = append(keys, fmt.Sprintf("%d currency", i))
	}
	sortConfigurationKeys(keys)
	for i := 1; i < len(keys); i++ {
		if configKeyCompare(keys[i-1], keys[i]) > 0 {
			t.Fatal(keys)
		}
	}
	numeric := []string{"Z", "10", "2", "a"}
	sortConfigurationKeys(numeric)
	if !reflect.DeepEqual(numeric, []string{"2", "10", "a", "Z"}) {
		t.Fatal(numeric)
	}
	if ordinalCompare("\uffff", "\U00010000") >= 0 {
		t.Fatal("supplementary must sort after BMP in .NET ordinal-ignore-case")
	}
}

func TestCompleteHTTPContract(t *testing.T) {
	c, _ := New(Provider{{Key: section + "1 USD", Value: ptr("2.00 EUR")}})
	for _, path := range []string{"/api/wallets/exchange", "/API/WALLETS/EXCHANGE/"} {
		w := httptest.NewRecorder()
		NewHandler(c).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"target_amount":2.00`) {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatal(w.Header())
		}
	}
	for _, method := range []string{"HEAD", "POST", "DELETE"} {
		w := httptest.NewRecorder()
		NewHandler(c).ServeHTTP(w, httptest.NewRequest(method, "/api/wallets/exchange", nil))
		if w.Code != 405 || w.Body.Len() != 0 || w.Header().Get("Allow") != "GET" {
			t.Fatal(w)
		}
	}
	for _, path := range []string{"/api/wallets/exchange/extra", "/api/wallets/exchange//", "/api/walletſ/exchange"} {
		w := httptest.NewRecorder()
		NewHandler(c).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || w.Body.Len() != 0 {
			t.Fatal(path, w.Code)
		}
	}
	invalid, _ := New(Provider{{Key: section + "1 A", Value: nil}})
	w := httptest.NewRecorder()
	NewHandler(invalid).ServeHTTP(w, httptest.NewRequest("GET", "/api/wallets/exchange", nil))
	if w.Code != 500 || strings.TrimSpace(w.Body.String()) != `{"code":"WALLET_EXCHANGE_CONFIGURATION_INVALID","message":"Currency exchange configuration is invalid.","status":500}` {
		t.Fatal(w.Code, w.Body.String())
	}
}
