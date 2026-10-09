// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/develop/bots"
	"testing"
)

type mutableExecutionAdapter struct {
	name, bot, binding string
	calls              int
}

func (a *mutableExecutionAdapter) Name() string      { return a.name }
func (a *mutableExecutionAdapter) BotID() string     { return a.bot }
func (a *mutableExecutionAdapter) BindingID() string { return a.binding }
func (a *mutableExecutionAdapter) Deliver(context.Context, bots.MessageDelivery) error {
	a.calls++
	return nil
}
func TestConfiguredAdapterRejectsIdentityDrift(t *testing.T) {
	for _, field := range []string{"name", "bot", "binding"} {
		t.Run(field, func(t *testing.T) {
			target := &mutableExecutionAdapter{name: "original", bot: "bot", binding: "binding"}
			adapter := configuredAdapter{name: target.name, bot: target.bot, binding: target.binding, target: target}
			switch field {
			case "name":
				target.name = "changed"
			case "bot":
				target.bot = "changed"
			case "binding":
				target.binding = "changed"
			}
			if adapter.Name() != "original" || adapter.BotID() != "bot" || adapter.BindingID() != "binding" {
				t.Fatal("registered identity drifted")
			}
			if e := adapter.Deliver(context.Background(), bots.MessageDelivery{}); !errors.Is(e, bots.ErrUnavailable) || target.calls != 0 {
				t.Fatal("changed adapter invoked", e)
			}
		})
	}
}
