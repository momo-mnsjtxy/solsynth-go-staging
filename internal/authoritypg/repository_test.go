// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

func TestRepositoryRejectsMissingStoreAndCrossAccountWrites(t *testing.T) {
	ctx := context.Background()
	r := New(nil)
	if _, e := r.LookupAccount(ctx, "synthetic_missing"); !errors.Is(e, authority.ErrNotFound) {
		t.Fatal(e)
	}
	if e := r.WithinAccount(ctx, "a1000000-0000-4000-8000-000000000001", func(authority.Transaction) error { return nil }); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
	if _, _, e := r.FindRefresh(ctx, make([]byte, 32)); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := r.FindCode(ctx, make([]byte, 32)); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
	tx := &transaction{account: authority.Account{ID: "a1000000-0000-4000-8000-000000000001", Version: 2}}
	if e := tx.SaveAccount(authority.Account{ID: tx.account.ID, Version: 1}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("version downgrade", e)
	}
	if e := tx.SaveSession(authority.Session{AccountID: "a1000000-0000-4000-8000-000000000002"}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
	if e := tx.SaveCode(authority.LoginCode{AccountID: "a1000000-0000-4000-8000-000000000002"}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal(e)
	}
}
func TestSeedSyntheticRequiresDisposableFixtureShape(t *testing.T) {
	if e := SeedSynthetic(context.Background(), nil, "a1000000-0000-4000-8000-000000000001", "realistic-account", time.Now()); e == nil {
		t.Fatal("non-fixture seed accepted")
	}
}
