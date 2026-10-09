// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/application"
	"strings"
)

func chatProfileFlags(fs *flag.FlagSet) *string {
	return fs.String("chat-profile-perks", "", "explicit JSON map of canonical internal subscription identifiers to perk levels 1 through 3; level 2 is required for chat nickname customization; omitted disables PATCH")
}
func applyChatProfile(c *application.Config, raw string) error {
	var parsed application.Config
	if e := applyChatOrganization(&parsed, raw); e != nil {
		return errors.New(strings.ReplaceAll(e.Error(), "chat search", "chat profile"))
	}
	c.ChatProfilePerks = parsed.ChatSearchPerks
	return nil
}
