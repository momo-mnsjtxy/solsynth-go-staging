// SPDX-License-Identifier: AGPL-3.0-only
package realmboostpg

import (
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	"testing"
)

func TestOwnedPolicyRequiredAndCopied(t *testing.T) {
	id := "10000000-0000-4000-8000-000000000001"
	g := &authoritypg.TransactionGuard{}
	cfg := Config{TreasuryAccountID: id, TreasuryWalletID: id}
	if _, e := New(&sql.DB{}, g, &commercepg.Repository{}, cfg); !errors.Is(e, b.ErrUnavailable) {
		t.Fatal(e)
	}
	cfg.Policy = &OwnedPolicy{LabelCaps: [4]int{0, 5, 10, 20}, PerkLevels: map[string]int{"configured": 2}}
	r, e := New(&sql.DB{}, g, &commercepg.Repository{}, cfg)
	if e != nil {
		t.Fatal(e)
	}
	cfg.Policy.PerkLevels["configured"] = 3
	cfg.Policy.LabelCaps[1] = 99
	if r.cfg.Policy.PerkLevels["configured"] != 2 || r.cfg.Policy.LabelCaps[1] != 5 {
		t.Fatal("mutable config")
	}
	cfg.Policy.LabelCaps = [4]int{1, 2, 3, 4}
	if _, e = New(&sql.DB{}, g, &commercepg.Repository{}, cfg); !errors.Is(e, b.ErrInvalid) {
		t.Fatal(e)
	}
}
