// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"testing"
)

// syntheticFederationHistoryTransition probes only final history freshness
// while a later-order job/audit row is held. It is NOT a canonical policy writer
// or grant-ABA test. Never update accounts, grants, punishments or memberships
// from this held transaction: their triggers acquire earlier account locks.
// Real canonical account-first mutation/ABA tests must remain separate.
func syntheticFederationHistoryTransition(t *testing.T, ctx context.Context, tx *sql.Tx, table, actor string) {
	t.Helper()
	assertNoCanonicalWriteLock := func() {
		var bad bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation IN(to_regclass('owned_authority_accounts'),to_regclass('owned_authority_grants'),to_regclass('owned_authority_punishments'),to_regclass('owned_authority_groups'),to_regclass('owned_authority_group_grants'),to_regclass('owned_authority_group_members')) AND mode IN('RowShareLock','RowExclusiveLock','ShareRowExclusiveLock','ExclusiveLock','AccessExclusiveLock'))`).Scan(&bad)
		if err != nil || bad {
			t.Fatal("synthetic later-lock probe must not hold canonical account-affecting write locks", err)
		}
	}
	assertNoCanonicalWriteLock()
	var beforeAccount, afterAccount int64
	if err := tx.QueryRowContext(ctx, `SELECT factor_policy_version FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&beforeAccount); err != nil {
		t.Fatal(err)
	}
	var query string
	switch table {
	case "discovery":
		query = `UPDATE owned_federation_discovery_epochs SET revision=revision+2 WHERE kind='account' AND entity_id=$1::uuid RETURNING revision`
	case "delivery":
		query = `UPDATE owned_federation_delivery_epochs SET revision=revision+2 WHERE kind='account' AND entity_id=$1::uuid RETURNING revision`
	default:
		t.Fatal("unknown synthetic history scope")
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, query, actor).Scan(&revision); err != nil || revision < 3 {
		t.Fatal("synthetic history transition was not applied", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT factor_policy_version FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&afterAccount); err != nil || afterAccount != beforeAccount {
		t.Fatal("synthetic history probe changed canonical policy", err)
	}
	assertNoCanonicalWriteLock()
}
