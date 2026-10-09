// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/internal/walletpg"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync"
	"testing"
	"time"
)

type sponsorFixture struct {
	*moderationFixture
	sponsor                     *sponsorshippg.Repository
	checkout                    *commercepg.Repository
	payer, otherPayer, treasury wallet.Wallet
}

func newSponsorFixture(t *testing.T, retain bool) *sponsorFixture {
	t.Helper()
	f := &sponsorFixture{moderationFixture: newModerationFixture(t)}
	query(t, f.db, sponsorshippg.Schema)
	if !retain {
		t.Cleanup(func() {
			for _, table := range []string{"owned_post_sponsor_impressions", "owned_post_sponsor_placements", "owned_post_sponsor_audit"} {
				_, _ = f.db.Exec(`DELETE FROM `+table+` WHERE post_id IN(SELECT post_id FROM owned_post_sponsorships WHERE account_id IN($1::uuid,$2::uuid))`, f.actor, f.other)
			}
			_, _ = f.db.Exec(`DELETE FROM owned_post_sponsorships WHERE account_id IN($1::uuid,$2::uuid)`, f.actor, f.other)
		})
	}
	f.payer = walletCreate(t, walletpg.New(f.db), f.actor)
	f.otherPayer = walletCreate(t, walletpg.New(f.db), f.other)
	f.treasury = walletCreate(t, walletpg.New(f.db), f.outside)
	for _, w := range []wallet.Wallet{f.payer, f.otherPayer} {
		query(t, f.db, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,amount,created_at,updated_at)VALUES($1::uuid,$2::uuid,'golds',100,$3::timestamptz,$3::timestamptz)`, authorityUUID(t), w.ID, f.config.Now())
	}
	f.checkout = commercepg.New(f.db, f.guard, commercepg.Adapters{}).WithClock(f.config.Now)
	var e error
	f.sponsor, e = sponsorshippg.New(f.db, f.guard, f.checkout, f.social, f.timeline, sponsorshippg.Config{TreasuryAccountID: f.outside, TreasuryWalletID: f.treasury.ID, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	f.checkout.WithSponsorship(f.sponsor)
	if e = f.sponsor.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *sponsorFixture) order(t *testing.T, post posts.Post, actor string, ctx context.Context, key, amount string) s.OrderResponse {
	t.Helper()
	o, e := f.sponsor.CreateOrder(ctx, actor, post.ID, key, s.Request{Amount: walletAmount(t, amount)})
	if e != nil {
		t.Fatal(e)
	}
	return o
}
func (f *sponsorFixture) pay(t *testing.T, o s.OrderResponse, actor string, ctx context.Context, w wallet.Wallet) c.Order {
	t.Helper()
	v, e := f.checkout.PayOrder(ctx, actor, o.OrderID, &w.ID)
	if e != nil || v.Status != c.Finished || v.TransactionID == nil {
		t.Fatal(v, e)
	}
	return v
}
func TestPGPublisherSponsorshipPaymentExactlyOnceAndIsolation(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic paid sponsorship", posts.Public)
	o := f.order(t, p, f.other, f.otherCtx, "one", "5.125")
	if total, e := f.sponsor.Total(f.ctx, "", p.ID); e != nil || total.Sign() != 0 {
		t.Fatal("unpaid bid promoted", total, e)
	}
	again := f.order(t, p, f.other, f.otherCtx, "one", "5.125")
	if again.OrderID != o.OrderID {
		t.Fatal("order replay duplicated")
	}
	if _, e := f.sponsor.CreateOrder(f.otherCtx, f.other, p.ID, "one", s.Request{Amount: walletAmount(t, "6")}); !errors.Is(e, c.ErrIdempotency) {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.ctx, f.actor, o.OrderID, &f.payer.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("foreign payer", e)
	}
	draft, e := f.checkout.GetOrder(f.otherCtx, f.other, o.OrderID, false)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(f.otherCtx, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = f.sponsor.FulfillSponsor(f.otherCtx, f.db, tx, f.other, draft, f.config.Now())
	_ = tx.Rollback()
	if !errors.Is(e, s.ErrForbidden) {
		t.Fatal("unpaid direct fulfillment accepted", e)
	}
	paid := f.pay(t, o, f.other, f.otherCtx, f.otherPayer)
	againPaid := f.pay(t, o, f.other, f.otherCtx, f.otherPayer)
	if *againPaid.TransactionID != *paid.TransactionID {
		t.Fatal("payment replay changed transaction")
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "94.875", "0.000", "94.875")
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "5.125", "0.000", "5.125")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid AND status=2`, o.OrderID); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_sponsorships WHERE order_id=$1::uuid AND fulfilled_at IS NOT NULL`, o.OrderID); n != 1 {
		t.Fatal(n)
	}
	for _, who := range []struct {
		actor string
		ctx   context.Context
		want  int
	}{{f.other, f.otherCtx, 1}, {f.actor, f.ctx, 1}, {f.outside, f.outsideCtx, 0}} {
		rows, total, e := f.sponsor.History(who.ctx, who.actor, p.ID, s.Page{})
		if e != nil || total != who.want || len(rows) != who.want {
			t.Fatal(who.actor, rows, total, e)
		}
	}
	total, e := f.sponsor.Total(f.ctx, "", p.ID)
	if e != nil || total.String() != "5.125" {
		t.Fatal(total, e)
	}
}
func TestPGPublisherSponsorshipAuthorityAndPrivateRevalidation(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic visibility recheck", posts.Public)
	o := f.order(t, p, f.other, f.otherCtx, "private", "5")
	private := posts.Private
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.otherPayer.ID); !errors.Is(e, c.ErrNotFound) {
		t.Fatal("private target funded", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "100.000", "0.000", "100.000")
	public := posts.Public
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Visibility: &public}); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}, {Pattern: "posts.sponsor", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.otherPayer.ID); e == nil {
		t.Fatal("revoked sponsor permission paid")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.otherPayer.ID); e == nil {
		t.Fatal("disabled owner funded")
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.actor)
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.private.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.sponsor.CreateOrder(f.ctx, f.actor, p.ID, "realm", s.Request{Amount: walletAmount(t, "5")}); !errors.Is(e, s.ErrNotFound) {
		t.Fatal("realm-linked public promotion", e)
	}
	if _, _, e := f.sponsor.Ads(f.outsideCtx, f.outside, f.privatePub.ID, false, s.Page{}); !errors.Is(e, s.ErrNotFound) {
		t.Fatal("global flag bypassed private publisher", e)
	}
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.publicPub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Viewer}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.accounts.ChangeMember(f.otherCtx, f.other, f.publicPub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.public.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.sponsor.Ads(f.otherCtx, f.other, f.publicPub.ID, true, s.Page{}); !errors.Is(e, s.ErrForbidden) {
		t.Fatal("publisher role bypassed realm membership", e)
	}
}
func TestPGPublisherSponsorshipAuditRollbackConservation(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic sponsor rollback", posts.Public)
	o := f.order(t, p, f.other, f.otherCtx, "rollback", "7.777")
	name := "synthetic_sponsor_audit_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_post_sponsor_audit ADD CONSTRAINT `+name+` CHECK(order_id IS DISTINCT FROM '`+o.OrderID+`'::uuid OR action<>'fulfilled') NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_post_sponsor_audit DROP CONSTRAINT IF EXISTS ` + name) })
	_, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.otherPayer.ID)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != name {
		t.Fatal("exact audit rollback constraint", e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "0.000", "0.000", "0.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_sponsorships WHERE order_id=$1::uuid AND fulfilled_at IS NOT NULL`, o.OrderID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `ALTER TABLE owned_post_sponsor_audit DROP CONSTRAINT `+name)
	f.pay(t, o, f.other, f.otherCtx, f.otherPayer)
}
func TestPGPublisherSponsorshipConcurrencyAndSecondaryLockRetry(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic sponsor concurrent", posts.Public)
	o := f.order(t, p, f.other, f.otherCtx, "concurrent", "5")
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`SELECT id FROM owned_publishers WHERE id=$1::uuid FOR UPDATE`, f.individual.ID); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.otherCtx, 2*time.Second)
	defer cancel()
	if _, e = f.checkout.PayOrder(ctx, f.other, o.OrderID, &f.otherPayer.ID); !errors.Is(e, c.ErrRetry) {
		t.Fatal("secondary lock did not request retry", e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "100.000", "0.000", "100.000")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.checkout.PayOrder(f.otherCtx, f.other, o.OrderID, &f.otherPayer.ID)
			if e == nil && v.Status != c.Finished {
				e = c.ErrConflict
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1::uuid`, o.OrderID); n != 1 {
		t.Fatal(n)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "5.000", "0.000", "5.000")
}
func TestPGPublisherSponsorshipOppositeOwnerPayments(t *testing.T) {
	f := newSponsorFixture(t, false)
	name := "synthetic-sponsored-other-" + f.other
	otherPub, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	visibility := posts.Public
	otherPost, e := f.posts.Create(f.otherCtx, f.other, otherPub.ID, posts.Patch{Content: postString("Synthetic other owner post"), Visibility: &visibility})
	if e != nil {
		t.Fatal(e)
	}
	ownPost := f.post(t, f.individual, "Synthetic first owner post", posts.Public)
	a := f.order(t, otherPost, f.actor, f.ctx, "opposite-a", "5")
	b := f.order(t, ownPost, f.other, f.otherCtx, "opposite-b", "5")
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() { <-start; _, e := f.checkout.PayOrder(f.ctx, f.actor, a.OrderID, &f.payer.ID); errs <- e }()
	go func() {
		<-start
		_, e := f.checkout.PayOrder(f.otherCtx, f.other, b.OrderID, &f.otherPayer.ID)
		errs <- e
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil && !errors.Is(e, c.ErrRetry) {
			t.Fatal(e)
		}
	}
	f.pay(t, a, f.actor, f.ctx, f.payer)
	f.pay(t, b, f.other, f.otherCtx, f.otherPayer)
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "10.000", "0.000", "10.000")
}
func TestPGPublisherSponsorshipOrderAndBidExpiry(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic sponsor expiry", posts.Public)
	unpaid := f.order(t, p, f.other, f.otherCtx, "expiry", "5")
	past := f.config.Now().Add(-time.Second)
	query(t, f.db, `UPDATE commerce_v1.orders SET expires_at=$2::timestamptz,document=jsonb_set(document,'{expired_at}',to_jsonb($2::timestamptz)) WHERE id=$1::uuid`, unpaid.OrderID, past)
	if _, e := f.checkout.PayOrder(f.otherCtx, f.other, unpaid.OrderID, &f.otherPayer.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.otherPayer.ID, "golds", "100.000", "0.000", "100.000")
	paid := f.order(t, p, f.other, f.otherCtx, "paid-expiry", "5")
	f.pay(t, paid, f.other, f.otherCtx, f.otherPayer)
	fulfilled := f.config.Now().Add(-25 * time.Hour)
	query(t, f.db, `UPDATE owned_post_sponsorships SET fulfilled_at=$2::timestamptz,expires_at=$3::timestamptz WHERE order_id=$1::uuid`, paid.OrderID, fulfilled, fulfilled.Add(24*time.Hour))
	total, e := f.sponsor.Total(f.ctx, "", p.ID)
	if e != nil || total.Sign() != 0 {
		t.Fatal(total, e)
	}
	history, n, e := f.sponsor.History(f.otherCtx, f.other, p.ID, s.Page{})
	if e != nil || n != 1 || len(history) != 1 {
		t.Fatal(history, n, e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.treasury.ID, "golds", "5.000", "0.000", "5.000")
}
func TestPGPublisherSponsorshipAuctionCurrentVisibilityAndAds(t *testing.T) {
	f := newSponsorFixture(t, false)
	p := f.post(t, f.individual, "Synthetic auction winner", posts.Public)
	o := f.order(t, p, f.actor, f.ctx, "auction", "5.001")
	f.pay(t, o, f.actor, f.ctx, f.payer)
	worker, e := sponsorshippg.NewAuctionWorker(f.db, f.social, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	if e = worker.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	created, e := worker.Run(context.Background())
	if e != nil || !created {
		t.Fatal(created, e)
	}
	if created, e = worker.Run(context.Background()); e != nil || created {
		t.Fatal("duplicate hourly placement", created, e)
	}
	v, e := f.sponsor.Current(f.ctx, "")
	if e != nil || !v.Sponsored || v.Post == nil || v.Post.ID != p.ID || !v.Post.Sponsored {
		t.Fatal(v, e)
	}
	if e = f.sponsor.RecordDisplay(f.ctx, "", p.ID); e != nil {
		t.Fatal(e)
	}
	ads, n, e := f.sponsor.Ads(f.ctx, "", f.individual.ID, false, s.Page{})
	if e != nil || n != 1 || len(ads) != 1 || ads[0].ActiveBidTotal.String() != "5.001" || ads[0].ShownCount != 1 || ads[0].DisplayChance == nil || *ads[0].DisplayChance != 1 {
		t.Fatal(ads, n, e)
	}
	reason := "spam"
	if _, e = f.admin.Mutate(f.outsideCtx, f.outside, admin.PostKind, p.ID, admin.Command{Action: "shadowban", Reason: &reason}); e != nil {
		t.Fatal(e)
	}
	v, e = f.sponsor.Current(f.ctx, f.actor)
	if e != nil || v.Sponsored {
		t.Fatal("owner's moderated post promoted", v, e)
	}
	ads, n, e = f.sponsor.Ads(f.ctx, f.actor, f.individual.ID, false, s.Page{})
	if e != nil || n != 0 || len(ads) != 0 {
		t.Fatal("suppressed ad totals leaked", ads, n, e)
	}
}
func TestPGPublisherSponsorshipRestartPersistence(t *testing.T) {
	const marker = "Synthetic durable paid sponsorship"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var n int
		e := db.QueryRow(`SELECT count(*) FROM owned_post_sponsorships s JOIN owned_text_posts p ON p.id=s.post_id JOIN commerce_v1.orders o ON o.id=s.order_id JOIN wallet_v1.transactions t ON t.id=s.transaction_id WHERE p.content=$1::text AND s.fulfilled_at IS NOT NULL AND s.expires_at=s.fulfilled_at+interval '24 hours' AND o.status=3 AND o.transaction_id=t.id AND t.status=2 AND t.managed_by=o.id AND t.amount=s.amount AND t.currency='golds'`, marker).Scan(&n)
		if e != nil || n != 1 {
			t.Fatal(n, e)
		}
		return
	}
	f := newSponsorFixture(t, true)
	p := f.post(t, f.individual, marker, posts.Public)
	o := f.order(t, p, f.other, f.otherCtx, "restart", "5.333")
	f.pay(t, o, f.other, f.otherCtx, f.otherPayer)
}
