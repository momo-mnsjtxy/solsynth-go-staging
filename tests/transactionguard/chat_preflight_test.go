// SPDX-License-Identifier: AGPL-3.0-only
package transactionguard_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type successfulCustomGuard struct{}

func (successfulCustomGuard) Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error) {
	return authority.Principal{}, nil
}
func (successfulCustomGuard) Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error) {
	return authority.Principal{}, nil
}

func TestChatCustomGuardWithoutPreflightFailsClosedBeforeDomainSQL(t *testing.T) {
	state := &sentinel{}
	db := sql.OpenDB(connector{state})
	defer db.Close()
	r, e := chatpg.NewWithAuthority(db, policy, successfulCustomGuard{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.Create(context.Background(), actor, rooms.RoomPatch{Name: ptr("Synthetic")})
	if !errors.Is(e, rooms.ErrUnavailable) || state.statements != 0 || state.commits != 0 || state.rollbacks != 1 {
		t.Fatal(e, state)
	}
}
