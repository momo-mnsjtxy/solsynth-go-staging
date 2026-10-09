// SPDX-License-Identifier: AGPL-3.0-only
package transactionguard_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/messager/rooms"
)

func TestChatForwardAndRedirectCheckProofBeforeSourceDiscovery(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		name := "reference"
		if redirect {
			name = "redirect"
		}
		t.Run(name, func(t *testing.T) {
			harness(t, actor, "chat.messages.create", func(db *sql.DB, g *denyGuard) error {
				r, e := chatpg.NewWithAuthority(db, policy, g)
				if e != nil {
					return e
				}
				if redirect {
					_, e = r.Redirect(context.Background(), actor, target, []string{target}, "proof-before-source")
				} else {
					_, e = r.Send(context.Background(), actor, target, rooms.TextRequest{ForwardedMessageID: ptr(target)})
				}
				return e
			})
		})
	}
}

func TestChatForwardAndRedirectRejectGuardWithoutPreflightBeforeSourceDiscovery(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		name := "reference"
		if redirect {
			name = "redirect"
		}
		t.Run(name, func(t *testing.T) {
			state := &sentinel{}
			db := sql.OpenDB(connector{state})
			defer db.Close()
			r, e := chatpg.NewWithAuthority(db, policy, successfulCustomGuard{})
			if e != nil {
				t.Fatal(e)
			}
			if redirect {
				_, e = r.Redirect(context.Background(), actor, target, []string{target}, "proof-before-source")
			} else {
				_, e = r.Send(context.Background(), actor, target, rooms.TextRequest{ForwardedMessageID: ptr(target)})
			}
			if !errors.Is(e, rooms.ErrUnavailable) || state.statements != 0 || state.commits != 0 || state.rollbacks != 1 {
				t.Fatal(e, state)
			}
		})
	}
}
