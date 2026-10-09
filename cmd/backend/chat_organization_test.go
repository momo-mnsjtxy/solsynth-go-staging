// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestChatSearchConfigurationIsExplicitAndBounded(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"a":0}`, `{"a":4}`, `{"a":1,"a":2}`, `{" a":1}`, `{"a":1} {}`, `{"a":"1"}`, `{"a":null}`} {
		var c application.Config
		if applyChatOrganization(&c, raw) == nil {
			t.Fatal(raw)
		}
	}
	var c application.Config
	if applyChatOrganization(&c, "") != nil || c.ChatSearchPerks != nil {
		t.Fatal(c.ChatSearchPerks)
	}
	if applyChatOrganization(&c, `{"synthetic-system":1}`) != nil || c.ChatSearchPerks["synthetic-system"] != 1 {
		t.Fatal(c.ChatSearchPerks)
	}
	if applyChatOrganization(&c, `{}`) != nil || c.ChatSearchPerks == nil {
		t.Fatal("explicit empty policy must remain distinct from unset")
	}
}
