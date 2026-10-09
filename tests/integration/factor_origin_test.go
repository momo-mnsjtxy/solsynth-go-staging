// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

func factorOriginTarget(t *testing.T, f *factorSetupFixture, disabled bool) signin.Factor {
	t.Helper()
	factor := f.create(t, 3)
	if disabled {
		uri := factor.CreatedResponse["uri"]
		if _, e := f.service.EnableFactor(f.ctx, f.pair.Token, factor.ID, syntheticTOTP(t, uri, f.now.Load())); e != nil {
			t.Fatal(e)
		}
		f.now.Add(30)
		f.pair = f.credentialLogin(t, factor.ID, syntheticTOTP(t, uri, f.now.Load()))
		if e := f.service.DisableFactor(f.ctx, f.pair.Token, factor.ID); e != nil {
			t.Fatal(e)
		}
		f.now.Add(30)
	}
	return factor
}

func TestPGFactorSetupAccountSecurityRestoration(t *testing.T) {
	for _, flag := range []string{"disabled", "superuser"} {
		for _, disabled := range []bool{false, true} {
			t.Run(flag+map[bool]string{false: "/pending", true: "/reenable"}[disabled], func(t *testing.T) {
				f := newFactorSetupFixture(t)
				other := newFactorSetupFixture(t)
				factor := factorOriginTarget(t, f, disabled)
				control := other.create(t, 3)
				before := factorPolicyVersion(t, f)
				otherBefore := factorPolicyVersion(t, other)
				store := authoritypg.New(f.db)
				var version int64
				for _, value := range []bool{true, false} {
					e := store.WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
						a := tx.Account()
						version = a.Version
						if flag == "disabled" {
							a.Disabled = value
						} else {
							a.Superuser = value
						}
						return tx.SaveAccount(a)
					})
					if e != nil {
						t.Fatal(e)
					}
				}
				if factorPolicyVersion(t, f) != before+2 {
					t.Fatal("canonical account security ABA lost history")
				}
				var current int64
				if e := f.db.QueryRow(`SELECT version FROM owned_authority_accounts WHERE id=$1::uuid`, f.account.ID).Scan(&current); e != nil || current != version {
					t.Fatal("test did not isolate unchanged account version", current, version, e)
				}
				assertFactorSetupState(t, f, false, "cancelled")
				f.pair = f.credentialLogin(t, "", "")
				path := "/stargate/factors/" + factor.ID + "/enable"
				code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
				f.call(t, "POST", path, f.pair.Token, code, true, 401)
				if factorPolicyVersion(t, other) != otherBefore {
					t.Fatal("unrelated account history changed")
				}
				other.call(t, "POST", "/stargate/factors/"+control.ID+"/enable", other.pair.Token, syntheticTOTP(t, control.CreatedResponse["uri"], other.now.Load()), true, 200)
			})
		}
	}
}

func TestPGFactorSetupAccountSecurityRollbackAndProfileWrites(t *testing.T) {
	for _, flag := range []string{"disabled", "superuser"} {
		t.Run(flag, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			factor := f.create(t, 3)
			before := factorPolicyVersion(t, f)
			store := authoritypg.New(f.db)
			rollback := errors.New("synthetic account security rollback")
			e := store.WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
				a := tx.Account()
				if flag == "disabled" {
					a.Disabled = true
				} else {
					a.Superuser = true
				}
				if e := tx.SaveAccount(a); e != nil {
					return e
				}
				return rollback
			})
			if !errors.Is(e, rollback) {
				t.Fatal(e)
			}
			if e = store.WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
				a := tx.Account()
				if a.Disabled || a.Superuser {
					t.Fatal("rollback retained a flag")
				}
				if e := tx.SaveAccount(a); e != nil {
					return e
				}
				a.Nick = "Synthetic profile edit"
				a.Language = "en"
				return tx.SaveAccount(a)
			}); e != nil {
				t.Fatal(e)
			}
			if factorPolicyVersion(t, f) != before {
				t.Fatal("rollback/no-op/profile edit changed security history")
			}
			assertFactorSetupState(t, f, true, "pending")
			f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()), true, 200)
		})
	}
}

func TestPGFactorSetupSessionOriginFreshFallback(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, change := range []string{"none", "refresh", "scope_restore", "shorten_restore", "revoke", "rollback"} {
			t.Run(map[bool]string{false: "pending/", true: "reenable/"}[disabled]+change, func(t *testing.T) {
				f := newFactorSetupFixture(t)
				factor := factorOriginTarget(t, f, disabled)
				var originID string
				var generation int64
				if e := f.db.QueryRow(`SELECT session_id::text,authorization_epoch FROM owned_authority_factor_setups WHERE account_id=$1::uuid`, f.account.ID).Scan(&originID, &generation); e != nil {
					t.Fatal(e)
				}
				// Disable stales the setup session itself. The revocation API is
				// exercised with a separately completed current credential login.
				f.pair = f.credentialLogin(t, "", "")
				store := authoritypg.New(f.db)
				if change == "revoke" {
					if e := f.issuer.Revoke(f.ctx, f.pair.Token, originID); e != nil {
						t.Fatal(e)
					}
				} else if change != "none" {
					rollback := errors.New("synthetic session security rollback")
					e := store.WithinAccount(f.ctx, f.account.ID, func(tx authority.Transaction) error {
						origin, e := tx.Session(originID)
						if e != nil {
							return e
						}
						oldScopes, oldExpiry := append([]string(nil), origin.Scopes...), origin.ExpiresAt
						switch change {
						case "refresh":
							origin.Epoch++
							origin.ExpiresAt = origin.ExpiresAt.Add(time.Minute)
						case "scope_restore", "rollback":
							origin.Scopes = []string{"posts.read"}
						case "shorten_restore":
							origin.ExpiresAt = origin.ExpiresAt.Add(-time.Minute)
						}
						if e = tx.SaveSession(origin); e != nil {
							return e
						}
						if change == "rollback" {
							return rollback
						}
						if change == "refresh" {
							return nil
						}
						origin, e = tx.Session(originID)
						if e != nil {
							return e
						}
						origin.Scopes, origin.ExpiresAt = oldScopes, oldExpiry
						return tx.SaveSession(origin)
					})
					if change == "rollback" {
						if !errors.Is(e, rollback) {
							t.Fatal(e)
						}
					} else if e != nil {
						t.Fatal(e)
					}
				}
				var current int64
				if e := f.db.QueryRow(`SELECT authorization_epoch FROM owned_authority_sessions WHERE id=$1::uuid`, originID).Scan(&current); e != nil {
					t.Fatal(e)
				}
				invalid := change == "scope_restore" || change == "shorten_restore" || change == "revoke"
				if invalid && current <= generation || !invalid && current != generation {
					t.Fatal("unexpected canonical origin generation", current, generation)
				}
				// Fresh fallback may repair expiry, but cannot repair security history.
				f.now.Add(301)
				f.pair = f.credentialLogin(t, "", "")
				status := 200
				if invalid {
					status = 401
				}
				f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()), true, status)
				if invalid {
					assertFactorSetupState(t, f, false, "cancelled")
					f.pair = f.credentialLogin(t, "", "")
					f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()), true, 401)
				}
			})
		}
	}
}
