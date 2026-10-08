// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"testing"
)

func TestPGWalletManagedTransactionPurposeScopes(t *testing.T) {
	f := newFundsFixture(t)
	packet := f.create(t, map[string]any{"total_amount": 0.006, "recipient_account_ids": []string{f.one.actor, f.two.actor, f.three.actor}})
	for _, person := range []fundPerson{f.one, f.two, f.three} {
		f.claim(t, packet.ID, person, 200)
		f.claim(t, packet.ID, person, 200)
		f.balance(t, person, "100.002", "0.000")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE purpose='fund' AND managed_by=$1`, packet.ID); n != 3 {
		t.Fatal("fund receipts collided or replay duplicated them", n)
	}
	f.balance(t, f.creator, "99.994", "0.000")
	orders := newCommerceFixture(t)
	o := commerceOrder(t, orders, "managed-index-order", false)
	paid, e := orders.r.PayOrder(orders.ctx, orders.actor, o.ID, &orders.wallet.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = orders.db.Exec(`INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,frozen_at,created_at,updated_at,fingerprint,purpose,managed_by) SELECT $1::uuid,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,frozen_at,created_at,updated_at,fingerprint,purpose,managed_by FROM wallet_v1.transactions WHERE id=$2::uuid`, authorityUUID(t), *paid.TransactionID)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23505" || pg.ConstraintName != "wallet_commerce_order" {
		t.Fatal("duplicate commerce order escaped its unique index", e)
	}
	if n := commerceCount(t, orders.db, `SELECT count(*) FROM wallet_v1.transactions WHERE purpose='commerce' AND managed_by=$1`, o.ID); n != 1 {
		t.Fatal(n)
	}
	for _, purpose := range []string{"transfer", "unconfigured"} {
		_, e = orders.db.Exec(`INSERT INTO wallet_v1.transactions(id,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,frozen_at,created_at,updated_at,fingerprint,purpose,managed_by) SELECT $1::uuid,actor_id,payer_wallet_id,payee_wallet_id,currency,amount,status,is_frozen,require_confirmation,frozen_at,created_at,updated_at,fingerprint,$3,managed_by FROM wallet_v1.transactions WHERE id=$2::uuid`, authorityUUID(t), *paid.TransactionID, purpose)
		if !errors.As(e, &pg) || pg.Code != "23514" {
			t.Fatal("an invalid managed purpose bypassed its check constraint", purpose, e)
		}
	}
	if _, e = orders.r.UpdateOrder(orders.ctx, o.ID, c.UpdateStatus{Credentials: orders.credentials, Status: c.Cancelled}); e != nil {
		t.Fatal(e)
	}
}

func TestPGWalletManagedIndexLegacyMigrationAtomicity(t *testing.T) {
	db := database(t)
	schema := "synthetic_managed_index_" + strings.ReplaceAll(authorityUUID(t), "-", "")
	query(t, db, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	query(t, db, `CREATE TABLE `+schema+`.transactions(id uuid PRIMARY KEY,purpose text NOT NULL,managed_by uuid)`)
	query(t, db, `CREATE UNIQUE INDEX wallet_commerce_order ON `+schema+`.transactions(managed_by) WHERE managed_by IS NOT NULL`)
	key := authorityUUID(t)
	query(t, db, `INSERT INTO `+schema+`.transactions VALUES($1,'fund',$2)`, authorityUUID(t), key)
	raw, e := os.ReadFile("../../internal/commercepg/order_index_scope.sql")
	if e != nil {
		t.Fatal(e)
	}
	migration := strings.ReplaceAll(string(raw), "wallet_v1.", schema+".")
	// Force an error after DROP to verify the enclosing DO rolls back DDL.
	failure := strings.Replace(migration, " EXECUTE 'ALTER INDEX", " RAISE EXCEPTION 'synthetic migration rollback';\n EXECUTE 'ALTER INDEX", 1)
	if failure == migration {
		t.Fatal("rollback injection missing")
	}
	if _, e = db.Exec(failure); e == nil {
		t.Fatal("migration failure not injected")
	}
	_, e = db.Exec(`INSERT INTO `+schema+`.transactions VALUES($1,'fund',$2)`, authorityUUID(t), key)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23505" || pg.ConstraintName != "wallet_commerce_order" {
		t.Fatal("failed replacement dropped old protection", e)
	}
	query(t, db, migration)
	query(t, db, `INSERT INTO `+schema+`.transactions VALUES($1,'fund',$2)`, authorityUUID(t), key)
	query(t, db, `INSERT INTO `+schema+`.transactions VALUES($1,'commerce',$2)`, authorityUUID(t), key)
	_, e = db.Exec(`INSERT INTO `+schema+`.transactions VALUES($1,'commerce',$2)`, authorityUUID(t), key)
	if !errors.As(e, &pg) || pg.Code != "23505" || pg.ConstraintName != "wallet_commerce_order" {
		t.Fatal("upgraded commerce uniqueness missing", e)
	}
	query(t, db, migration) // Reapplication is safe and still preserves the name.
}
