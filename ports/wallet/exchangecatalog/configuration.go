// SPDX-License-Identifier: AGPL-3.0-only
// Reusable immutable view of the already-tested IConfiguration-style providers.
package exchangecatalog

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Configuration reuses the catalog's pinned ordinal/key ordering behavior.
// Values are copied; callers cannot mutate the snapshot via input/output pointers.
type Configuration struct {
	providers []Provider
	values    map[string]*string
}

func NewConfiguration(providers ...Provider) (*Configuration, error) {
	c := &Configuration{values: map[string]*string{}}
	for _, provider := range providers {
		copied := make(Provider, 0, len(provider))
		seen := map[string]bool{}
		for _, entry := range provider {
			if !utf8.ValidString(entry.Key) || entry.Value != nil && !utf8.ValidString(*entry.Value) {
				return nil, errors.New("configuration must contain valid Unicode")
			}
			key := ordinalKey(entry.Key)
			if seen[key] {
				return nil, errors.New("duplicate case-insensitive configuration key")
			}
			seen[key] = true
			var value *string
			if entry.Value != nil {
				v := *entry.Value
				value = &v
			}
			copied = append(copied, Entry{Key: entry.Key, Value: value})
			c.values[key] = value
		}
		c.providers = append(c.providers, copied)
	}
	return c, nil
}

func (c *Configuration) Lookup(path string) (*string, bool) {
	if c == nil {
		return nil, false
	}
	value, exists := c.values[ordinalKey(path)]
	if value == nil {
		return nil, exists
	}
	v := *value
	return &v, exists
}

// Children returns direct child names in framework key order. A later provider
// overrides values but does not erase descendant keys from earlier providers.
func (c *Configuration) Children(path string) []string {
	if c == nil {
		return []string{}
	}
	var segments []string
	if path != "" {
		segments = strings.Split(path, ":")
	}
	children := []string{}
	for _, provider := range c.providers {
		current := []string{}
		for _, entry := range provider {
			parts := strings.Split(entry.Key, ":")
			if len(parts) <= len(segments) {
				continue
			}
			matches := true
			for i, segment := range segments {
				if !equalOrdinal(parts[i], segment) {
					matches = false
					break
				}
			}
			if matches {
				current = append(current, parts[len(segments)])
			}
		}
		current = append(current, children...)
		sortConfigurationKeys(current)
		children = current
	}
	result := []string{}
	seen := map[string]bool{}
	for _, child := range children {
		key := ordinalKey(child)
		if !seen[key] {
			seen[key] = true
			result = append(result, child)
		}
	}
	return result
}
