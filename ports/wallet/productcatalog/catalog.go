// SPDX-License-Identifier: AGPL-3.0-only
// Source-derived from licensed Wallet product options/service/controller and
// owned API documentation, pinned at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
package productcatalog

import (
	"bytes"
	"encoding/json"
	"errors"

	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

const section = "Payment:Product:GoldCurrency"

var ErrDefaultCurrencyUnverified = errors.New("missing currency requires an explicitly selected documented-default fixture")

type entry struct {
	name  string
	value string
}
type provider struct {
	name    string
	entries []entry
}

// Catalog is a real immutable configuration projection, not a checkout client.
type Catalog struct {
	identifier, displayName, currency *string
	providers                         []provider
}

// New binds explicit configuration. It refuses to infer the excluded symbolic
// currency initializer when no Currency setting is supplied.
func New(providers ...exchangecatalog.Provider) (*Catalog, error) { return bind(nil, providers...) }

// NewWithSyntheticDefaultCurrency permits the caller-selected fixture default.
// The docs show golds, but do not prove the omitted-configuration initializer.
func NewWithSyntheticDefaultCurrency(currency string, providers ...exchangecatalog.Provider) (*Catalog, error) {
	return bind(&currency, providers...)
}

func bind(defaultCurrency *string, providers ...exchangecatalog.Provider) (*Catalog, error) {
	config, err := exchangecatalog.NewConfiguration(providers...)
	if err != nil {
		return nil, err
	}
	identifier, display := "wallet.golds_resupply_pack", "Golds Resupply Pack"
	c := &Catalog{identifier: &identifier, displayName: &display, currency: defaultCurrency, providers: []provider{}}
	for _, field := range []struct {
		name string
		out  **string
	}{{"Identifier", &c.identifier}, {"DisplayName", &c.displayName}, {"Currency", &c.currency}} {
		if value, present := config.Lookup(section + ":" + field.name); present {
			*field.out = value
		}
	}
	if _, present := config.Lookup(section + ":Currency"); !present && defaultCurrency == nil {
		return nil, ErrDefaultCurrencyUnverified
	}
	path := section + ":ProviderMappings"
	for _, name := range config.Children(path) {
		children := config.Children(path + ":" + name)
		value, present := config.Lookup(path + ":" + name)
		if len(children) == 0 && present && value != nil && *value != "" {
			continue
		}
		p := provider{name: snakeCase(name), entries: []entry{}}
		for _, reference := range children {
			raw, _ := config.Lookup(path + ":" + name + ":" + reference)
			text := "0"
			ok := true
			if raw != nil {
				text, ok = decimalText(*raw)
			}
			if ok {
				p.entries = append(p.entries, entry{name: snakeCase(reference), value: text})
			}
		}
		c.providers = append(c.providers, p)
	}
	return c, nil
}

// JSON preserves nested dictionary order, duplicate post-naming JSON keys and
// decimal lexemes. Converting these objects into Go maps would lose evidence.
func (c *Catalog) JSON() ([]byte, error) {
	if c == nil {
		return nil, errors.New("product catalog unavailable")
	}
	var out bytes.Buffer
	out.WriteString(`{"key":"golds_resupply_pack","identifier":`)
	for i, value := range []*string{c.identifier, c.displayName, c.currency} {
		if i == 1 {
			out.WriteString(`,"display_name":`)
		}
		if i == 2 {
			out.WriteString(`,"currency":`)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out.Write(data)
	}
	out.WriteString(`,"provider_mappings":{`)
	for i, p := range c.providers {
		if i > 0 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(p.name)
		out.Write(name)
		out.WriteString(`:{`)
		for j, e := range p.entries {
			if j > 0 {
				out.WriteByte(',')
			}
			name, _ := json.Marshal(e.name)
			out.Write(name)
			out.WriteByte(':')
			out.WriteString(e.value)
		}
		out.WriteByte('}')
	}
	out.WriteString(`}}`)
	return out.Bytes(), nil
}
