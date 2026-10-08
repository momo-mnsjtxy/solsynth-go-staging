// SPDX-License-Identifier: AGPL-3.0-only
package developpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/develop"
	"testing"
)

func TestBotCommandsRepositoryPolicy(t *testing.T) {
	id := "ab000000-0000-0000-0000-000000000001"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range []*BotCommandsRepository{nil, {}, NewBotCommandsRepository(nil)} {
		for _, c := range []context.Context{context.Background(), ctx} {
			if _, err := r.ReadBotCommands(c, id); !errors.Is(err, develop.ErrBotCommandsPolicyUnverified) {
				t.Fatal(err)
			}
		}
	}
	r := NewSyntheticCompatibilityBotCommandsRepository(nil)
	if _, err := r.ReadBotCommands(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.ReadBotCommands(context.Background(), "bad"); !errors.Is(err, develop.ErrBotCommandsInvalidID) {
		t.Fatal(err)
	}
	if _, err := r.ReadBotCommands(context.Background(), id); !errors.Is(err, ErrDatabaseRequired) {
		t.Fatal(err)
	}
}
