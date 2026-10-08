// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/sphere/posts"
	rating "solsynth.local/go-migration/ports/sphere/publisherrating"
	sponsor "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

func TestPGBackendPublisherCommerceComposition(t *testing.T) {
	f := newSponsorFixture(t, false)
	query(t, f.db, publisherratingpg.Schema)
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	day := f.config.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, Sponsorship: &sponsorshippg.Config{TreasuryAccountID: f.outside, TreasuryWalletID: f.treasury.ID}, PublisherRatings: &publisherratingpg.Config{ReactionOnly: true, EarliestDay: day}}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	p := f.post(t, f.individual, "Synthetic composed sponsorship", posts.Public)
	path := "/sphere/posts/" + p.ID
	backendJSONCall(t, app, "POST", path+"/sponsor", "", map[string]any{"amount": 5.555}, 401)
	order := backendDecode[sponsor.OrderResponse](t, backendJSONCall(t, app, "POST", path+"/sponsor", f.otherToken, map[string]any{"amount": 5.555}, 200))
	total := backendDecode[struct {
		Total wallet.Amount `json:"total_amount"`
	}](t, backendCall(t, app, "GET", path+"/sponsor", "", "", 200))
	if total.Total.Sign() != 0 {
		t.Fatal("unpaid order promoted", total)
	}
	payPath := "/wallet/orders/" + order.OrderID + "/pay"
	payBody := map[string]any{"payer_wallet_id": f.otherPayer.ID}
	// The composed host requires a real current payment policy before checkout.
	backendJSONCall(t, app, "POST", payPath, f.otherToken, payBody, 503)
	query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1::uuid,false,$2::timestamptz)`, f.other, f.config.Now())
	blocker, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if _, e = blocker.Exec(`SELECT id FROM owned_publishers WHERE id=$1::uuid FOR UPDATE`, f.individual.ID); e != nil {
		t.Fatal(e)
	}
	retry := backendJSONCall(t, app, "POST", payPath, f.otherToken, payBody, 409)
	var errorBody struct {
		Code string `json:"code"`
	}
	errorBody = backendDecode[struct {
		Code string `json:"code"`
	}](t, retry)
	if errorBody.Code != "ORDER_RETRY_REQUIRED" {
		t.Fatal(errorBody)
	}
	if e = blocker.Rollback(); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "100.000", "0.000", "100.000")
	paid := backendDecode[commerce.Order](t, backendJSONCall(t, app, "POST", payPath, f.otherToken, payBody, 200))
	if paid.Status != commerce.Finished || paid.TransactionID == nil {
		t.Fatal(paid)
	}
	replay := backendDecode[commerce.Order](t, backendJSONCall(t, app, "POST", payPath, f.otherToken, payBody, 200))
	if replay.TransactionID == nil || *replay.TransactionID != *paid.TransactionID {
		t.Fatal("HTTP payment replay changed ledger", replay)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "94.445", "0.000", "94.445")
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "5.555", "0.000", "5.555")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.payment_authorizations WHERE account_id=$1::uuid AND consumed_at IS NOT NULL`, f.other); n != 1 {
		t.Fatal("final atomic payment check did not consume proof", n)
	}
	history := backendDecode[[]sponsor.Bid](t, backendCall(t, app, "GET", path+"/sponsor/history", f.otherToken, "", 200))
	if len(history) != 1 || history[0].Amount.String() != "5.555" {
		t.Fatal(history)
	}
	before := backendDecode[rating.Overview](t, backendCall(t, app, "GET", "/sphere/publishers/"+f.individual.Name+"/rating/overview", "", "", 200))
	if before.Rating != 100 || before.Policy != rating.Policy || before.AwardScoreIncluded {
		t.Fatal(before)
	}
	query(t, f.db, `UPDATE owned_text_posts SET created_at=$2::timestamptz WHERE id=$1::uuid`, p.ID, day.Add(12*time.Hour))
	backendJSONCall(t, app, "POST", path+"/reactions", f.otherToken, map[string]any{"symbol": "synthetic-composed-positive", "attitude": 0}, 200)
	// Other synthetic fixtures may leave a larger eligible rating backlog.
	// Drain bounded batches until this publisher's durable receipt is observed.
	workCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		n, x := app.ProcessPublisherCommerceDue(workCtx, 100)
		if x != nil {
			t.Fatal(x)
		}
		var recorded bool
		if x = f.db.QueryRowContext(workCtx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid AND period_start=$2::timestamptz)`, f.individual.ID, day).Scan(&recorded); x != nil {
			t.Fatal(x)
		}
		if recorded {
			break
		}
		if n == 0 {
			t.Fatal("publisher worker stopped before the expected receipt")
		}
	}
	winner := backendDecode[sponsor.Current](t, backendCall(t, app, "GET", "/api/posts/sponsor/current", "", "", 200))
	if !winner.Sponsored || winner.Post == nil || winner.Post.ID != p.ID {
		t.Fatal(winner)
	}
	overview := backendDecode[rating.Overview](t, backendCall(t, app, "GET", "/api/publishers/"+f.individual.ID+"/rating/overview", "", "", 200))
	if overview.Rating != 101 || overview.AwardScoreIncluded {
		t.Fatal(overview)
	}
	if _, e = app.ProcessPublisherCommerceDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid`, f.individual.ID); n != 1 {
		t.Fatal(n)
	}
	pending := backendDecode[sponsor.OrderResponse](t, backendJSONCall(t, app, "POST", path+"/sponsor", f.otherToken, map[string]any{"amount": 6}, 200))
	private := posts.Private
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	backendJSONCall(t, app, "POST", "/api/orders/"+pending.OrderID+"/pay", f.otherToken, payBody, 404)
	winner = backendDecode[sponsor.Current](t, backendCall(t, app, "GET", "/sphere/posts/sponsor/current", f.token, "", 200))
	if winner.Sponsored {
		t.Fatal("private post still promoted", winner)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "94.445", "0.000", "94.445")
}
