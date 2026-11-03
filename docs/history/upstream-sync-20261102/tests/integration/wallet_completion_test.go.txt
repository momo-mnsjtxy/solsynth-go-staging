// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/merchantpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/merchants"
)

func merchantPool(t *testing.T, f commerceFixture) (*merchantpg.Repository, context.Context) {
	t.Helper()
	db := database(t)
	verifier, err := authority.NewVerifier(authoritypg.New(db), f.config)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := verifier.Authenticate(f.ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return merchantpg.New(db, guard).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() }), guard.Bind(base, proof)
}

func enablePayoutScope(t *testing.T, f commerceFixture) {
	t.Helper()
	query(t, f.db, `UPDATE owned_custom_apps SET oauth_config=jsonb_set(oauth_config,'{allowed_scopes}',oauth_config->'allowed_scopes'||'["orders.payouts.manage"]'::jsonb) WHERE id=$1::uuid`, f.app)
	query(t, f.db, `UPDATE owned_app_secrets SET scopes=scopes||'["orders.payouts.manage"]'::jsonb WHERE id=$1::uuid`, f.secretID)
}

func accountWithoutWallet(t *testing.T, f commerceFixture) string {
	t.Helper()
	account := authorityUUID(t)
	if err := authoritypg.SeedSynthetic(f.ctx, f.db, account, "synthetic_wallet_recipient_"+strings.ReplaceAll(account, "-", ""), time.Unix(f.clock.Load(), 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return account
}

func lockAccountForTrustedWallet(t *testing.T, ctx context.Context, tx *sql.Tx, account string) {
	t.Helper()
	var locked string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, account).Scan(&locked); err != nil || locked != account {
		t.Fatal("lock trusted wallet account", locked, err)
	}
}

type redeemWaitFixture struct {
	giver, recipient commerceFixture
	gift             c.Gift
	order            c.Order
}

func TestPGTrustedEnsurePrimaryWalletConcurrencyQuotaDefaultAndRollback(t *testing.T) {
	t.Run("concurrent first funded grants", func(t *testing.T) {
		f := newCommerceFixture(t)
		recipient := accountWithoutWallet(t, f)
		walletFund(t, f.db, f.payee, "synthetic-commerce", "10.000")
		grantAmount := walletAmount(t, "1.000")
		type result struct {
			wallet string
			err    error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		for _, key := range []string{"ensure-first-a", "ensure-first-b"} {
			key := key
			go func() {
				<-start
				tx, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
				if err != nil {
					results <- result{err: err}
					return
				}
				defer tx.Rollback()
				var locked string
				if err = tx.QueryRowContext(f.ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, recipient).Scan(&locked); err != nil {
					results <- result{err: err}
					return
				}
				primary, err := walletpg.EnsurePrimaryWalletInTransaction(f.ctx, tx, recipient, time.Unix(f.clock.Load(), 0).UTC())
				if err == nil {
					_, err = walletpg.FundedTransfer(f.ctx, tx, f.actor, f.payee.ID, primary.ID, "synthetic-commerce", grantAmount, nil, key, time.Unix(f.clock.Load(), 0).UTC())
				}
				if err == nil {
					err = tx.Commit()
				}
				results <- result{wallet: primary.ID, err: err}
			}()
		}
		close(start)
		var walletID string
		deadline := time.After(10 * time.Second)
		for range 2 {
			select {
			case got := <-results:
				if got.err != nil {
					t.Fatal("concurrent primary ensure", got.err)
				}
				if walletID == "" {
					walletID = got.wallet
				} else if got.wallet != walletID {
					t.Fatal("concurrent grants selected different primary wallets", walletID, got.wallet)
				}
			case <-deadline:
				t.Fatal("concurrent primary ensure timed out")
			}
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL AND is_primary`, recipient); n != 1 {
			t.Fatal("concurrent primary wallet count", n)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.outbox WHERE event_type='wallet.created' AND aggregate_id=$1::uuid`, walletID); n != 1 {
			t.Fatal("concurrent primary wallet creation events", n)
		}
		walletAssertBalance(t, walletpg.New(f.db), walletID, "synthetic-commerce", "2.000", "0.000", "2.000")
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "8.000", "0.000", "8.000")
	})

	t.Run("existing default at quota", func(t *testing.T) {
		f := newCommerceFixture(t)
		recipient := accountWithoutWallet(t, f)
		wallets := make([]wallet.Wallet, 0, 3)
		for i := 0; i < 3; i++ {
			created, err := walletpg.New(f.db).CreateWallet(f.ctx, wallet.Owner{AccountID: recipient}, "Quota wallet")
			if err != nil {
				t.Fatal(err)
			}
			wallets = append(wallets, created)
		}
		selected, err := walletpg.New(f.db).SetDefault(f.ctx, wallets[2].ID, recipient)
		if err != nil || !selected.IsPrimary {
			t.Fatal("set quota default", selected, err)
		}
		walletFund(t, f.db, f.payee, "synthetic-commerce", "2.000")
		tx, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		lockAccountForTrustedWallet(t, f.ctx, tx, recipient)
		primary, err := walletpg.EnsurePrimaryWalletInTransaction(f.ctx, tx, recipient, time.Unix(f.clock.Load(), 0).UTC())
		if err != nil || primary.ID != selected.ID {
			t.Fatal("trusted ensure ignored existing default at quota", primary, err)
		}
		if _, err = walletpg.FundedTransfer(f.ctx, tx, f.actor, f.payee.ID, primary.ID, "synthetic-commerce", walletAmount(t, "1.000"), nil, "ensure-at-quota", time.Unix(f.clock.Load(), 0).UTC()); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL`, recipient); n != 3 {
			t.Fatal("trusted ensure exceeded wallet quota", n)
		}
		walletAssertBalance(t, walletpg.New(f.db), selected.ID, "synthetic-commerce", "1.000", "0.000", "1.000")
	})

	t.Run("failed funding rolls wallet back", func(t *testing.T) {
		f := newCommerceFixture(t)
		recipient := accountWithoutWallet(t, f)
		tx, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		lockAccountForTrustedWallet(t, f.ctx, tx, recipient)
		primary, err := walletpg.EnsurePrimaryWalletInTransaction(f.ctx, tx, recipient, time.Unix(f.clock.Load(), 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		_, err = walletpg.FundedTransfer(f.ctx, tx, f.actor, f.payee.ID, primary.ID, "synthetic-commerce", walletAmount(t, "1.000"), nil, "ensure-rollback", time.Unix(f.clock.Load(), 0).UTC())
		if !errors.Is(err, wallet.ErrInsufficient) {
			t.Fatal("empty treasury funded first wallet", err)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.wallets WHERE account_id=$1::uuid`, recipient); n != 0 {
			t.Fatal("failed funded grant retained wallet", n)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.outbox WHERE event_type='wallet.created' AND aggregate_id=$1::uuid`, primary.ID); n != 0 {
			t.Fatal("failed funded grant retained wallet event", n)
		}
	})
}

func TestPGTrustedExistingPrimaryOpposingFundingUsesOneWalletLockOrder(t *testing.T) {
	f := newCommerceFixture(t)
	accounts := []string{accountWithoutWallet(t, f), accountWithoutWallet(t, f)}
	wallets := []wallet.Wallet{
		walletCreate(t, walletpg.New(f.db), accounts[0]),
		walletCreate(t, walletpg.New(f.db), accounts[1]),
	}
	for _, current := range wallets {
		walletFund(t, f.db, current, "synthetic-commerce", "10.000")
	}
	type result struct {
		payee string
		err   error
	}
	ready := make(chan struct{}, 2)
	fund := make(chan struct{})
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	for i := range 2 {
		i := i
		go func() {
			tx, err := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
			if err != nil {
				results <- result{err: err}
				return
			}
			defer tx.Rollback()
			var locked string
			if err = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, accounts[1-i]).Scan(&locked); err != nil {
				results <- result{err: err}
				return
			}
			primary, err := walletpg.EnsurePrimaryWalletInTransaction(ctx, tx, accounts[1-i], time.Unix(f.clock.Load(), 0).UTC())
			if err != nil {
				results <- result{err: err}
				return
			}
			ready <- struct{}{}
			<-fund
			_, err = walletpg.FundedTransfer(ctx, tx, f.actor, wallets[i].ID, primary.ID, "synthetic-commerce", walletAmount(t, "1.000"), nil, fmt.Sprintf("opposing-existing-primary-%d", i), time.Unix(f.clock.Load(), 0).UTC())
			if err == nil {
				err = tx.Commit()
			}
			results <- result{payee: primary.ID, err: err}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("opposing funding did not reach wallet lock barrier", ctx.Err())
		}
	}
	close(fund)
	seen := map[string]bool{}
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatal("opposing funded transfer", got.err)
			}
			seen[got.payee] = true
		case <-ctx.Done():
			t.Fatal("opposing funded transfer timed out", ctx.Err())
		}
	}
	if len(seen) != 2 {
		t.Fatal("opposing transfers did not preserve both primary wallets", seen)
	}
	for _, current := range wallets {
		walletAssertBalance(t, walletpg.New(f.db), current.ID, "synthetic-commerce", "10.000", "0.000", "10.000")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE actor_id=$1::uuid AND idempotency_key LIKE 'opposing-existing-primary-%'`, f.actor); n != 2 {
		t.Fatal("opposing funded transaction count", n)
	}
}

func newRedeemWaitFixture(t *testing.T) redeemWaitFixture {
	t.Helper()
	giver, recipient := newCommerceFixture(t), newCommerceFixture(t)
	ref := "developer.app:" + giver.app
	raw, _ := json.Marshal(ref)
	query(t, giver.db, `UPDATE commerce_v1.definitions SET document=jsonb_set(document,'{app_identifier}',$2::jsonb) WHERE identifier=$1`, giver.plan, raw)
	gift, err := giver.r.PurchaseGift(giver.ctx, giver.actor, c.PurchaseGift{SubscriptionIdentifier: giver.plan, PaymentMethod: "solian.wallet", RecipientID: &recipient.actor}, "wait-redeem")
	if err != nil {
		t.Fatal(err)
	}
	order, err := giver.r.GiftOrder(giver.ctx, giver.actor, gift.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = giver.r.PayOrder(giver.ctx, giver.actor, order.ID, &giver.wallet.ID); err != nil {
		t.Fatal(err)
	}
	return redeemWaitFixture{giver: giver, recipient: recipient, gift: gift, order: order}
}

func TestPGMerchantPendingRefundAndConcurrentSettlementConserveFunds(t *testing.T) {
	t.Run("pending refund replay", func(t *testing.T) {
		f := newCommerceFixture(t)
		r := merchantRepo(t, f)
		o := commerceOrder(t, f, "pending-refund", false)
		paid, err := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
		if err != nil || paid.TransactionID == nil {
			t.Fatal(paid, err)
		}
		if _, err = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
			t.Fatal(err)
		}
		refunded, err := r.RefundOrder(f.ctx, f.actor, o.ID, "pending-refund")
		if err != nil || refunded.ID != *paid.TransactionID || refunded.Status != wallet.Refunded {
			t.Fatal(refunded, err)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.order.cancelled'`, o.ID); n != 1 {
			t.Fatal("pending refund cancellation event", n)
		}
		replay, err := r.RefundOrder(f.ctx, f.actor, o.ID, "pending-refund")
		if err != nil || replay.ID != refunded.ID {
			t.Fatal("refund replay", replay, err)
		}
		replay, err = r.RefundOrder(f.ctx, f.actor, o.ID, "pending-refund-other-key")
		if err != nil || replay.ID != refunded.ID {
			t.Fatal("refund status replay", replay, err)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.order.cancelled'`, o.ID); n != 1 {
			t.Fatal("pending refund replay duplicated cancellation event", n)
		}
		if settled, settleErr := r.Settle(f.ctx, f.actor, f.publisher); settleErr != nil || len(settled) != 0 {
			t.Fatal("refunded pending order settled", settled, settleErr)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=2`, o.ID); n != 1 {
			t.Fatal("pending refund settlement state", n)
		}
	})

	t.Run("settle refund race", func(t *testing.T) {
		f := newCommerceFixture(t)
		r := merchantRepo(t, f)
		r2, ctx2 := merchantPool(t, f)
		o := commerceOrder(t, f, "settle-refund-race", false)
		if _, err := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := r.Settle(f.ctx, f.actor, f.publisher)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := r2.RefundOrder(ctx2, f.actor, o.ID, "race-refund")
			errs <- err
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal("settle/refund race", err)
			}
		}
		walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=2`, o.ID); n != 1 {
			t.Fatal("raced settlement state", n)
		}
	})
}

func TestPGMerchantBatchSettlementLocksMultipleWallets(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	first := commerceOrder(t, f, "batch-first", false)
	if _, err := f.r.PayOrder(f.ctx, f.actor, first.ID, &f.wallet.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.UpdateOrder(f.ctx, first.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
		t.Fatal(err)
	}
	secondPayee := walletCreate(t, walletpg.New(f.db), f.actor)
	if _, err := r.BindWallet(f.ctx, f.actor, f.publisher, f.app, merchants.WalletPatch{WalletID: &secondPayee.ID}); err != nil {
		t.Fatal(err)
	}
	second := commerceOrder(t, f, "batch-second", false)
	if _, err := f.r.PayOrder(f.ctx, f.actor, second.ID, &f.wallet.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.UpdateOrder(f.ctx, second.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
		t.Fatal(err)
	}
	settled, err := r.Settle(f.ctx, f.actor, f.publisher)
	if err != nil || len(settled) != 2 {
		t.Fatal(settled, err)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "79.998", "0.000", "79.998")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "10.001", "0.000", "10.001")
	walletAssertBalance(t, walletpg.New(f.db), secondPayee.ID, "synthetic-commerce", "10.001", "0.000", "10.001")
}

func secondMerchantApp(t *testing.T, f commerceFixture, payoutWallet string) (string, c.Credentials) {
	t.Helper()
	name := "reverse-" + strings.ReplaceAll(authorityUUID(t), "-", "")
	pub, err := accountpg.NewWithAuthority(f.db, f.guard).CreatePublisher(f.ctx, f.actor, profiles.PublisherPatch{Name: &name, Nick: &name})
	if err != nil {
		t.Fatal(err)
	}
	dev, project, app, secret := authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t)
	now := time.Unix(f.clock.Load(), 0).UTC()
	query(t, f.db, `INSERT INTO owned_developers(id,publisher_id,created_at) VALUES($1,$2,$3)`, dev, pub.ID, now)
	query(t, f.db, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,created_at,updated_at) VALUES($1,$2,$3,'Reverse settlement', $4,$4)`, project, dev, name, now)
	scopes := []string{"orders.create", "orders.status.manage", "orders.metrics.read"}
	config, _ := json.Marshal(apps.OAuthConfig{AllowedScopes: scopes, RedirectURIs: []string{}, AllowedGrantTypes: []string{}})
	query(t, f.db, `INSERT INTO owned_custom_apps(id,project_id,slug,name,status,oauth_config,created_at,updated_at) VALUES($1,$2,$3,'Reverse settlement',1,$4,$5,$5)`, app, project, name, config, now)
	token := "oa1." + secret + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	digest := sha256.Sum256([]byte("solsynth.owned-app.v1\x00" + app + "\x00" + token))
	scopeJSON, _ := json.Marshal(scopes)
	query(t, f.db, `INSERT INTO owned_app_secrets(id,app_id,secret_hash,scopes,expires_at,created_at,updated_at) VALUES($1,$2,$3,$4,now()+interval '1 hour',$5,$5)`, secret, app, digest[:], scopeJSON, now)
	query(t, f.db, `INSERT INTO commerce_v1.merchants(app_id,wallet_id) VALUES($1::uuid,$2::uuid)`, app, payoutWallet)
	return pub.ID, c.Credentials{ClientID: app, ClientSecret: token}
}

func TestPGMerchantSettlementReverseWalletTopologyDoesNotDeadlock(t *testing.T) {
	f := newCommerceFixture(t)
	merchantRepo(t, f)
	walletFund(t, f.db, f.payee, "synthetic-commerce", "100.000")
	secondPublisher, secondCredentials := secondMerchantApp(t, f, f.wallet.ID)

	first := commerceOrder(t, f, "reverse-first", false)
	if _, err := f.r.PayOrder(f.ctx, f.actor, first.ID, &f.wallet.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.UpdateOrder(f.ctx, first.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
		t.Fatal(err)
	}
	second, err := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: secondCredentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "10.001")}, "reverse-second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.r.PayOrder(f.ctx, f.actor, second.ID, &f.payee.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.r.UpdateOrder(f.ctx, second.ID, c.UpdateStatus{Credentials: secondCredentials, Status: c.Finished}); err != nil {
		t.Fatal(err)
	}

	firstRepo, firstCtx := merchantPool(t, f)
	secondRepo, secondCtx := merchantPool(t, f)
	type settlementResult struct {
		count int
		err   error
	}
	start := make(chan struct{})
	results := make(chan settlementResult, 2)
	for _, work := range []struct {
		repo      *merchantpg.Repository
		ctx       context.Context
		publisher string
	}{{firstRepo, firstCtx, f.publisher}, {secondRepo, secondCtx, secondPublisher}} {
		work := work
		go func() {
			<-start
			settled, settleErr := work.repo.Settle(work.ctx, f.actor, work.publisher)
			results <- settlementResult{len(settled), settleErr}
		}()
	}
	close(start)
	deadline := time.After(10 * time.Second)
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil || result.count != 1 {
				t.Fatal("reverse wallet settlement", result.count, result.err)
			}
		case <-deadline:
			t.Fatal("reverse wallet settlement deadlocked")
		}
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id IN($1::uuid,$2::uuid) AND status=1`, first.ID, second.ID); n != 2 {
		t.Fatal("reverse wallet settlements not completed", n)
	}
}

func TestPGMerchantPendingSettlementRestartManualRelease(t *testing.T) {
	type saved struct {
		Actor, Publisher, Payer, Payee, OrderID, TransactionID, Token string
		Authority                                                     restartAuthoritySnapshot
	}
	const marker = "wallet-merchant-pending-settlement.json"
	if fixtureRestartPhase(t) == "before" {
		f := newCommerceFixture(t)
		merchantRepo(t, f)
		originalNow, wall := f.config.Now().UTC(), time.Now()
		f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
		f.config.Now = func() time.Time { return originalNow.Add(time.Since(wall)) }
		issuer, err := authority.New(authoritypg.New(f.db), f.config)
		if err != nil {
			t.Fatal(err)
		}
		pair := pgIssue(t, issuer, f.actor)
		o := commerceOrder(t, f, "restart-pending-settlement", false)
		paid, err := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
		if err != nil || paid.TransactionID == nil {
			t.Fatal(paid, err)
		}
		if _, err = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); err != nil {
			t.Fatal(err)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "89.999", "10.001", "89.999")
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=0`, o.ID); n != 1 {
			t.Fatal("restart fixture is not pending", n)
		}
		saveRestartFixture(t, marker, saved{Actor: f.actor, Publisher: f.publisher, Payer: f.wallet.ID, Payee: f.payee.ID, OrderID: o.ID, TransactionID: *paid.TransactionID, Token: pair.Token, Authority: saveAuthoritySnapshot(t, f.config)})
		return
	}

	db := database(t)
	var state saved
	loadRestartFixture(t, marker, &state)
	config := restoreAuthoritySnapshot(t, state.Authority)
	verifier, err := authority.NewVerifier(authoritypg.New(db), config)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := verifier.Authenticate(context.Background(), state.Token)
	if err != nil {
		t.Fatal(err)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx := guard.Bind(base, proof)
	r := merchantpg.New(db, guard).WithClock(config.Now)
	pending, err := r.PendingSettlements(ctx, state.Actor, state.Publisher)
	if err != nil || pending["synthetic-commerce"].Count != 1 || pending["synthetic-commerce"].Amount.String() != "10.001" {
		t.Fatal("pending settlement did not survive restart", pending, err)
	}
	settled, err := r.Settle(ctx, state.Actor, state.Publisher)
	if err != nil || len(settled) != 1 || settled[0].ID != state.TransactionID || settled[0].Status != wallet.Confirmed {
		t.Fatal("post-restart manual settlement", settled, err)
	}
	walletAssertBalance(t, walletpg.New(db), state.Payer, "synthetic-commerce", "89.999", "0.000", "89.999")
	walletAssertBalance(t, walletpg.New(db), state.Payee, "synthetic-commerce", "10.001", "0.000", "10.001")
	if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=1`, state.OrderID); n != 1 {
		t.Fatal("restart settlement row not confirmed", n)
	}
}

func TestPGFreeTrialRenewalUsesPendingMerchantSettlement(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	key := "trial-renewal-" + f.app
	definition := merchantDefinition(f, key)
	definition.AppIdentifier = &f.app
	definition.FreeTrialDays = 1
	if _, _, err := r.UpsertDefinition(f.ctx, f.actor, definition); err != nil {
		t.Fatal(err)
	}
	subscription, err := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{PayerWalletID: &f.wallet.ID}, IsFreeTrial: true}, "renewing-trial", false)
	if err != nil || !subscription.IsFreeTrial || subscription.RenewalAt == nil {
		t.Fatal(subscription, err)
	}
	past := time.Unix(f.clock.Load(), 0).UTC().Add(-time.Second)
	subscription.EndedAt = past
	subscription.RenewalAt = &past
	raw, _ := json.Marshal(subscription)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2,renewal_at=$2,document=$3::jsonb WHERE id=$1::uuid`, subscription.ID, past, raw)
	if processed, processErr := f.r.ProcessDue(f.ctx, 100); processErr != nil || processed != 1 {
		t.Fatal("trial renewal maintenance", processed, processErr)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.667", "3.333", "96.667")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	pending, err := r.PendingSettlements(f.ctx, f.actor, f.publisher)
	if err != nil || pending["synthetic-commerce"].Count != 1 || pending["synthetic-commerce"].Amount.String() != "3.333" {
		t.Fatal("trial renewal pending settlement", pending, err)
	}
	if settled, settleErr := r.Settle(f.ctx, f.actor, f.publisher); settleErr != nil || len(settled) != 1 {
		t.Fatal("trial renewal manual settlement", settled, settleErr)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.667", "0.000", "96.667")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "3.333", "0.000", "3.333")
	renewed, err := f.r.Subscription(f.ctx, f.actor, key)
	if err != nil || renewed.IsFreeTrial || renewed.PaymentDetails.OrderID == "" {
		t.Fatal("trial renewal state", renewed, err)
	}
}

func dueFreeTrial(t *testing.T, f commerceFixture, key string) c.Subscription {
	t.Helper()
	definition := merchantDefinition(f, key)
	definition.AppIdentifier = &f.app
	definition.FreeTrialDays = 1
	if _, _, err := merchantRepo(t, f).UpsertDefinition(f.ctx, f.actor, definition); err != nil {
		t.Fatal(err)
	}
	subscription, err := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{PayerWalletID: &f.wallet.ID}, IsFreeTrial: true}, key, false)
	if err != nil || !subscription.IsFreeTrial || subscription.RenewalAt == nil {
		t.Fatal(subscription, err)
	}
	past := time.Unix(f.clock.Load(), 0).UTC().Add(-time.Second)
	subscription.EndedAt = past
	subscription.RenewalAt = &past
	raw, _ := json.Marshal(subscription)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2,renewal_at=$2,document=$3::jsonb WHERE id=$1::uuid`, subscription.ID, past, raw)
	return subscription
}

func TestPGRenewalRejectsAccountPunishmentBeforeAndAfterWalletWait(t *testing.T) {
	assertExpiredUncharged := func(t *testing.T, f commerceFixture, key string) {
		t.Helper()
		subscription, err := f.r.Subscription(f.ctx, f.actor, key)
		if err != nil || subscription.Status != c.SubscriptionExpired || subscription.IsActive || subscription.IsAutoRenewal || subscription.RenewalAt != nil {
			t.Fatal("punished trial renewal state", subscription, err)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.merchant_settlements s JOIN commerce_v1.orders o ON o.id=s.order_id WHERE o.product_identifier=$1`, key); n != 0 {
			t.Fatal("punished renewal created settlement", n)
		}
	}

	t.Run("already active", func(t *testing.T) {
		f := newCommerceFixture(t)
		key := "punished-trial-" + f.app
		dueFreeTrial(t, f, key)
		query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1::uuid,'disable_account',clock_timestamp()-interval '1 second')`, f.actor)
		if processed, err := f.r.ProcessDue(f.ctx, 100); err != nil || processed != 1 {
			t.Fatal("active punishment maintenance", processed, err)
		}
		assertExpiredUncharged(t, f, key)
	})

	t.Run("scheduled while wallet locked", func(t *testing.T) {
		f := newCommerceFixture(t)
		key := "scheduled-punished-trial-" + f.app
		dueFreeTrial(t, f, key)
		query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1::uuid,'disable_account',clock_timestamp()+interval '500 milliseconds')`, f.actor)
		blocker, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var pid int
		if err = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = blocker.ExecContext(f.ctx, `SELECT id FROM wallet_v1.wallets WHERE id=$1::uuid FOR UPDATE`, f.wallet.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct {
			count int
			err   error
		}, 1)
		go func() {
			count, processErr := f.r.ProcessDue(f.ctx, 100)
			done <- struct {
				count int
				err   error
			}{count, processErr}
		}()
		waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "wallet_v1.wallets")
		time.Sleep(700 * time.Millisecond)
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		result := <-done
		if result.err != nil || result.count != 1 {
			t.Fatal("scheduled punishment maintenance", result.count, result.err)
		}
		assertExpiredUncharged(t, f, key)
	})
}

func TestPGAppPayoutScopeReplayEligibilityCurrencyAndExpiry(t *testing.T) {
	t.Run("payout and eligibility", func(t *testing.T) {
		f := newCommerceFixture(t)
		enablePayoutScope(t, f)
		payee, _, payeeWallet := merchantOther(t, f)
		walletFund(t, f.db, f.payee, "synthetic-commerce", "20.000")
		req := c.AppPayout{Credentials: f.credentials, PayeeAccountID: payee, Currency: "synthetic-commerce", Amount: walletAmount(t, "5.000")}
		one, err := f.r.CreateAppPayout(f.ctx, req, "payout")
		if err != nil {
			t.Fatal(one, err)
		}
		replay, err := f.r.CreateAppPayout(f.ctx, req, "payout")
		if err != nil || replay.ID != one.ID {
			t.Fatal("payout replay", replay, err)
		}
		changed := req
		changed.Amount = walletAmount(t, "5.001")
		if _, err = f.r.CreateAppPayout(f.ctx, changed, "payout"); !errors.Is(err, wallet.ErrIdempotency) {
			t.Fatal("changed payout replay", err)
		}
		for _, currency := range []string{" synthetic-commerce", "synthetic-commerce\n"} {
			bad := req
			bad.Currency = currency
			if _, err = f.r.CreateAppPayout(f.ctx, bad, "bad-currency"); !errors.Is(err, c.ErrInvalid) {
				t.Fatal("invalid payout currency", currency, err)
			}
		}
		query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, payee)
		if _, err = f.r.CreateAppPayout(f.ctx, req, "disabled-payee"); !errors.Is(err, wallet.ErrForbidden) {
			t.Fatal("disabled payout recipient", err)
		}
		query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, payee)
		if err = authoritypg.New(f.db).ReplacePolicy(f.ctx, payee, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); err != nil {
			t.Fatal(err)
		}
		if _, err = f.r.CreateAppPayout(f.ctx, req, "punished-payee"); !errors.Is(err, wallet.ErrForbidden) {
			t.Fatal("punished payout recipient", err)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "15.000", "0.000", "15.000")
		walletAssertBalance(t, walletpg.New(f.db), payeeWallet.ID, "synthetic-commerce", "5.000", "0.000", "5.000")
	})

	t.Run("credential expires while wallet locked", func(t *testing.T) {
		f := newCommerceFixture(t)
		enablePayoutScope(t, f)
		payee, _, _ := merchantOther(t, f)
		walletFund(t, f.db, f.payee, "synthetic-commerce", "10.000")
		query(t, f.db, `UPDATE owned_app_secrets SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1::uuid`, f.secretID)
		blocker, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var pid int
		if err = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = blocker.ExecContext(f.ctx, `SELECT id FROM wallet_v1.wallets WHERE id=$1::uuid FOR UPDATE`, f.payee.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, payoutErr := f.r.CreateAppPayout(f.ctx, c.AppPayout{Credentials: f.credentials, PayeeAccountID: payee, Currency: "synthetic-commerce", Amount: walletAmount(t, "1.000")}, "expiring-payout")
			done <- payoutErr
		}()
		waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "wallet_v1.wallets")
		time.Sleep(700 * time.Millisecond)
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		if err = <-done; err == nil {
			t.Fatal("expired app credential committed payout")
		}
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "10.000", "0.000", "10.000")
	})

	t.Run("scheduled recipient punishment activates while wallet locked", func(t *testing.T) {
		f := newCommerceFixture(t)
		enablePayoutScope(t, f)
		payee, _, payeeWallet := merchantOther(t, f)
		walletFund(t, f.db, f.payee, "synthetic-commerce", "10.000")
		query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1::uuid,'disable_account',clock_timestamp()+interval '500 milliseconds')`, payee)
		blocker, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var pid int
		if err = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = blocker.ExecContext(f.ctx, `SELECT id FROM wallet_v1.wallets WHERE id=$1::uuid FOR UPDATE`, f.payee.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, payoutErr := f.r.CreateAppPayout(f.ctx, c.AppPayout{Credentials: f.credentials, PayeeAccountID: payee, Currency: "synthetic-commerce", Amount: walletAmount(t, "1.000")}, "scheduled-punishment")
			done <- payoutErr
		}()
		waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "wallet_v1.wallets")
		time.Sleep(700 * time.Millisecond)
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		if err = <-done; !errors.Is(err, wallet.ErrForbidden) {
			t.Fatal("scheduled disabled recipient credited", err)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "10.000", "0.000", "10.000")
		walletAssertBalance(t, walletpg.New(f.db), payeeWallet.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	})

	t.Run("merchant rebind wins before debit", func(t *testing.T) {
		f := newCommerceFixture(t)
		enablePayoutScope(t, f)
		payee, _, payeeWallet := merchantOther(t, f)
		walletFund(t, f.db, f.payee, "synthetic-commerce", "10.000")
		newTreasury := walletCreate(t, walletpg.New(f.db), f.actor)
		walletFund(t, f.db, newTreasury, "synthetic-commerce", "10.000")
		blocker, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var pid int
		if err = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err = blocker.ExecContext(f.ctx, `SELECT app_id FROM commerce_v1.merchants WHERE app_id=$1::uuid FOR UPDATE`, f.app); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, payoutErr := f.r.CreateAppPayout(f.ctx, c.AppPayout{Credentials: f.credentials, PayeeAccountID: payee, Currency: "synthetic-commerce", Amount: walletAmount(t, "1.000")}, "rebound-payout")
			done <- payoutErr
		}()
		waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "commerce_v1.merchants")
		if _, err = blocker.ExecContext(f.ctx, `UPDATE commerce_v1.merchants SET wallet_id=$2::uuid WHERE app_id=$1::uuid`, f.app, newTreasury.ID); err != nil {
			t.Fatal(err)
		}
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		if err = <-done; err != nil {
			t.Fatal("payout after merchant rebind", err)
		}
		walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "10.000", "0.000", "10.000")
		walletAssertBalance(t, walletpg.New(f.db), newTreasury.ID, "synthetic-commerce", "9.000", "0.000", "9.000")
		walletAssertBalance(t, walletpg.New(f.db), payeeWallet.ID, "synthetic-commerce", "1.000", "0.000", "1.000")
	})
}

func TestPGWalletProductApplyReplayAndCanonicalOrder(t *testing.T) {
	f := newCommerceFixture(t)
	productID := "wallet.golds_resupply_pack"
	catalog, _ := json.Marshal(map[string]string{"identifier": productID, "currency": "synthetic-commerce"})
	r := merchantRepo(t, f).WithWalletProductCatalog(catalog)
	walletFund(t, f.db, f.payee, "synthetic-commerce", "12.000")
	var target string
	if err := f.db.QueryRow(`SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, f.actor).Scan(&target); err != nil {
		t.Fatal(err)
	}
	insert := func(app string) string {
		id := authorityUUID(t)
		now := time.Unix(f.clock.Load(), 0).UTC()
		o := c.Order{ID: id, Status: c.Paid, Currency: "synthetic-commerce", Amount: walletAmount(t, "4.000"), AppIdentifier: app, ProductIdentifier: &productID, PayeeWalletID: target, Items: []c.OrderItem{}, Meta: map[string]json.RawMessage{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(time.Hour)}
		raw, _ := json.Marshal(o)
		query(t, f.db, `INSERT INTO commerce_v1.orders(id,actor_id,payee_wallet_id,status,amount,currency,product_identifier,created_at,expires_at,document) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10::jsonb)`, id, f.actor, target, o.Status, o.Amount, o.Currency, productID, now, o.ExpiredAt, raw)
		return id
	}
	orderID := insert("internal")
	if _, err := r.ApplyWalletProduct(f.ctx, f.actor, orderID, merchants.WalletProductApply{TreasuryWalletID: f.payee.ID}, "unconfigured"); !errors.Is(err, merchants.ErrUnavailable) {
		t.Fatal("wallet product used caller treasury without trusted configuration", err)
	}
	r.WithWalletProductTreasury(f.payee.ID)
	one, err := r.ApplyWalletProduct(f.ctx, f.actor, orderID, merchants.WalletProductApply{TreasuryWalletID: f.payee.ID}, "apply")
	if err != nil {
		t.Fatal(one, err)
	}
	replay, err := r.ApplyWalletProduct(f.ctx, f.actor, orderID, merchants.WalletProductApply{TreasuryWalletID: f.payee.ID}, "apply")
	if err != nil || replay.ID != one.ID {
		t.Fatal("wallet product replay", replay, err)
	}
	otherTreasury := walletCreate(t, walletpg.New(f.db), f.actor)
	if _, err = r.ApplyWalletProduct(f.ctx, f.actor, orderID, merchants.WalletProductApply{TreasuryWalletID: otherTreasury.ID}, "apply"); !errors.Is(err, merchants.ErrForbidden) {
		t.Fatal("changed wallet product replay", err)
	}
	if _, err = r.ApplyWalletProduct(f.ctx, f.actor, insert("developer.app:"+f.app), merchants.WalletProductApply{TreasuryWalletID: f.payee.ID}, "wrong-app"); !errors.Is(err, merchants.ErrConflict) {
		t.Fatal("developer order accepted as internal wallet product", err)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "8.000", "0.000", "8.000")
	walletAssertBalance(t, walletpg.New(f.db), target, "synthetic-commerce", "104.000", "0.000", "104.000")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE id=$1::uuid`, one.ID); n != 1 {
		t.Fatal("wallet product replay duplicated transfer", n)
	}
}

func TestPGMerchantProofTTLBlocksTrialAndSettlementRead(t *testing.T) {
	newExpiring := func(t *testing.T, f commerceFixture) (*commercepg.Repository, *merchantpg.Repository, context.Context, *atomic.Int64) {
		t.Helper()
		clock := &atomic.Int64{}
		clock.Store(f.config.Now().Unix())
		config := f.config
		config.Now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
		config.AccessTTL = time.Second
		issuer, err := authority.New(authoritypg.New(f.db), config)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := authority.NewVerifier(authoritypg.New(f.db), config)
		if err != nil {
			t.Fatal(err)
		}
		pair := pgIssue(t, issuer, f.actor)
		proof, err := verifier.Authenticate(f.ctx, pair.Token)
		if err != nil {
			t.Fatal(err)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
		base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		t.Cleanup(cancel)
		ctx := guard.Bind(base, proof)
		commerce := commercepg.New(f.db, guard, commercepg.AppAdapters(apppg.New(f.db))).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() })
		merchant := merchantpg.New(f.db, guard).WithClock(func() time.Time { return time.Unix(f.clock.Load(), 0).UTC() })
		return commerce, merchant, ctx, clock
	}

	t.Run("free trial", func(t *testing.T) {
		f := newCommerceFixture(t)
		r := merchantRepo(t, f)
		key := "ttl-trial-" + f.app
		definition := merchantDefinition(f, key)
		definition.AppIdentifier = &f.app
		definition.FreeTrialDays = 1
		if _, _, err := r.UpsertDefinition(f.ctx, f.actor, definition); err != nil {
			t.Fatal(err)
		}
		commerce, _, ctx, clock := newExpiring(t, f)
		blocker, pid := atomicBlocker(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, "owned_publishers", f.publisher)
		done := make(chan error, 1)
		go func() {
			_, err := commerce.CreateSubscription(ctx, f.actor, c.CreateSubscription{Identifier: key, PaymentMethod: "solian.wallet", IsFreeTrial: true}, "ttl-trial", false)
			done <- err
		}()
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_publishers")
		clock.Add(2)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err == nil {
			t.Fatal("expired proof committed free trial")
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.subscription_trials WHERE account_id=$1::uuid AND identifier=$2`, f.actor, key); n != 0 {
			t.Fatal("expired proof persisted trial", n)
		}
	})

	t.Run("settlement read", func(t *testing.T) {
		f := newCommerceFixture(t)
		merchantRepo(t, f)
		_, merchant, ctx, clock := newExpiring(t, f)
		blocker, pid := atomicBlocker(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, "owned_publishers", f.publisher)
		done := make(chan error, 1)
		go func() {
			_, err := merchant.PendingSettlements(ctx, f.actor, f.publisher)
			done <- err
		}()
		waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_publishers")
		clock.Add(2)
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err == nil {
			t.Fatal("expired proof returned settlement data")
		}
	})
}

func TestPGGiftRedeemRechecksProofAndExpiryAfterWait(t *testing.T) {
	assertRolledBack := func(t *testing.T, f redeemWaitFixture) {
		t.Helper()
		if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND identifier=$2`, f.recipient.actor, f.giver.plan); n != 0 {
			t.Fatal("failed redemption granted entitlement", n)
		}
		if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid`, f.order.ID); n != 0 {
			t.Fatal("failed redemption created settlement", n)
		}
		if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.gifts WHERE id=$1::uuid AND status=1`, f.gift.ID); n != 1 {
			t.Fatal("failed redemption changed gift", n)
		}
		if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1::uuid AND event_type='commerce.gift.redeemed'`, f.gift.ID); n != 0 {
			t.Fatal("failed redemption emitted outbox", n)
		}
	}

	t.Run("proof ttl", func(t *testing.T) {
		f := newRedeemWaitFixture(t)
		clock := &atomic.Int64{}
		clock.Store(f.recipient.config.Now().Unix())
		config := f.recipient.config
		config.Now = func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
		config.AccessTTL = time.Second
		issuer, err := authority.New(authoritypg.New(f.recipient.db), config)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := authority.NewVerifier(authoritypg.New(f.recipient.db), config)
		if err != nil {
			t.Fatal(err)
		}
		pair := pgIssue(t, issuer, f.recipient.actor)
		proof, err := verifier.Authenticate(f.recipient.ctx, pair.Token)
		if err != nil {
			t.Fatal(err)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.recipient.db), verifier)
		base, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ctx := guard.Bind(base, proof)
		repo := commercepg.New(f.recipient.db, guard, commercepg.AppAdapters(apppg.New(f.recipient.db))).WithClock(func() time.Time { return time.Unix(f.recipient.clock.Load(), 0).UTC() })
		locks := atomicAuthorityFixture{db: f.recipient.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_publishers", f.giver.publisher)
		done := make(chan error, 1)
		go func() { _, redeemErr := repo.RedeemGift(ctx, f.recipient.actor, f.gift.GiftCode); done <- redeemErr }()
		waitAtomicBlocked(t, locks, pid, "owned_publishers")
		clock.Add(2)
		if err = blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err = <-done; err == nil {
			t.Fatal("expired proof redeemed gift")
		}
		assertRolledBack(t, f)
	})

	t.Run("gift expires", func(t *testing.T) {
		f := newRedeemWaitFixture(t)
		locks := atomicAuthorityFixture{db: f.recipient.db, ctx: f.recipient.ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_publishers", f.giver.publisher)
		done := make(chan error, 1)
		go func() {
			_, redeemErr := f.recipient.r.RedeemGift(f.recipient.ctx, f.recipient.actor, f.gift.GiftCode)
			done <- redeemErr
		}()
		waitAtomicBlocked(t, locks, pid, "owned_publishers")
		f.recipient.clock.Store(f.gift.ExpiresAt.Unix())
		if err := blocker.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, c.ErrConflict) {
			t.Fatal("expired gift redeemed after wait", err)
		}
		assertRolledBack(t, f)
	})
}

func TestPGGiftRedemptionPendingSettlementCanBeRefunded(t *testing.T) {
	f := newRedeemWaitFixture(t)
	redemption, err := f.recipient.r.RedeemGift(f.recipient.ctx, f.recipient.actor, f.gift.GiftCode)
	if err != nil || redemption.Gift.Status != c.GiftRedeemed || redemption.Subscription.ID == "" {
		t.Fatal("gift redemption", redemption, err)
	}
	if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=0`, f.order.ID); n != 1 {
		t.Fatal("redeemed gift did not create pending settlement", n)
	}
	walletAssertBalance(t, walletpg.New(f.giver.db), f.giver.wallet.ID, "synthetic-commerce", "96.667", "3.333", "96.667")
	walletAssertBalance(t, walletpg.New(f.giver.db), f.giver.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	refunded, err := merchantRepo(t, f.giver).RefundOrder(f.giver.ctx, f.giver.actor, f.order.ID, "refund-redeemed-gift")
	if err != nil || refunded.ID == "" || refunded.Status != wallet.Refunded {
		t.Fatal("pending gift refund", refunded, err)
	}
	walletAssertBalance(t, walletpg.New(f.giver.db), f.giver.wallet.ID, "synthetic-commerce", "100.000", "0.000", "100.000")
	walletAssertBalance(t, walletpg.New(f.giver.db), f.giver.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	if n := commerceCount(t, f.giver.db, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE order_id=$1::uuid AND status=2`, f.order.ID); n != 1 {
		t.Fatal("refunded gift settlement state", n)
	}
}

func TestPGMerchantAdminReadsKeepOperatorScopesAndBillingIsolation(t *testing.T) {
	f := newCommerceFixture(t)
	r := merchantRepo(t, f)
	o := commerceOrder(t, f, "operator-visible-order", false)
	purchase := c.VerifiedPurchase{Provider: "paddle", ExternalID: "operator-isolation-" + f.app, AccountID: f.actor, Identifier: f.plan, BegunAt: time.Unix(f.clock.Load(), 0).UTC(), DurationDays: 1, Testing: true}
	if _, err := f.r.ApplyProvider(f.ctx, purchase); err != nil {
		t.Fatal(err)
	}

	operator := authorityUUID(t)
	if err := authoritypg.SeedSynthetic(f.ctx, f.db, operator, "synthetic_wallet_operator_"+operator, time.Unix(f.clock.Load(), 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := authoritypg.New(f.db).ReplacePolicy(f.ctx, operator, []authority.Rule{{Pattern: "orders.view"}}, nil); err != nil {
		t.Fatal(err)
	}
	pair := pgIssue(t, f.issuer, operator)
	proof, err := f.verifier.Authenticate(f.ctx, pair.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx := f.guard.Bind(f.ctx, proof)
	orders, err := r.AdminOrders(ctx, operator, merchants.AdminQuery{})
	if err != nil || orders.Total < 1 {
		t.Fatal("orders operator could not read admin projection", orders, err)
	}
	found := false
	for _, item := range orders.Items {
		found = found || item.ID == o.ID
	}
	if !found {
		t.Fatal("admin order projection omitted target")
	}
	if _, err = r.AdminTransactions(ctx, operator, merchants.AdminQuery{}); err == nil {
		t.Fatal("orders-only operator read wallet ledger")
	}
	inbound, err := r.AdminInboundOrders(ctx, operator, merchants.AdminQuery{})
	if err != nil || inbound.Total != 1 || len(inbound.Items) != 1 {
		t.Fatal("orders operator inbound projection", inbound, err)
	}
	billing, err := r.Billing(ctx, operator, merchants.AdminQuery{})
	if err != nil || billing.Total != 0 || len(billing.Items) != 0 {
		t.Fatal("operator billing crossed account boundary", billing, err)
	}
}
