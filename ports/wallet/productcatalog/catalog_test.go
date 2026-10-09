// SPDX-License-Identifier: AGPL-3.0-only
package productcatalog

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

func tokens(t *testing.T, body string) []any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(body))
	d.UseNumber()
	result := []any{}
	for {
		value, err := d.Token()
		if err == io.EOF {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
}
func TestIndependentProductConfigurationOracle(t *testing.T) {
	var cases []struct {
		Name    string          `json:"name"`
		Configs []string        `json:"configs"`
		Direct  json.RawMessage `json:"direct_definition"`
	}
	var expected []struct {
		Name  string  `json:"name"`
		Body  *string `json:"body"`
		Error *string `json:"error"`
	}
	for _, input := range []struct {
		path string
		out  any
	}{{"../../../tests/contract/wallet-product-oracle/cases.json", &cases}, {"../../../tests/contract/wallet-product-oracle/expected.json", &expected}} {
		data, err := os.ReadFile(input.path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, input.out); err != nil {
			t.Fatal(err)
		}
	}
	if len(cases) != len(expected) {
		t.Fatal("reference case counts differ")
	}
	for i, c := range cases {
		// Direct-definition injection is a separate reference probe. The Go public
		// API accepts actual configuration providers only, not such object injection.
		if c.Direct != nil {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			if expected[i].Name != c.Name {
				t.Fatal("reference ordering mismatch")
			}
			var providers []exchangecatalog.Provider
			var err error
			for _, config := range c.Configs {
				var p exchangecatalog.Provider
				p, err = exchangecatalog.LoadJSON(strings.NewReader(config))
				if err != nil {
					break
				}
				providers = append(providers, p)
			}
			var catalog *Catalog
			if err == nil {
				catalog, err = NewWithSyntheticDefaultCurrency("golds", providers...)
			}
			var body []byte
			if err == nil {
				body, err = catalog.JSON()
			}
			if expected[i].Error != nil {
				if err == nil {
					t.Fatal("expected reference failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if expected[i].Body == nil || !reflect.DeepEqual(tokens(t, string(body)), tokens(t, *expected[i].Body)) {
				t.Fatalf("got %s\nwant %s", body, *expected[i].Body)
			}
		})
	}
}

func TestMissingCurrencyIsNotGuessed(t *testing.T) {
	if _, err := New(); err != ErrDefaultCurrencyUnverified {
		t.Fatal(err)
	}
	provider, err := exchangecatalog.LoadJSON(strings.NewReader(`{"Payment":{"Product":{"GoldCurrency":{"Currency":"DOC"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(provider)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.JSON()
	if err != nil || !strings.Contains(string(data), `"currency":"DOC"`) {
		t.Fatal(string(data), err)
	}
}

func TestNamingEvidenceAndConfigurationIsolation(t *testing.T) {
	reference, err := os.ReadFile("../../../tests/contract/wallet-product-oracle/naming-bmp.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(reference) != string(namingData) {
		t.Fatal("compiled naming table differs from independent reference")
	}
	value := "DOC"
	inputs := exchangecatalog.Provider{{Key: "Payment:Product:GoldCurrency:Currency", Value: &value}}
	config, err := exchangecatalog.NewConfiguration(inputs)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(inputs)
	if err != nil {
		t.Fatal(err)
	}
	value = "changed"
	inputs[0].Key = "changed"
	v, ok := config.Lookup("payment:product:goldcurrency:currency")
	if !ok || v == nil || *v != "DOC" {
		t.Fatal(v, ok)
	}
	*v = "changed-again"
	v, _ = config.Lookup("payment:product:goldcurrency:currency")
	if *v != "DOC" {
		t.Fatal(*v)
	}
	body, _ := c.JSON()
	if !strings.Contains(string(body), `"currency":"DOC"`) {
		t.Fatal(string(body))
	}
}

func TestOrdinalEquivalentDifferentUTF8LengthsMergeChildren(t *testing.T) {
	for _, configs := range [][]string{
		{`{"Payment":{"Product":{"GoldCurrency":{"Currency":"DOC","ProviderMappings":{"Ⱥ":{"First":"1.00"},"ⱥ":{"Second":"2.00"}}}}}}`},
		{`{"Payment":{"Product":{"GoldCurrency":{"Currency":"DOC","ProviderMappings":{"Ⱥ":{"First":"1.00"}}}}}}`, `{"Payment":{"Product":{"GoldCurrency":{"ProviderMappings":{"ⱥ":{"Second":"2.00"}}}}}}`},
	} {
		var providers []exchangecatalog.Provider
		for _, text := range configs {
			p, err := exchangecatalog.LoadJSON(strings.NewReader(text))
			if err != nil {
				t.Fatal(err)
			}
			providers = append(providers, p)
		}
		c, err := New(providers...)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := c.JSON()
		if !strings.Contains(string(body), `"first":1.00,"second":2.00`) {
			t.Fatal(string(body))
		}
	}
}
