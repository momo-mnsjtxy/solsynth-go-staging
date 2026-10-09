// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"reflect"
	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
)

func TestPGCouponSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	const marker = "coupon-checkout-restart.json"
	type saved struct {
		Paid, Unpaid        c.Order
		Coupon              c.Coupon
		Actor, Payer, Payee string
		Events              []string
	}
	if phase == "before" {
		f := enableCoupons(t, newCommerceFixture(t))
		q := couponRequest(f, "order", []string{"synthetic-widget"})
		q.Terms.MaxUses = 1
		q.Terms.MaxUsesPerAccount = 1
		cp := createCoupon(t, f, q)
		one := discountedOrder(t, f, cp.ID, "saved-paid")
		two := discountedOrder(t, f, cp.ID, "saved-unpaid")
		paid, e := f.r.PayOrder(f.ctx, f.actor, one.ID, &f.wallet.ID)
		if e != nil {
			t.Fatal(e)
		}
		cp, e = f.r.RevokeCoupon(f.ctx, cp.ID, c.RevokeCoupon{Credentials: f.credentials})
		if e != nil {
			t.Fatal(e)
		}
		state := saved{Paid: paid, Unpaid: two, Coupon: cp, Actor: f.actor, Payer: f.wallet.ID, Payee: f.payee.ID}
		rows, e := f.db.Query(`SELECT event_id::text FROM commerce_v1.outbox WHERE aggregate_id=$1 ORDER BY sequence`, paid.ID)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			state.Events = append(state.Events, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil || len(state.Events) != 3 {
			t.Fatal(state.Events, e)
		}
		saveRestartFixture(t, marker, state)
		return
	}
	db := database(t)
	var state saved
	loadRestartFixture(t, marker, &state)
	if !wallet.ValidID(state.Actor) || !wallet.ValidID(state.Payer) || state.Paid.Coupon == nil || state.Paid.TransactionID == nil || state.Unpaid.Coupon == nil || state.Coupon.Enabled {
		t.Fatal("invalid saved coupon fixture")
	}
	for _, want := range []c.Order{state.Paid, state.Unpaid} {
		var raw []byte
		if e := db.QueryRow(`SELECT document FROM commerce_v1.orders WHERE id=$1 AND actor_id=$2 AND amount=$3 AND payee_wallet_id=$4`, want.ID, state.Actor, want.Amount, state.Payee).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var got c.Order
		if e := json.Unmarshal(raw, &got); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("persisted immutable quote changed", got, want)
		}
	}
	var enabled bool
	if e := db.QueryRow(`SELECT enabled FROM commerce_v1.coupons WHERE id=$1`, state.Coupon.ID).Scan(&enabled); e != nil || enabled {
		t.Fatal("revocation lost", enabled, e)
	}
	var raw []byte
	if e := db.QueryRow(`SELECT quote FROM commerce_v1.coupon_redemptions WHERE order_id=$1 AND coupon_id=$2 AND account_id=$3 AND gross=4.247 AND discount=1.001 AND net=3.246`, state.Paid.ID, state.Coupon.ID, state.Actor).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var quote c.CouponQuote
	if e := json.Unmarshal(raw, &quote); e != nil || !reflect.DeepEqual(&quote, state.Paid.Coupon) {
		t.Fatal("saved redemption differs", quote, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1`, state.Coupon.ID); n != 1 {
		t.Fatal("usage changed across restart", n)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM wallet_v1.transactions WHERE id=$1 AND managed_by=$2 AND amount=3.246 AND status=1`, *state.Paid.TransactionID, state.Paid.ID); n != 1 {
		t.Fatal("saved canonical hold missing", n)
	}
	for _, id := range state.Events {
		if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.outbox WHERE event_id=$1 AND aggregate_id=$2 AND published_at IS NULL`, id, state.Paid.ID); n != 1 {
			t.Fatal("saved outbox event lost", id, n)
		}
	}
	walletAssertBalance(t, walletpg.New(db), state.Payer, "synthetic-commerce", "96.754", "3.246", "96.754")
	walletAssertBalance(t, walletpg.New(db), state.Payee, "synthetic-commerce", "0.000", "0.000", "0.000")
}
