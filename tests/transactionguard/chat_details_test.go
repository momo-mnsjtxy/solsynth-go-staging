// SPDX-License-Identifier: AGPL-3.0-only
package transactionguard_test

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
	"time"
)

type syntheticProfilePerk struct{}

func (syntheticProfilePerk) RequireChatProfile(context.Context, *sql.DB, *sql.Tx, string, time.Time) error {
	return nil
}
func TestChatDetailsCustomGuardCannotReachDomainSQL(t *testing.T) {
	for _, action := range []string{"update", "clear", "moderate-delete", "moderate-timeout"} {
		t.Run(action, func(t *testing.T) {
			state := &sentinel{}
			db := sql.OpenDB(connector{state})
			defer db.Close()
			r, e := chatpg.NewWithAuthority(db, policy, successfulCustomGuard{})
			if e != nil {
				t.Fatal(e)
			}
			r.WithProfilePerks(syntheticProfilePerk{})
			room := "c2000000-0000-4000-8000-000000000001"
			target := "c3000000-0000-4000-8000-000000000001"
			switch action {
			case "update":
				_, e = r.UpdateProfile(context.Background(), actor, room, rooms.ProfileRequest{})
			case "clear":
				_, e = r.ClearProfile(context.Background(), actor, room)
			case "moderate-delete":
				e = r.ModerateDelete(context.Background(), actor, room, target, rooms.ModerationDelete{})
			case "moderate-timeout":
				_, e = r.ModerateTimeout(context.Background(), actor, room, target, rooms.ModerationTimeout{DurationMinutes: 1})
			}
			if !errors.Is(e, rooms.ErrUnavailable) || state.statements != 0 || state.commits != 0 || state.rollbacks != 1 {
				t.Fatal(e, state)
			}
		})
	}
}
