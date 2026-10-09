// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/postawardpg"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/interactions"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	"solsynth.local/go-migration/ports/sphere/posts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sync"
	"testing"
	"time"
)

type postAwardFixture struct {
	*realmPublishingFixture
	awards                       *postawardpg.Repository
	checkout                     *commercepg.Repository
	payer, beneficiary, treasury wallet.Wallet
	cfg                          postawardpg.Config
}

func newPostAwardFixture(t *testing.T, policy string) *postAwardFixture {
	t.Helper()
	f := &postAwardFixture{realmPublishingFixture: newRealmPublishingFixture(t)}
	query(t, f.db, postawardpg.Schema)
	f.payer = walletCreate(t, walletpg.New(f.db), f.other)
	f.beneficiary = walletCreate(t, walletpg.New(f.db), f.actor)
	f.treasury = walletCreate(t, walletpg.New(f.db), f.outside)
	query(t, f.db, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,amount,created_at,updated_at)VALUES($1::uuid,$2::uuid,'points',100,$3::timestamptz,$3::timestamptz)`, authorityUUID(t), f.payer.ID, f.config.Now())
	f.checkout = commercepg.New(f.db, f.guard, commercepg.Adapters{}).WithClock(f.config.Now)
	f.cfg = postawardpg.Config{TreasuryAccountID: f.outside, TreasuryWalletID: f.treasury.ID, Policy: policy, Now: f.config.Now}
	var e error
	f.awards, e = postawardpg.New(f.db, f.guard, f.checkout, f.social, f.cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.checkout.WithPostAwards(f.awards)
	if e = f.awards.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.awards.BindWallet(f.ctx, f.actor, fixturePublisherHandle(f.individual), a.WalletPatch{WalletID: &f.beneficiary.ID}); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *postAwardFixture) post(t *testing.T, pub accounts.Publisher, body string, visibility int) posts.Post {
	t.Helper()
	at := f.config.Now()
	p, e := f.posts.Create(f.ctx, f.actor, pub.ID, posts.Patch{Content: &body, Visibility: &visibility, PublishedAt: &at})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *postAwardFixture) order(t *testing.T, p posts.Post, key, amount string, attitude int) a.OrderResponse {
	t.Helper()
	o, e := f.awards.CreateOrder(f.otherCtx, f.other, p.ID, key, a.Request{Amount: walletAmount(t, amount), Attitude: attitude, Message: postString("Synthetic paid award")})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func (f *postAwardFixture) pay(t *testing.T, o a.OrderResponse) c.Order {
	t.Helper()
	paid, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.payer.ID)
	if e != nil || paid.Status != c.Finished || paid.TransactionID == nil {
		t.Fatal(paid, e)
	}
	return paid
}
func TestPGPostAwardCheckoutReceiptAndConservation(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic funded award", posts.Public)
	o := f.order(t, p, "positive", "5.125", a.Positive)
	if rows, n, e := f.awards.List(f.ctx, "", p.ID, a.Page{}); e != nil || n != 0 || len(rows) != 0 {
		t.Fatal(rows, n, e)
	}
	draft, e := f.checkout.GetOrder(f.otherCtx, f.other, o.OrderID, false)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(f.otherCtx, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = f.awards.FulfillPostAward(f.otherCtx, f.db, tx, f.other, draft, f.config.Now())
	tx.Rollback()
	if !errors.Is(e, a.ErrForbidden) {
		t.Fatal("unpaid fulfillment", e)
	}
	paid := f.pay(t, o)
	if again := f.pay(t, o); *again.TransactionID != *paid.TransactionID {
		t.Fatal("duplicate payment")
	}
	if again := f.order(t, p, "positive", "5.125", a.Positive); again.OrderID != o.OrderID {
		t.Fatal("duplicate order")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "94.875", "0.000", "94.875")
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "points", "0.000", "5.125", "0.000")
	pending, e := f.awards.Pending(f.ctx, "", p.ID)
	if e != nil || pending.Count != 1 || pending.TotalAmount.String() != "5.125" || pending.PayoutAmount.String() != "5.125" {
		t.Fatal(pending, e)
	}
	read, e := f.posts.Get(f.ctx, "", p.ID)
	if e != nil || read.AwardedScore.String() != "5.125" {
		t.Fatal(read, e)
	}
	if n, e := f.awards.ProcessDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := f.awards.ProcessDue(context.Background(), 100); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "points", "0.000", "0.000", "0.000")
	walletAssertBalance(t, walletpg.New(f.db), f.beneficiary.ID, "points", "5.125", "0.000", "5.125")
}

func TestPGPublisherRewardPreviewViewerAndScopedSettlementRoutes(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "publisher reward preview", posts.Public)
	f.pay(t, f.order(t, p, "preview", "5", a.Positive))
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.individual.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Viewer}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.accounts.ChangeMember(f.otherCtx, f.other, f.individual.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	preview, e := f.accounts.PublisherRewards(f.otherCtx, f.other, f.individual.ID)
	if e != nil || preview.PendingCount != 1 || preview.PendingAmount != "5.000" {
		t.Fatal(preview, e)
	}
	scoped := func(permission string) context.Context {
		pair, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "reward settlement", Scopes: []string{permission}})
		if e != nil {
			t.Fatal(e)
		}
		proof, e := f.verifier.Authenticate(context.Background(), pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		return f.guard.Bind(context.Background(), proof)
	}
	if _, e = f.awards.SettlePublisherRewards(scoped("publishers.rewards.resettle"), f.actor, 100); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("wrong settlement scope accepted", e)
	}
	if n, e := f.awards.SettlePublisherRewards(scoped("publishers.rewards.settle"), f.actor, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	preview, e = f.accounts.PublisherRewards(f.otherCtx, f.other, f.individual.ID)
	if e != nil || preview.PendingCount != 0 || preview.SettledAmount != "5.000" {
		t.Fatal(preview, e)
	}
}

func TestPGPublisherRewardResettleSkipsReceiptsFromPreviousTreasury(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "treasury rotation reward", posts.Public)
	old := f.order(t, p, "old-treasury", "5", a.Positive)
	f.pay(t, old)
	query(t, f.db, `UPDATE owned_post_award_receipts SET attempts=7,next_attempt_at=clock_timestamp()+interval '1 day' WHERE purchase_id IN(SELECT id FROM owned_post_award_purchases WHERE order_id=$1::uuid)`, old.OrderID)

	newTreasury := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(context.Background(), f.db, newTreasury, "synthetic_rotated_treasury_"+newTreasury[:8], f.config.Now()); e != nil {
		t.Fatal(e)
	}
	newWallet := walletCreate(t, walletpg.New(f.db), newTreasury)
	rotatedConfig := f.cfg
	rotatedConfig.TreasuryAccountID, rotatedConfig.TreasuryWalletID = newTreasury, newWallet.ID
	rotated, e := postawardpg.New(f.db, f.guard, f.checkout, f.social, rotatedConfig)
	if e != nil {
		t.Fatal(e)
	}
	if e = rotated.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	f.checkout.WithPostAwards(rotated)
	current, e := rotated.CreateOrder(f.otherCtx, f.other, p.ID, "new-treasury", a.Request{Amount: walletAmount(t, "6"), Attitude: a.Positive})
	if e != nil {
		t.Fatal(e)
	}
	if paid, e := f.checkout.PayOrder(f.otherCtx, f.other, current.OrderID, &f.payer.ID); e != nil || paid.Status != c.Finished {
		t.Fatal(paid, e)
	}
	window := f.config.Now().UTC()
	count, e := rotated.ResettlePublisherRewards(f.ctx, f.actor, a.ResettleRequest{DateFrom: window.Add(-time.Hour), DateTo: window.Add(time.Hour)}, 100)
	if e != nil || count != 1 {
		t.Fatal("mixed treasury resettle", count, e)
	}
	var oldAttempts int
	var oldSettled, currentSettled bool
	if e = f.db.QueryRow(`SELECT r.attempts,r.settled_at IS NOT NULL FROM owned_post_award_receipts r JOIN owned_post_award_purchases p ON p.id=r.purchase_id WHERE p.order_id=$1::uuid`, old.OrderID).Scan(&oldAttempts, &oldSettled); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT r.settled_at IS NOT NULL FROM owned_post_award_receipts r JOIN owned_post_award_purchases p ON p.id=r.purchase_id WHERE p.order_id=$1::uuid`, current.OrderID).Scan(&currentSettled); e != nil {
		t.Fatal(e)
	}
	if oldAttempts != 7 || oldSettled || !currentSettled {
		t.Fatal("treasury rotation receipt isolation", oldAttempts, oldSettled, currentSettled)
	}
}
func TestPGPostAwardNegativeAndExplicitRounding(t *testing.T) {
	f := newPostAwardFixture(t, a.Documented80)
	p := f.post(t, f.individual, "Synthetic exact split award", posts.Public)
	f.pay(t, f.order(t, p, "positive", "1.239", 0))
	f.pay(t, f.order(t, p, "negative", "2.001", 2))
	f.pay(t, f.order(t, p, "tiny", "0.001", 0))
	pending, e := f.awards.Pending(f.ctx, "", p.ID)
	if e != nil || pending.Count != 1 || pending.PayoutAmount.String() != "0.991" {
		t.Fatal(pending, e)
	}
	read, e := f.posts.Get(f.ctx, "", p.ID)
	if e != nil || read.AwardedScore.String() != "-0.761" {
		t.Fatal(read, e)
	}
	if n, e := f.awards.SettleMerchant(f.ctx, f.actor, fixturePublisherHandle(f.individual), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "96.759", "0.000", "96.759")
	walletAssertBalance(t, walletpg.New(f.db), f.beneficiary.ID, "points", "0.991", "0.000", "0.991")
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "points", "2.250", "0.000", "2.250")
}
func TestPGPostAwardImmutableBeneficiaryAndMissingBinding(t *testing.T) {
	f := newPostAwardFixture(t, "")
	// Merchant routes resolve exact publisher handles. A UUID is not a handle,
	// and resolving the correct publisher grants no access to another wallet.
	if _, e := f.awards.Merchant(f.ctx, f.actor, f.individual.ID); !errors.Is(e, a.ErrNotFound) {
		t.Fatal("publisher UUID was accepted as a merchant handle", e)
	}
	if _, e := f.awards.BindWallet(f.ctx, f.actor, fixturePublisherHandle(f.individual), a.WalletPatch{WalletID: &f.payer.ID}); !errors.Is(e, a.ErrWallet) {
		t.Fatal("foreign beneficiary wallet accepted", e)
	}
	binding, e := f.awards.Merchant(f.ctx, f.actor, fixturePublisherHandle(f.individual))
	if e != nil || binding.PublisherID != f.individual.ID || binding.PaymentWalletID == nil || *binding.PaymentWalletID != f.beneficiary.ID {
		t.Fatal("rejected wallet changed explicit beneficiary binding", binding, e)
	}
	p := f.post(t, f.individual, "Synthetic captured beneficiary", posts.Public)
	paid := f.order(t, p, "paid", "5", 0)
	f.pay(t, paid)
	unpaid := f.order(t, p, "unpaid", "6", 0)
	next := walletCreate(t, walletpg.New(f.db), f.actor)
	if _, e := f.awards.BindWallet(f.ctx, f.actor, fixturePublisherHandle(f.individual), a.WalletPatch{WalletID: &next.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, unpaid.OrderID, &f.payer.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal("binding drift accepted", e)
	}
	if _, e := f.awards.BindWallet(f.ctx, f.actor, fixturePublisherHandle(f.individual), a.WalletPatch{}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.awards.CreateOrder(f.otherCtx, f.other, p.ID, "missing", a.Request{Amount: walletAmount(t, "1")}); !errors.Is(e, a.ErrWallet) {
		t.Fatal(e)
	}
	if n, e := f.awards.ProcessDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.beneficiary.ID, "points", "5.000", "0.000", "5.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "95.000", "0.000", "95.000")
	if _, e := f.awards.BindWallet(f.otherCtx, f.other, fixturePublisherHandle(f.individual), a.WalletPatch{WalletID: &f.payer.ID}); !errors.Is(e, a.ErrForbidden) {
		t.Fatal("foreign manager", e)
	}
}
func TestPGPostAwardPrivateRealmAndCurrentAuthority(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic private linked award", posts.Public)
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.private.ID}); e != nil {
		t.Fatal(e)
	}
	o := f.order(t, p, "member", "3", 0)
	f.pay(t, o)
	if _, _, e := f.awards.List(f.ctx, "", p.ID, a.Page{}); !errors.Is(e, a.ErrNotFound) {
		t.Fatal("anonymous realm leak", e)
	}
	if _, e := f.awards.Pending(f.outsideCtx, f.outside, p.ID); !errors.Is(e, a.ErrNotFound) {
		t.Fatal("foreign realm leak", e)
	}
	unpaid := f.order(t, p, "revoke", "2", 0)
	if _, e := f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, unpaid.OrderID, &f.payer.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	if _, _, e := f.awards.List(f.otherCtx, f.other, p.ID, a.Page{}); !errors.Is(e, a.ErrNotFound) {
		t.Fatal(e)
	}
	if n, e := f.awards.ProcessDue(context.Background(), 100); e != nil || n != 1 {
		t.Fatal("visibility canceled owed share", n, e)
	}
	public := f.post(t, f.individual, "Synthetic fresh award proof", posts.Public)
	denied := f.order(t, public, "denied", "1", 0)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}, {Pattern: "posts.award", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, denied.OrderID, &f.payer.ID); e == nil {
		t.Fatal("revoked scope paid")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "97.000", "0.000", "97.000")
}
func TestPGPostAwardAuditRollbackIsAtomic(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic rollback award", posts.Public)
	o := f.order(t, p, "rollback", "5", 0)
	query(t, f.db, `ALTER TABLE owned_post_award_audit ADD CONSTRAINT synthetic_post_award_rollback CHECK(action<>'fulfilled' OR order_id<>'`+o.OrderID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_post_award_audit DROP CONSTRAINT IF EXISTS synthetic_post_award_rollback`)
	})
	_, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.payer.ID)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "synthetic_post_award_rollback" {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_award_receipts WHERE purchase_id IN(SELECT id FROM owned_post_award_purchases WHERE order_id=$1::uuid)`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
	read, e := f.posts.Get(f.ctx, "", p.ID)
	if e != nil || read.AwardedScore.Sign() != 0 {
		t.Fatal(read, e)
	}
	query(t, f.db, `ALTER TABLE owned_post_award_audit DROP CONSTRAINT synthetic_post_award_rollback`)
	f.pay(t, o)
}
func TestPGPostAwardConcurrentReplayAndLockRetry(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic concurrent award", posts.Public)
	o := f.order(t, p, "one", "5", 0)
	ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancel()
	block, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback()
	if _, e = block.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1::uuid FOR UPDATE`, f.individual.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.checkout.PayOrder(ctx, f.other, o.OrderID, &f.payer.ID); !errors.Is(e, c.ErrRetry) {
		t.Fatal(e)
	}
	block.Rollback()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.checkout.PayOrder(ctx, f.other, o.OrderID, &f.payer.ID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_award_receipts WHERE purchase_id IN(SELECT id FROM owned_post_award_purchases WHERE order_id=$1::uuid)`, o.OrderID); n != 1 {
		t.Fatal(n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "95.000", "0.000", "95.000")
}
func TestPGPostAwardReleaseDisabledBeneficiaryAndRollback(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic payout retry", posts.Public)
	o := f.order(t, p, "held", "5", 0)
	f.pay(t, o)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
	if n, e := f.awards.ProcessDue(context.Background(), 100); e == nil || n != 0 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "points", "0.000", "5.000", "0.000")
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.actor)
	query(t, f.db, `ALTER TABLE owned_post_award_audit ADD CONSTRAINT synthetic_post_award_release CHECK(action<>'settled' OR order_id<>'`+o.OrderID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_post_award_audit DROP CONSTRAINT IF EXISTS synthetic_post_award_release`)
	})
	_, e := f.awards.SettleMerchant(f.ctx, f.actor, fixturePublisherHandle(f.individual), 100)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "synthetic_post_award_release" {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "points", "0.000", "5.000", "0.000")
	query(t, f.db, `ALTER TABLE owned_post_award_audit DROP CONSTRAINT synthetic_post_award_release`)
	if n, e := f.awards.SettleMerchant(f.ctx, f.actor, fixturePublisherHandle(f.individual), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestPGPostAwardExpiryAndPolicySnapshot(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic captured award policy", posts.Public)
	expired := f.order(t, p, "expired", "5", 0)
	past := f.config.Now().Add(-time.Second)
	query(t, f.db, `UPDATE commerce_v1.orders SET expires_at=$2::timestamptz,document=jsonb_set(document,'{expired_at}',to_jsonb($2::timestamptz)) WHERE id=$1::uuid`, expired.OrderID, past)
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, expired.OrderID, &f.payer.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	o := f.order(t, p, "policy", "5.001", 0)
	cfg := f.cfg
	cfg.Policy = a.Documented80
	next, e := postawardpg.New(f.db, f.guard, f.checkout, f.social, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f.checkout.WithPostAwards(next)
	f.pay(t, o)
	pending, e := next.Pending(f.ctx, "", p.ID)
	if e != nil || pending.PayoutAmount.String() != "5.001" {
		t.Fatal(pending, e)
	}
}
func TestPGPostAwardRatingSignedTermAndOldReceipt(t *testing.T) {
	f := newPostAwardFixture(t, "")
	query(t, f.db, publisherratingpg.Schema)
	p := f.post(t, f.individual, "Synthetic award rating input", posts.Public)
	day := f.config.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	query(t, f.db, `UPDATE owned_text_posts SET created_at=$2::timestamptz WHERE id=$1::uuid`, p.ID, day.Add(12*time.Hour))
	if _, _, e := f.interactions.React(f.otherCtx, f.other, p.ID, interactions.ReactionRequest{Symbol: "thumb_up", Attitude: 0}); e != nil {
		t.Fatal(e)
	}
	f.pay(t, f.order(t, p, "negative", "5", 2))
	rating, e := publisherratingpg.New(f.db, f.social, publisherratingpg.Config{PaidAwards: true, EarliestDay: day, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := rating.ProcessPublisherDay(f.ctx, f.individual.ID, day); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	v, e := rating.Overview(f.ctx, "", f.individual.ID)
	if e != nil || v.Rating != 100 || !v.AwardScoreIncluded || v.Policy != "reaction_and_paid_awards_v1" {
		t.Fatal("must truncate combined 1-0.5 to zero", v, e)
	}
	legacy, e := publisherratingpg.New(f.db, f.social, publisherratingpg.Config{ReactionOnly: true, EarliestDay: day, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if n, e := legacy.ProcessPublisherDay(f.ctx, f.individual.ID, day); e != nil || n != 0 {
		t.Fatal("old day rewritten", n, e)
	}
	query(t, f.db, publisherratingpg.Schema)
}
func TestPGPostAwardOppositePublisherPayments(t *testing.T) {
	f := newPostAwardFixture(t, "")
	name := "synthetic-award-other-" + f.other
	pub, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.awards.BindWallet(f.otherCtx, f.other, fixturePublisherHandle(pub), a.WalletPatch{WalletID: &f.payer.ID}); e != nil {
		t.Fatal(e)
	}
	visibility := posts.Public
	at := f.config.Now()
	otherPost, e := f.posts.Create(f.otherCtx, f.other, pub.ID, posts.Patch{Content: postString("Synthetic opposite award"), Visibility: &visibility, PublishedAt: &at})
	if e != nil {
		t.Fatal(e)
	}
	own := f.post(t, f.individual, "Synthetic owner award", posts.Public)
	query(t, f.db, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,amount,created_at,updated_at)VALUES($1::uuid,$2::uuid,'points',100,$3::timestamptz,$3::timestamptz)`, authorityUUID(t), f.beneficiary.ID, f.config.Now())
	one := f.order(t, own, "opposite-b", "5", 0)
	two, e := f.awards.CreateOrder(f.ctx, f.actor, otherPost.ID, "opposite-a", a.Request{Amount: walletAmount(t, "5")})
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	otherCtx, cancelOther := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancelOther()
	go func() { <-start; _, e := f.checkout.PayOrder(ctx, f.actor, two.OrderID, &f.beneficiary.ID); errs <- e }()
	go func() { <-start; _, e := f.checkout.PayOrder(otherCtx, f.other, one.OrderID, &f.payer.ID); errs <- e }()
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil && !errors.Is(e, c.ErrRetry) {
			t.Fatal(e)
		}
	}
	f.pay(t, one)
	if _, e = f.checkout.PayOrder(ctx, f.actor, two.OrderID, &f.beneficiary.ID); e != nil {
		t.Fatal(e)
	}
	if n, e := f.awards.ProcessDue(context.Background(), 100); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
func TestPGPostAwardRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	const marker = "Synthetic durable post award reserve"
	if phase == "after" {
		db := database(t)
		var n int
		e := db.QueryRow(`SELECT count(*) FROM owned_post_award_purchases p JOIN owned_text_posts post ON post.id=p.post_id JOIN owned_post_award_receipts r ON r.purchase_id=p.id JOIN wallet_v1.transactions source ON source.id=r.transaction_id JOIN wallet_v1.transactions reserve ON reserve.id=r.reserve_transaction_id WHERE post.content=$1::text AND p.policy='merchant_full_v1' AND p.fulfilled_at IS NOT NULL AND p.share_amount=p.amount AND source.status=2 AND reserve.status=1 AND reserve.amount=p.amount AND r.settled_at IS NULL AND post.awarded_score=p.amount`, marker).Scan(&n)
		if e != nil || n != 1 {
			t.Fatal(n, e)
		}
		return
	}
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, marker, posts.Public)
	f.pay(t, f.order(t, p, "restart", "3.333", 0))
}

func TestPGPostAwardOrganizationWalletAndRevocation(t *testing.T) {
	f := newPostAwardFixture(t, "")
	realmWallet, e := walletpg.New(f.db).CreateWallet(context.Background(), wallet.Owner{RealmID: f.private.ID}, "Synthetic realm award wallet")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.awards.BindWallet(f.ctx, f.actor, fixturePublisherHandle(f.privatePub), a.WalletPatch{WalletID: &realmWallet.ID}); e != nil {
		t.Fatal(e)
	}
	p := f.post(t, f.privatePub, "Synthetic private organization award", posts.Public)
	o := f.order(t, p, "organization", "4", 0)
	f.pay(t, o)
	if _, _, e = f.awards.List(f.ctx, "", p.ID, a.Page{}); !errors.Is(e, a.ErrNotFound) {
		t.Fatal("organization award leak", e)
	}
	if n, e := f.awards.SettleMerchant(f.ctx, f.actor, fixturePublisherHandle(f.privatePub), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), realmWallet.ID, "points", "4.000", "0.000", "4.000")
	queued := f.order(t, p, "revoked-organization", "2", 0)
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.checkout.PayOrder(f.otherCtx, f.other, queued.OrderID, &f.payer.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestPGPostAwardAuthorityAfterAccountWait(t *testing.T) {
	f := newPostAwardFixture(t, "")
	p := f.post(t, f.individual, "Synthetic queued award authority", posts.Public)
	o := f.order(t, p, "wait", "5", 0)
	ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancel()
	block, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback()
	var pid int
	if e = block.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = block.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, f.other); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := f.checkout.PayOrder(ctx, f.other, o.OrderID, &f.payer.ID); result <- e }()
	moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
	if _, e = block.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.other); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; e == nil {
		t.Fatal("queued stale authority paid")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payer.ID, "points", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
}
