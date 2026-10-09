// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	w "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

func reportingTransfer(t *testing.T, f backendFixture, payer, payee w.Wallet, amount, currency, token string, hold bool) w.Transaction {
	t.Helper()
	body := map[string]any{"amount": json.Number(amount), "currency": currency, "payer_wallet_id": payer.ID, "payee_wallet_id": payee.ID, "require_confirmation": hold}
	created := backendDecode[w.Transaction](t, backendJSONCall(t, f.app, "POST", "/wallet/wallets/transfer", token, body, 200))
	return backendDecode[w.Transaction](t, backendCall(t, f.app, "GET", "/api/wallets/transactions/"+created.ID, token, "", 200))
}
func reportingWallets(t *testing.T, f backendFixture) (w.Wallet, w.Wallet) {
	t.Helper()
	repo := walletpg.New(f.db)
	a, b := walletCreate(t, repo, f.actor), walletCreate(t, repo, f.other)
	for _, item := range []struct {
		id     string
		wallet w.Wallet
	}{{f.actor, a}, {f.other, b}} {
		query(t, f.db, `INSERT INTO wallet_v1.payment_policies(account_id,pin_required,updated_at)VALUES($1,false,now())`, item.id)
		walletFund(t, f.db, item.wallet, "points", "100")
		walletFund(t, f.db, item.wallet, "golds", "100")
	}
	return a, b
}
func TestPGBackendWalletReportingLifecyclePrivacyAndDates(t *testing.T) {
	f := newBackendFixture(t)
	// Distinct canonical creation instants are needed to test an inclusive
	// single-instant range; the shared backend fixture normally freezes time.
	fixtureNow := f.config.Now()
	f.config.Now = func() time.Time { return fixtureNow }
	f.app = backendApp(t, f.db, f.config)
	a, b := reportingWallets(t, f)
	empty := backendDecode[w.WalletStats](t, backendCall(t, f.app, "GET", "/api/wallets/stats", f.token, "", 200))
	if empty.TotalIncome == nil || empty.TotalIncome.Sign() != 0 || len(empty.Summary) != 0 {
		t.Fatal(empty)
	}
	settled := reportingTransfer(t, f, a, b, "1.001", "points", f.token, false)
	fixtureNow = fixtureNow.Add(time.Second)
	hold := reportingTransfer(t, f, a, b, "2.002", "points", f.token, true)
	fixtureNow = fixtureNow.Add(time.Second)
	refunded := reportingTransfer(t, f, a, b, "3.003", "points", f.token, true)
	backendCall(t, f.app, "POST", "/api/wallets/transactions/"+refunded.ID+"/reject", f.otherToken, "", 200)
	stats := backendDecode[w.WalletStats](t, backendCall(t, f.app, "GET", "/wallet/wallets/stats", f.token, "", 200))
	if stats.TotalTransactions != 3 || stats.TotalOutgoing.String() != "1.001" || stats.TotalIncome.String() != "0.000" || stats.Sum.String() != "-1.001" {
		t.Fatal(stats)
	}
	backendCall(t, f.app, "POST", "/api/wallets/transactions/"+hold.ID+"/confirm", f.otherToken, "", 200)
	stats = backendDecode[w.WalletStats](t, backendCall(t, f.app, "GET", "/api/wallets/stats?currencies=PoInTs&wallets="+a.ID+"&wallets="+a.ID, f.token, "", 200))
	if stats.TotalTransactions != 3 || stats.TotalOutgoing.String() != "3.003" {
		t.Fatal(stats)
	}
	// Explicit timezone offset denotes the same inclusive boundary instant.
	at := settled.CreatedAt.In(time.FixedZone("synthetic offset", 3600)).Format(time.RFC3339Nano)
	overview := backendDecode[w.WalletOverview](t, backendCall(t, f.app, "GET", "/api/wallets/overview?startDate="+url.QueryEscape(at)+"&endDate="+url.QueryEscape(at), f.token, "", 200))
	if overview.TotalSpending.String() != "1.001" || overview.StartDate == nil || !overview.StartDate.Equal(settled.CreatedAt) {
		t.Fatal(overview)
	}
	reportingTransfer(t, f, b, a, "0.003", "golds", f.otherToken, false)
	overview = backendDecode[w.WalletOverview](t, backendCall(t, f.app, "GET", "/api/wallets/overview", f.token, "", 200))
	if overview.TotalIncome != nil || overview.TotalSpending != nil || overview.NetTotal != nil || overview.Summary["Transfer"].Currencies["points"].Spending.String() != "3.003" || overview.Summary["Transfer"].Currencies["golds"].Income.String() != "0.003" {
		t.Fatal(overview)
	}
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+a.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/wallets/orders?wallet="+a.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/wallets/stats", "", "", 401)
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+a.ID+"&wallets="+b.ID, f.token, "", 404)
	// Canonical realm membership is required even for a public realm wallet.
	query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,2,now())`, f.actor)
	realm := backendDecode[social.Realm](t, backendJSONCall(t, f.app, "POST", "/passport/realms", f.token, map[string]any{"slug": "report-" + f.actor, "name": "Synthetic reporting realm", "is_public": true, "is_community": true}, 200))
	realmWallet := backendDecode[w.Wallet](t, backendJSONCall(t, f.app, "POST", "/api/wallets", f.token, map[string]any{"realm_id": realm.ID}, 200))
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+realmWallet.ID, f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+realmWallet.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "POST", "/passport/realms/"+realm.ID+"/members/me", f.otherToken, "", 200)
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+realmWallet.ID, f.otherToken, "", 200)
	backendCall(t, f.app, "GET", "/api/wallets/orders?wallet="+realmWallet.ID, f.otherToken, "", 200)
	backendCall(t, f.app, "DELETE", "/passport/realms/"+realm.ID+"/members/me", f.otherToken, "", 204)
	backendCall(t, f.app, "GET", "/api/wallets/stats?wallets="+realmWallet.ID, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/wallets/orders?wallet="+realmWallet.ID, f.otherToken, "", 404)
}

func TestPGWalletReportingOrderPaginationAndSettlement(t *testing.T) {
	f := newCommerceFixture(t)
	repo := walletpg.NewWithAuthority(f.db, f.guard)
	ids := []string{}
	for i, key := range []string{"report-finished", "report-held", "report-refunded", "report-unpaid"} {
		req := c.CreateOrder{Credentials: f.credentials, Currency: "synthetic-commerce", Amount: walletAmount(t, "1.001"), Meta: map[string]json.RawMessage{"private_note": json.RawMessage(`"synthetic-private-destination"`)}}
		o, e := f.r.CreateOrder(f.ctx, req, key)
		if e != nil {
			t.Fatal(e)
		}
		if i == 3 {
			continue
		}
		ids = append(ids, o.ID)
		if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
			t.Fatal(e)
		}
		if i != 1 {
			status := c.Finished
			if i == 2 {
				status = c.Cancelled
			}
			if _, e = f.r.UpdateOrder(f.ctx, o.ID, c.UpdateStatus{Credentials: f.credentials, Status: status}); e != nil {
				t.Fatal(e)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids))) // The trusted fixture clock gives all orders one timestamp.
	for i, id := range ids {
		page, e := repo.WalletOrders(f.ctx, f.actor, w.OrderHistoryQuery{TransactionQuery: w.TransactionQuery{WalletID: f.wallet.ID, Take: 1, Offset: i}})
		if e != nil || page.Total != 3 || len(page.Items) != 1 {
			t.Fatal(page, e)
		}
		var item struct {
			c.Order
			Transaction w.Transaction `json:"transaction"`
		}
		if e = json.Unmarshal(page.Items[0], &item); e != nil || item.ID != id || item.Transaction.ID == "" || len(item.Meta) != 0 || strings.Contains(string(page.Items[0]), "synthetic-private-destination") {
			t.Fatal(string(page.Items[0]), e)
		}
	}
	page, e := repo.WalletOrders(f.ctx, f.actor, w.OrderHistoryQuery{TransactionQuery: w.TransactionQuery{WalletID: f.wallet.ID, Take: 20, Offset: 90}})
	if e != nil || page.Total != 3 || len(page.Items) != 0 {
		t.Fatal(page, e)
	}
	finished := 3
	page, e = repo.WalletOrders(f.ctx, f.actor, w.OrderHistoryQuery{TransactionQuery: w.TransactionQuery{WalletID: f.payee.ID, Direction: "income", Take: 20}, Status: &finished})
	if e != nil || page.Total != 1 {
		t.Fatal(page, e)
	}
	stats, e := repo.WalletStats(f.ctx, f.actor, w.StatsQuery{Period: 30, Wallets: []string{f.wallet.ID, f.payee.ID}, Currencies: []string{"synthetic-commerce"}})
	if e != nil || stats.TotalOrders != 4 || stats.TotalTransactions != 3 || stats.TotalIncome.String() != "1.001" || stats.TotalOutgoing.String() != "1.001" || stats.Sum.Sign() != 0 {
		t.Fatal(stats, e)
	}
}

func TestPGBackendWalletReportingFundsDoNotDoubleCount(t *testing.T) {
	f := newFundsFixture(t)
	fund := f.create(t, map[string]any{"is_raising": true, "target_amount": 3.003, "is_open": true})
	f.contribute(t, fund.ID, f.one, 2.002, 200)
	report := func() w.WalletStats {
		return backendDecode[w.WalletStats](t, backendCall(t, f.app, "GET", "/wallet/wallets/stats?currencies="+fundCurrency, f.token, "", 200))
	}
	if v := report(); v.TotalIncome == nil || v.TotalIncome.Sign() != 0 {
		t.Fatal(v)
	}
	f.contribute(t, fund.ID, f.two, 1.001, 200)
	cancelled := f.create(t, map[string]any{"is_raising": true, "target_amount": 5, "is_open": true})
	f.contribute(t, cancelled.ID, f.one, 1.001, 200)
	backendCall(t, f.app, "POST", "/api/wallets/funds/"+cancelled.ID+"/cancel", f.token, "", 200)
	if v := report(); v.TotalIncome.String() != "3.003" || v.TotalOutgoing.Sign() != 0 {
		t.Fatal(v)
	}
}

func TestPGWalletReportingWaitsForCurrentAuthority(t *testing.T) {
	for _, change := range []string{"account", "session", "scopes"} {
		t.Run(change, func(t *testing.T) {
			f := newAtomicAuthorityFixture(t)
			repo := walletpg.NewWithAuthority(f.db, f.guard)
			blocker, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(f.ctx, `SELECT pg_backend_pid() FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := repo.WalletStats(f.ctx, f.actor, w.StatsQuery{Period: 30}); done <- e }()
			waitAtomicBlocked(t, f, pid, "owned_authority_accounts")
			switch change {
			case "account":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor)
			case "session":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp(),epoch=epoch+1,authorization_epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID)
			case "scopes":
				_, e = blocker.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET scopes='["posts.view"]'::jsonb,epoch=epoch+1,authorization_epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, w.ErrForbidden) && !errors.Is(e, w.ErrUnauthorized) {
				t.Fatal("stale report authority", e)
			}
			if _, e = repo.WalletOverview(context.Background(), f.actor, w.OverviewQuery{}); !errors.Is(e, w.ErrUnauthorized) {
				t.Fatal("missing original proof", e)
			}
		})
	}
	for _, kind := range []string{"stats", "orders"} {
		t.Run("realm-expiry-after-wallet-wait/"+kind, func(t *testing.T) {
			f := newAtomicAuthorityFixture(t)
			realm := authorityUUID(t)
			wallet, e := walletpg.New(f.db).CreateWallet(f.ctx, w.Owner{RealmID: realm}, "Synthetic expiring report wallet")
			if e != nil {
				t.Fatal(e)
			}
			blocker, pid := atomicBlocker(t, f, "wallet_v1.wallets", wallet.ID)
			query(t, f.db, `INSERT INTO wallet_v1.realm_grants(account_id,realm_id,role,expires_at,updated_at) VALUES($1,$2,'viewer',clock_timestamp()+interval '1 second',clock_timestamp())`, f.actor, realm)
			repo := walletpg.NewWithAuthority(f.db, f.guard)
			done := make(chan error, 1)
			go func() {
				var e error
				if kind == "stats" {
					_, e = repo.WalletStats(f.ctx, f.actor, w.StatsQuery{Period: 30, Wallets: []string{wallet.ID}})
				} else {
					_, e = repo.WalletOrders(f.ctx, f.actor, w.OrderHistoryQuery{TransactionQuery: w.TransactionQuery{WalletID: wallet.ID, Take: 20}})
				}
				done <- e
			}()
			waitAtomicBlocked(t, f, pid, "wallet_v1.wallets")
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var expired bool
				if e = f.db.QueryRowContext(f.ctx, `SELECT expires_at<=clock_timestamp() FROM wallet_v1.realm_grants WHERE account_id=$1 AND realm_id=$2`, f.actor, realm).Scan(&expired); e != nil {
					t.Fatal(e)
				}
				if expired {
					break
				}
				select {
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				case <-ticker.C:
				}
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, w.ErrNotFound) {
				t.Fatal("expired realm scope returned report data", e)
			}
		})
	}
}

func TestPGBackendWalletReportingRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	const marker = "wallet-reporting-original-proof.json"
	type saved struct {
		Actor, Token, Wallet string
		Authority            restartAuthoritySnapshot
		Spending             string
	}
	if phase == "before" {
		f := newBackendFixture(t)
		now, wall := f.config.Now(), time.Now()
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return now.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		f.token = pgIssue(t, issuer, f.actor).Token
		f.otherToken = pgIssue(t, issuer, f.other).Token
		f.app = backendApp(t, db, f.config)
		a, b := reportingWallets(t, f)
		reportingTransfer(t, f, a, b, "1.001", "points", f.token, false)
		v := backendDecode[w.WalletOverview](t, backendCall(t, f.app, "GET", "/api/wallets/overview", f.token, "", 200))
		if v.TotalSpending.String() != "1.001" {
			t.Fatal(v)
		}
		saveRestartFixture(t, marker, saved{f.actor, f.token, a.ID, saveAuthoritySnapshot(t, f.config), v.TotalSpending.String()})
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	app := backendApp(t, db, restoreAuthoritySnapshot(t, v.Authority))
	got := backendDecode[w.WalletOverview](t, backendCall(t, app, "GET", "/api/wallets/overview", v.Token, "", 200))
	if got.AccountID != v.Actor || got.TotalSpending == nil || got.TotalSpending.String() != v.Spending {
		t.Fatal(got)
	}
	stats := backendDecode[w.WalletStats](t, backendCall(t, app, "GET", "/api/wallets/stats?wallets="+v.Wallet, v.Token, "", 200))
	if stats.TotalTransactions != 1 || stats.TotalOutgoing.String() != v.Spending {
		t.Fatal(stats)
	}
}
