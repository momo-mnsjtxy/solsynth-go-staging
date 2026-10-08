// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
)

// Empty policy restoration must not erase evidence that an enrollment's
// authorization policy changed. Live row xmin/ID snapshots alone lose a row
// that was inserted and then deleted between the two observations.
func TestPGFactorSetupEmptyPolicyRestorationDoesNotRevive(t *testing.T) {
	for _, kind := range []string{"grant", "punishment"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			factor := f.create(t, 3)
			store := authoritypg.New(f.db)
			var rules []authority.Rule
			var punishments []authority.Punishment
			if kind == "grant" {
				rules = []authority.Rule{{Pattern: "accounts.*", Deny: true}}
			} else {
				punishments = []authority.Punishment{{Kind: "disable_account", Pattern: "*"}}
			}
			if e := store.ReplacePolicy(f.ctx, f.account.ID, rules, punishments); e != nil {
				t.Fatal(e)
			}
			if e := store.ReplacePolicy(f.ctx, f.account.ID, nil, nil); e != nil {
				t.Fatal(e)
			}
			path := "/stargate/factors/" + factor.ID + "/enable"
			code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
			f.call(t, "POST", path, f.pair.Token, code, true, 401)
			var valid bool
			var state string
			if e := f.db.QueryRow(`SELECT p.valid,s.state FROM owned_authority_pending_enrollments p JOIN owned_authority_factor_setups s ON s.account_id=p.account_id WHERE p.account_id=$1::uuid`, f.account.ID).Scan(&valid, &state); e != nil || valid || state != "cancelled" {
				t.Fatal("policy change did not cancel pending provenance", valid, state, e)
			}
			f.pair = f.credentialLogin(t, "", "")
			f.call(t, "POST", path, f.pair.Token, code, true, 401)
		})
	}
}

// Policy deletion followed by recreation of the same content is a security
// transition even when the final effective permissions are identical.
func TestPGFactorSetupPolicyDeleteRecreateAndAccountScope(t *testing.T) {
	for _, kind := range []string{"grant", "punishment", "membership", "group_grant", "credentials", "factor"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			other := newFactorSetupFixture(t)
			group := authorityUUID(t)
			query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_factor_policy_"+group)
			var id int64
			switch kind {
			case "grant":
				if e := f.db.QueryRow(`INSERT INTO owned_authority_grants(account_id,pattern)VALUES($1::uuid,'posts.read')RETURNING id`, f.account.ID).Scan(&id); e != nil {
					t.Fatal(e)
				}
			case "punishment":
				if e := f.db.QueryRow(`INSERT INTO owned_authority_punishments(account_id,kind,pattern)VALUES($1::uuid,'permission_modification','synthetic.unused')RETURNING id`, f.account.ID).Scan(&id); e != nil {
					t.Fatal(e)
				}
			case "membership", "group_grant":
				query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group)
				if kind == "group_grant" {
					if e := f.db.QueryRow(`INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1::uuid,'posts.read')RETURNING id`, group).Scan(&id); e != nil {
						t.Fatal(e)
					}
				}
			}
			factor := f.create(t, 3)
			unrelated := other.create(t, 3)
			before := factorPolicyVersion(t, f)
			otherBefore := factorPolicyVersion(t, other)
			tx, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if _, e = tx.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, f.account.ID); e != nil {
				t.Fatal(e)
			}
			var commands []string
			var args []any
			switch kind {
			case "grant":
				commands = []string{`DELETE FROM owned_authority_grants WHERE id=$1::bigint`, `INSERT INTO owned_authority_grants(id,account_id,pattern)VALUES($1::bigint,$2::uuid,'posts.read')`}
				args = []any{id, f.account.ID}
			case "punishment":
				commands = []string{`DELETE FROM owned_authority_punishments WHERE id=$1::bigint`, `INSERT INTO owned_authority_punishments(id,account_id,kind,pattern)VALUES($1::bigint,$2::uuid,'permission_modification','synthetic.unused')`}
				args = []any{id, f.account.ID}
			case "membership":
				commands = []string{`DELETE FROM owned_authority_group_members WHERE account_id=$1::uuid AND group_id=$2::uuid`, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`}
				args = []any{f.account.ID, group}
			case "group_grant":
				commands = []string{`DELETE FROM owned_authority_group_grants WHERE id=$1::bigint`, `INSERT INTO owned_authority_group_grants(id,group_id,pattern)VALUES($1::bigint,$2::uuid,'posts.read')`}
				args = []any{id, group}
			case "credentials":
				commands = []string{`UPDATE owned_authority_credentials SET verified_at=NULL WHERE account_id=$1::uuid`, `UPDATE owned_authority_credentials SET verified_at=$2::timestamptz WHERE account_id=$1::uuid`}
				var verified time.Time
				if e = tx.QueryRow(`SELECT verified_at FROM owned_authority_credentials WHERE account_id=$1::uuid`, f.account.ID).Scan(&verified); e != nil {
					t.Fatal(e)
				}
				args = []any{f.account.ID, verified}
			case "factor":
				// Preserve the exact UUID, secret, deadline and revision in a temporary
				// row; deleting/recreating it must not restore pending provenance.
				commands = []string{`CREATE TEMP TABLE synthetic_factor_restore ON COMMIT DROP AS SELECT * FROM owned_authority_factors WHERE id=$1::uuid`, `DELETE FROM owned_authority_factors WHERE id=$1::uuid`, `INSERT INTO owned_authority_factors SELECT * FROM synthetic_factor_restore`}
				args = []any{factor.ID}
			}
			for _, command := range commands {
				n := 0
				if strings.Contains(command, "$1") {
					n = 1
				}
				if strings.Contains(command, "$2") {
					n = 2
				}
				if _, e = tx.Exec(command, args[:n]...); e != nil {
					t.Fatal(e)
				}
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if factorPolicyVersion(t, f) <= before {
				t.Fatal("policy history did not advance")
			}
			assertFactorSetupState(t, f, false, "cancelled")
			if factorPolicyVersion(t, other) != otherBefore {
				t.Fatal("unrelated account history changed")
			}
			assertFactorSetupState(t, other, true, "pending")
			assertFactorSetupCannotRevive(t, f, factor)
			other.call(t, "POST", "/stargate/factors/"+unrelated.ID+"/enable", other.pair.Token, syntheticTOTP(t, unrelated.CreatedResponse["uri"], other.now.Load()), true, 200)
		})
	}
}

func TestPGFactorSetupEmptyGroupHistoryAndMemberScope(t *testing.T) {
	for _, kind := range []string{"membership", "group_grant"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			group := authorityUUID(t)
			query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_factor_group_"+group)
			if kind == "group_grant" {
				query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group)
			}
			factor := f.create(t, 3)
			if kind == "membership" {
				query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group)
				query(t, f.db, `DELETE FROM owned_authority_group_members WHERE account_id=$1::uuid AND group_id=$2::uuid`, f.account.ID, group)
			} else {
				query(t, f.db, `INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1::uuid,'posts.read')`, group)
				query(t, f.db, `DELETE FROM owned_authority_group_grants WHERE group_id=$1::uuid`, group)
			}
			assertFactorSetupState(t, f, false, "cancelled")
			assertFactorSetupCannotRevive(t, f, factor)
		})
	}
}

func TestPGFactorSetupPolicyHistoryRollback(t *testing.T) {
	for _, kind := range []string{"grant", "punishment", "membership", "group_grant", "credentials", "factor"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactorSetupFixture(t)
			group := authorityUUID(t)
			query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_factor_rollback_"+group)
			if kind == "group_grant" {
				query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group)
			}
			factor := f.create(t, 3)
			before := factorPolicyVersion(t, f)
			var groupBefore int64
			if e := f.db.QueryRow(`SELECT factor_policy_version FROM owned_authority_groups WHERE id=$1::uuid`, group).Scan(&groupBefore); e != nil {
				t.Fatal(e)
			}
			tx, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			if _, e = tx.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, f.account.ID); e != nil {
				t.Fatal(e)
			}
			command := map[string]string{
				"grant":       `INSERT INTO owned_authority_grants(account_id,pattern)VALUES($1::uuid,'posts.read')`,
				"punishment":  `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'block_login')`,
				"membership":  `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`,
				"group_grant": `INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1::uuid,'posts.read')`,
				"credentials": `UPDATE owned_authority_credentials SET verified_at=NULL WHERE account_id=$1::uuid`,
				"factor":      `DELETE FROM owned_authority_factors WHERE account_id=$1::uuid AND type=3`,
			}[kind]
			args := []any{f.account.ID}
			if kind == "group_grant" {
				args[0] = group
			}
			if strings.Contains(command, "$2") {
				args = append(args, group)
			}
			if _, e = tx.Exec(command, args...); e != nil {
				t.Fatal(e)
			}
			var valid bool
			var state string
			var changed int64
			if e = tx.QueryRow(`SELECT a.factor_policy_version,p.valid,s.state FROM owned_authority_accounts a JOIN owned_authority_pending_enrollments p ON p.account_id=a.id JOIN owned_authority_factor_setups s ON s.account_id=a.id WHERE a.id=$1::uuid`, f.account.ID).Scan(&changed, &valid, &state); e != nil || changed <= before || valid || state != "cancelled" {
				t.Fatal("missing transactional cancellation", changed, valid, state, e)
			}
			if e = tx.Rollback(); e != nil {
				t.Fatal(e)
			}
			if factorPolicyVersion(t, f) != before {
				t.Fatal("rolled-back history persisted")
			}
			var groupAfter int64
			if e = f.db.QueryRow(`SELECT factor_policy_version FROM owned_authority_groups WHERE id=$1::uuid`, group).Scan(&groupAfter); e != nil || groupAfter != groupBefore {
				t.Fatal("group history escaped rollback", groupAfter, groupBefore, e)
			}
			assertFactorSetupState(t, f, true, "pending")
			f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()), true, 200)
		})
	}
}

// A group grant transaction can miss a joining member in its committed-row
// enumeration. The group version must still remember an insert/delete cycle
// that commits after this newly joined account creates its setup receipt.
func TestPGFactorSetupConcurrentJoiningMemberPolicyHistory(t *testing.T) {
	f := newFactorSetupFixture(t)
	group := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_factor_join_"+group)
	join, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer join.Rollback()
	if _, e = join.Exec(`SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, f.account.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = join.Exec(`INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group); e != nil {
		t.Fatal(e)
	}
	grants, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer grants.Rollback()
	if _, e = grants.Exec(`SET LOCAL lock_timeout='2s'`); e != nil {
		t.Fatal(e)
	}
	if _, e = grants.Exec(`INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1::uuid,'posts.read')`, group); e != nil {
		t.Fatal(e)
	}
	if _, e = grants.Exec(`DELETE FROM owned_authority_group_grants WHERE group_id=$1::uuid`, group); e != nil {
		t.Fatal(e)
	}
	if e = join.Commit(); e != nil {
		t.Fatal(e)
	}
	factor := f.create(t, 3)
	before := factorPolicyVersion(t, f)
	if e = grants.Commit(); e != nil {
		t.Fatal(e)
	}
	// This schedule deliberately bypasses the current-member account bump; the
	// durable group version, not an incidental account trigger, must reject it.
	if factorPolicyVersion(t, f) != before {
		t.Fatal("fixture did not isolate unseen joining member")
	}
	assertFactorSetupState(t, f, true, "pending")
	assertFactorSetupCannotRevive(t, f, factor)
	assertFactorSetupState(t, f, false, "cancelled")
}

func TestPGFactorSetupNoopAndCounterAdvancePreserveHistory(t *testing.T) {
	f := newFactorSetupFixture(t)
	group := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_factor_noop_"+group)
	query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, f.account.ID, group)
	factor := f.create(t, 3)
	before := factorPolicyVersion(t, f)
	query(t, f.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)ON CONFLICT DO NOTHING`, f.account.ID, group)
	query(t, f.db, `UPDATE owned_authority_credentials SET verified_at=verified_at WHERE account_id=$1::uuid`, f.account.ID)
	query(t, f.db, `UPDATE owned_authority_factors SET last_counter=last_counter+1 WHERE id=$1::uuid`, factor.ID)
	if factorPolicyVersion(t, f) != before {
		t.Fatal("non-security update changed history")
	}
	assertFactorSetupState(t, f, true, "pending")
	f.call(t, "POST", "/stargate/factors/"+factor.ID+"/enable", f.pair.Token, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()), true, 200)
}

func factorPolicyVersion(t *testing.T, f *factorSetupFixture) int64 {
	t.Helper()
	var version int64
	if e := f.db.QueryRow(`SELECT factor_policy_version FROM owned_authority_accounts WHERE id=$1::uuid`, f.account.ID).Scan(&version); e != nil {
		t.Fatal(e)
	}
	return version
}
func assertFactorSetupState(t *testing.T, f *factorSetupFixture, wantValid bool, wantState string) {
	t.Helper()
	var valid bool
	var state string
	if e := f.db.QueryRow(`SELECT p.valid,s.state FROM owned_authority_pending_enrollments p JOIN owned_authority_factor_setups s ON s.account_id=p.account_id WHERE p.account_id=$1::uuid`, f.account.ID).Scan(&valid, &state); e != nil || valid != wantValid || state != wantState {
		t.Fatal("setup provenance", valid, state, e)
	}
}
func assertFactorSetupCannotRevive(t *testing.T, f *factorSetupFixture, factor signin.Factor) {
	t.Helper()
	path := "/stargate/factors/" + factor.ID + "/enable"
	code := syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load())
	f.call(t, "POST", path, f.pair.Token, code, true, 401)
	f.pair = f.credentialLogin(t, "", "")
	f.call(t, "POST", path, f.pair.Token, code, true, 401)
}
