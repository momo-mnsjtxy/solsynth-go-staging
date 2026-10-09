// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"

	"solsynth.local/go-migration/internal/application"
)

func chatOrganizationFlags(fs *flag.FlagSet) *string {
	return fs.String("chat-search-perks", "", "explicit JSON map of canonical internal subscription identifiers to perk levels 1 through 3; omitted disables cloud search")
}
func applyChatOrganization(c *application.Config, raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > 32768 {
		return errors.New("invalid chat search perk mapping")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return errors.New("chat search perks must be a JSON object")
	}
	levels := map[string]int{}
	for d.More() {
		k, x := d.Token()
		if x != nil {
			return errors.New("invalid chat search perk mapping")
		}
		key, ok := k.(string)
		if !ok || key == "" || strings.TrimSpace(key) != key || len(key) > 256 {
			return errors.New("invalid chat search perk identifier")
		}
		if _, ok = levels[key]; ok {
			return errors.New("duplicate chat search perk identifier")
		}
		var n int
		if e = d.Decode(&n); e != nil || n < 1 || n > 3 {
			return errors.New("chat search perk level must be 1 through 3")
		}
		levels[key] = n
		if len(levels) > 100 {
			return errors.New("too many chat search perk identifiers")
		}
	}
	if _, e = d.Token(); e != nil {
		return errors.New("invalid chat search perk mapping")
	}
	if _, e = d.Token(); e != io.EOF {
		return errors.New("trailing chat search perk data")
	}
	c.ChatSearchPerks = levels
	return nil
}
