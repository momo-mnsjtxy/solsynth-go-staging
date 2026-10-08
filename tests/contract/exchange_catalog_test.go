package contract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

type exchangeOracleCase struct {
	Name      string                     `json:"name"`
	Configs   []string                   `json:"configs"`
	Providers []exchangecatalog.Provider `json:"providers"`
}
type exchangeOracleResult struct {
	Name        string  `json:"name"`
	Status      int     `json:"status"`
	Body        *string `json:"body"`
	ConfigError *string `json:"config_error"`
}

func exchangeJSON(t *testing.T, b []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("invalid JSON %s: %v", b, err)
	}
	return result
}

func TestExchangeCatalogDotNetOracle(t *testing.T) {
	read := func(path string, dst any) {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, dst); e != nil {
			t.Fatal(e)
		}
	}
	var cases []exchangeOracleCase
	var results []exchangeOracleResult
	read("exchange-oracle/cases.json", &cases)
	read("exchange-oracle/expected.json", &results)
	if len(cases) != len(results) || len(cases) == 0 {
		t.Fatal("oracle case/result count mismatch")
	}
	for i, test := range cases {
		expected := results[i]
		t.Run(test.Name, func(t *testing.T) {
			if expected.Name != test.Name {
				t.Fatal("oracle fixture ordering mismatch")
			}
			providers := []exchangecatalog.Provider{}
			var configErr error
			for j, config := range test.Configs {
				// Exercise real JSON files rather than hand-constructing all providers.
				path := filepath.Join(t.TempDir(), fmt.Sprintf("appsettings-%d.json", j))
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				provider, err := exchangecatalog.LoadJSON(file)
				file.Close()
				if err != nil {
					configErr = err
					break
				}
				providers = append(providers, provider)
			}
			providers = append(providers, test.Providers...)
			var catalog *exchangecatalog.Catalog
			if configErr == nil {
				catalog, configErr = exchangecatalog.New(providers...)
			}
			if expected.ConfigError != nil {
				if configErr == nil {
					t.Fatal(".NET rejected configuration but Go accepted")
				}
				return
			}
			if configErr != nil {
				t.Fatal(configErr)
			}
			handler := exchangecatalog.NewHandler(catalog)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/wallets/exchange", nil))
			if response.Code != expected.Status {
				t.Fatalf("status %d want %d: %s", response.Code, expected.Status, response.Body.String())
			}
			if expected.Body == nil {
				t.Fatal("missing successful oracle body")
			}
			// UseNumber preserves decimal lexemes, including trailing scale and values
			// outside binary64, while allowing equivalent JSON escaping/key ordering.
			got, want := exchangeJSON(t, response.Body.Bytes()), exchangeJSON(t, []byte(*expected.Body))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Go body %s\n.NET body %s", response.Body.String(), *expected.Body)
			}
		})
	}
}

func TestExchangeCatalogActualMVCMethodContract(t *testing.T) {
	var cases []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Status int    `json:"status"`
		Allow  string `json:"allow"`
		Body   string `json:"body"`
	}
	b, err := os.ReadFile("exchange-oracle/mvc-methods.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	target := "2 EUR"
	catalog, err := exchangecatalog.New(exchangecatalog.Provider{{Key: "Payment:CurrencyExchange:1 USD", Value: &target}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Method+test.Path, func(t *testing.T) {
			w := httptest.NewRecorder()
			exchangecatalog.NewHandler(catalog).ServeHTTP(w, httptest.NewRequest(test.Method, test.Path, nil))
			if w.Code != test.Status || w.Header().Get("Allow") != test.Allow {
				t.Fatalf("method status/Allow %d/%q want %d/%q", w.Code, w.Header().Get("Allow"), test.Status, test.Allow)
			}
			if test.Body == "" {
				if w.Body.Len() != 0 {
					t.Fatalf("MVC method rejection body must be empty: %s", w.Body.String())
				}
			} else if !reflect.DeepEqual(exchangeJSON(t, w.Body.Bytes()), exchangeJSON(t, []byte(test.Body))) {
				t.Fatal("MVC success DTO differs")
			}
		})
	}
}
