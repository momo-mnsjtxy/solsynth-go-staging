// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"testing"
)

func TestSessionsExcludeAccountInvalidatedCredentials(t *testing.T) {
	s, _, _ := setup(t)
	old := issue(t, s, accountA, "*")
	ctx := context.Background()
	if err := s.InvalidateAccount(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	fresh := issue(t, s, accountA, "*")
	rows, total, err := s.Sessions(ctx, fresh.Token, 0, 20, true)
	if err != nil || total != 1 || len(rows) != 1 || !rows[0].IsCurrent {
		t.Fatalf("invalidated sessions listed as active: total=%d rows=%d err=%v", total, len(rows), err)
	}
	if _, err = s.Authenticate(ctx, old.Token); err != ErrStale {
		t.Fatal(err)
	}
	if _, err = s.Refresh(ctx, old.RefreshToken, 0, ""); err != ErrStale {
		t.Fatal(err)
	}
}
