// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/walletpg"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

func TestPGRecurringProductCheckoutExpiryDuringWalletWait(t *testing.T) {
	f := newRecurringProductFixture(t)
	o := recurringOrder(t, f, "expiry-during-wallet-wait")
	o.ExpiredAt = time.Unix(f.clock.Load(), 0).UTC().Add(30 * time.Second)
	raw, e := json.Marshal(o)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE commerce_v1.orders SET expires_at=$2::timestamptz,document=$3::jsonb WHERE id=$1::uuid`, o.ID, o.ExpiredAt, raw)
	blocker, pid := atomicBlocker(t, f.atomicAuthorityFixture, "wallet_v1.wallets", f.wallet.ID)
	done := make(chan error, 1)
	go func() { _, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "wallet_v1.wallets")
	f.clock.Store(o.ExpiredAt.Unix())
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, c.ErrConflict) {
		t.Fatal("checkout deadline was extended by wallet wait", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.pockets WHERE wallet_id=$1::uuid AND currency='synthetic-commerce' AND (amount<>0 OR held_amount<>0)`, f.payee.ID); n != 0 {
		t.Fatal("late payment credited beneficiary", n)
	}
	// Inspect stored row/document state: public GET deliberately projects an
	// elapsed unpaid order as expired even before its durable expiry transition.
	var status int
	var transactionID sql.NullString
	if e = f.db.QueryRow(`SELECT status,transaction_id::text,document FROM commerce_v1.orders WHERE id=$1::uuid`, o.ID).Scan(&status, &transactionID, &raw); e != nil {
		t.Fatal(e)
	}
	var current c.Order
	if e = json.Unmarshal(raw, &current); e != nil || status != int(c.Unpaid) || transactionID.Valid || current.Status != c.Unpaid || current.TransactionID != nil {
		t.Fatal("late payment partially committed", status, transactionID, current, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.ID); n != 0 {
		t.Fatal("late payment retained ledger transaction", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND identifier=$2::text`, f.actor, "app-product:"+f.product); n != 0 {
		t.Fatal("late payment retained subscription", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.order.paid'`, o.ID); n != 0 {
		t.Fatal("late payment retained paid outbox event", n)
	}
	// The existing expiry transition remains durable on a subsequent request.
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT status,document FROM commerce_v1.orders WHERE id=$1::uuid`, o.ID).Scan(&status, &raw); e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &current); e != nil || status != int(c.Expired) || current.Status != c.Expired {
		t.Fatal(status, current, e)
	}
}
