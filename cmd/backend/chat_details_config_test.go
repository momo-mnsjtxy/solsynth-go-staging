// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"flag"
	"solsynth.local/go-migration/internal/application"
	"testing"
)

func TestChatProfileExplicitPolicyFlagAndNoDefaultTier(t *testing.T) {
	var c application.Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	raw := chatProfileFlags(fs)
	if e := fs.Parse(nil); e != nil {
		t.Fatal(e)
	}
	if e := applyChatProfile(&c, *raw); e != nil || c.ChatProfilePerks != nil {
		t.Fatal(c, e)
	}
	if e := applyChatProfile(&c, `{"internal-plan":2}`); e != nil || c.ChatProfilePerks["internal-plan"] != 2 || c.ChatSearchPerks != nil {
		t.Fatal(c, e)
	}
	for _, raw := range []string{`{"x":2,"x":3}`, `{"x":0}`, `{"x":4}`, `null`, `[]`, `{" x":2}`, `{"x":2} {}`} {
		if e := applyChatProfile(&c, raw); e == nil {
			t.Fatal(raw)
		}
	}
	if e := applyChatProfile(&c, `{}`); e != nil || c.ChatProfilePerks == nil || len(c.ChatProfilePerks) != 0 {
		t.Fatal(c, e)
	}
}
