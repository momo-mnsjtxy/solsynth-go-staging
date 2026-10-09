// SPDX-License-Identifier: AGPL-3.0-only
// Derived from the pinned licensed WalletController exchange-catalog path.
package exchangecatalog

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Entry is one flattened IConfiguration-style key/value pair. Nil represents
// null; keys use ':' for hierarchy. Providers are ordered low to high priority.
type Entry struct {
	Key   string
	Value *string
}
type Provider []Entry

type Option struct {
	SourceCurrency string  `json:"source_currency"`
	SourceAmount   Decimal `json:"source_amount"`
	TargetCurrency string  `json:"target_currency"`
	TargetAmount   Decimal `json:"target_amount"`
}

var ErrInvalidConfiguration = errors.New("currency exchange configuration is invalid")

// Catalog is an immutable snapshot; rebuild explicitly to adopt config changes.
type Catalog struct{ entries []Entry }

const section = "Payment:CurrencyExchange:"

func New(providers ...Provider) (*Catalog, error) {
	values := make(map[string]*string)
	children := []string{}
	for _, provider := range providers {
		localSeen := make(map[string]bool)
		currentChildren := []string{}
		for _, entry := range provider {
			if !utf8.ValidString(entry.Key) || entry.Value != nil && !utf8.ValidString(*entry.Value) {
				return nil, errors.New("configuration must contain valid Unicode")
			}
			key := ordinalKey(entry.Key)
			if localSeen[key] {
				return nil, errors.New("duplicate case-insensitive configuration key")
			}
			localSeen[key] = true
			if entry.Value == nil {
				values[key] = nil
			} else {
				v := *entry.Value
				values[key] = &v
			}
			if len(entry.Key) >= len(section) && equalOrdinal(entry.Key[:len(section)], section) {
				child := strings.SplitN(entry.Key[len(section):], ":", 2)[0]
				currentChildren = append(currentChildren, child)
			}
		}
		currentChildren = append(currentChildren, children...)
		sortConfigurationKeys(currentChildren)
		children = currentChildren
	}
	result := &Catalog{entries: []Entry{}}
	seen := make(map[string]bool)
	for _, child := range children {
		k := ordinalKey(child)
		if seen[k] {
			continue
		}
		seen[k] = true
		value := values[ordinalKey(section+child)]
		result.entries = append(result.entries, Entry{Key: child, Value: value})
	}
	return result, nil
}

func (c *Catalog) Options() ([]Option, error) {
	result := []Option{}
	if c == nil {
		return nil, ErrInvalidConfiguration
	}
	for _, entry := range c.entries {
		sourceAmount, sourceCurrency, ok := parseCurrencyAmount(&entry.Key)
		if !ok {
			continue
		}
		targetAmount, targetCurrency, ok := parseCurrencyAmount(entry.Value)
		if !ok || equalOrdinal(sourceCurrency, targetCurrency) {
			return nil, ErrInvalidConfiguration
		}
		result = append(result, Option{SourceCurrency: sourceCurrency, SourceAmount: sourceAmount, TargetCurrency: targetCurrency, TargetAmount: targetAmount})
	}
	return result, nil
}
