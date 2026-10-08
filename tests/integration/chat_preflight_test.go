// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/authority"
)

func TestPGChatAuthorityPreflightSameTransactionAndNoLocks(t *testing.T) {
	f := newChatRelationsFixture(t)
	tx, e := f.db.BeginTx(f.base, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = f.guard.Preflight(context.Background(), f.db, tx, f.owner, "chat.create"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound preflight", e)
	}
	if _, e = f.guard.Preflight(f.contexts[f.owner], nil, tx, f.owner, "chat.create"); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("foreign pool preflight", e)
	}
	if _, e = f.guard.Preflight(f.contexts[f.owner], f.db, tx, f.other, "chat.create"); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("actor substitution", e)
	}
	if _, e = f.guard.Preflight(f.contexts[f.owner], f.db, tx, f.owner, "chat.create"); e != nil {
		t.Fatal(e)
	}
	other, e := f.db.BeginTx(f.base, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Rollback()
	var id string
	if e = other.QueryRowContext(f.base, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, f.owner).Scan(&id); e != nil {
		t.Fatal("preflight acquired an account lock", e)
	}
	if _, e = other.ExecContext(f.base, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.owner); e != nil {
		t.Fatal(e)
	}
	if e = other.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.guard.Authorize(f.contexts[f.owner], f.db, tx, f.owner, "chat.create"); e == nil {
		t.Fatal("locking guard failed to observe disable after successful preflight")
	}
}
