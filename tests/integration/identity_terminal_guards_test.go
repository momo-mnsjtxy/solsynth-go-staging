// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"testing"
	"time"
)

// Table names are fixed test cases. The real writer's relation lock wait is
// observed with pg_blocking_pids, rather than an in-process artificial delay.
func pgIdentityTableBlocker(t *testing.T, db *sql.DB, ctx context.Context, table string) (*sql.Tx, int) {
	t.Helper()
	if table != "owned_fortune_requests" && table != "owned_progression_outbox" {
		t.Fatal("unexpected identity test relation")
	}
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `LOCK TABLE `+table+` IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	return tx, pid
}

func pgIdentityWaitUntil(t *testing.T, db *sql.DB, ctx context.Context, at time.Time) {
	t.Helper()
	if _, e := db.ExecContext(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM($1::timestamptz-clock_timestamp())))::double precision+0.02)`, at); e != nil {
		t.Fatal(e)
	}
}

func TestPGBackdatedCheckInTerminalReportWaitRechecksPaidExpiry(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		name := "new check-in"
		if duplicate {
			name = "duplicate check-in"
		}
		t.Run(name, func(t *testing.T) {
			f := newCommerceFixture(t)
			query(t, f.db, `UPDATE owned_authority_accounts SET created_at=clock_timestamp()-interval '10 days'WHERE id=$1`, f.actor)
			sub, e := f.r.CreateSubscription(f.ctx, f.actor, commerce.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: commerce.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "owned-terminal-backdate-perk", false)
			if e != nil {
				t.Fatal(e)
			}
			order, e := f.r.SubscriptionOrder(f.ctx, f.actor, sub.Identifier)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.r.PayOrder(f.ctx, f.actor, order.ID, &f.wallet.ID); e != nil {
				t.Fatal(e)
			}
			reports, e := fortunepg.New(f.db, f.guard, fortunepg.Config{Timeout: time.Second, MaxAttempts: 2})
			if e != nil {
				t.Fatal(e)
			}
			if e = reports.Ready(f.ctx); e != nil {
				t.Fatal(e)
			}
			r := progressionpg.NewWithCheckInReports(f.db, f.guard, reports).WithCheckInPerks(f.r)
			if e = r.WithCheckInPolicy(progressionpg.CheckInPolicy{BackdateWindowDays: 7, PerkProducts: []string{f.plan}}); e != nil {
				t.Fatal(e)
			}
			var yesterday time.Time
			if e = f.db.QueryRow(`SELECT clock_timestamp()-interval '1 day'`).Scan(&yesterday); e != nil {
				t.Fatal(e)
			}
			want := 0
			if duplicate {
				if _, e = r.BackdatedCheckIn(f.ctx, f.actor, yesterday); e != nil {
					t.Fatal(e)
				}
				want = 1
			}
			// This is an owned synthetic paid cycle boundary fixture: the real
			// subscription creator and internal-wallet PayOrder already supplied
			// account, economics and payment provenance. Keep the canonical JSON
			// and SQL timeline equal while making its next expiry observable.
			var raw []byte
			if e = f.db.QueryRow(`SELECT document FROM commerce_v1.subscriptions WHERE id=$1`, sub.ID).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(raw, &sub); e != nil {
				t.Fatal(e)
			}
			var expiry time.Time
			if e = f.db.QueryRow(`SELECT clock_timestamp()+interval '5 seconds'`).Scan(&expiry); e != nil {
				t.Fatal(e)
			}
			sub.BegunAt = expiry.Add(-24 * time.Hour)
			sub.EndedAt = expiry
			sub.RenewalAt = nil
			raw, e = json.Marshal(sub)
			if e != nil {
				t.Fatal(e)
			}
			query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2,ended_at=$3,renewal_at=NULL,document=$4 WHERE id=$1`, sub.ID, sub.BegunAt, sub.EndedAt, raw)
			blocker, pid := pgIdentityTableBlocker(t, f.db, f.ctx, "owned_fortune_requests")
			result := make(chan error, 1)
			go func() { _, e := r.BackdatedCheckIn(f.ctx, f.actor, yesterday); result <- e }()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_fortune_requests")
			pgIdentityWaitUntil(t, f.db, f.ctx, expiry)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-result; !errors.Is(e, progression.ErrForbidden) {
				t.Fatal("terminal report wait admitted expired paid entitlement", duplicate, e)
			}
			var checkins, xp, audit, fortune int
			if e = f.db.QueryRow(`SELECT (SELECT count(*)FROM owned_progression_checkins WHERE account_id=$1),(SELECT count(*)FROM owned_progression_experience WHERE account_id=$1 AND reason_type='check-in'),(SELECT count(*)FROM owned_progression_audit WHERE actor_id=$1 AND action='check-in'),(SELECT count(*)FROM owned_fortune_requests r JOIN owned_progression_checkins c ON c.id=r.checkin_id WHERE c.account_id=$1)`, f.actor).Scan(&checkins, &xp, &audit, &fortune); e != nil || checkins != want || xp != want || audit != want || fortune != want {
				t.Fatal("terminal expiry did not roll back check-in/XP/audit/report", checkins, xp, audit, fortune, e)
			}
		})
	}
}

func TestPGSourcePointsActualWorkerTerminalOutboxWaitRechecksPunishment(t *testing.T) {
	db, _ := createIsolatedRestartDatabase(t, "pointswait")
	f, _ := newIdentitySavedFixture(t, db)
	treasury := ppOther(t, f)
	payer := pgSourceWallet(t, f, treasury, "100")
	payee := pgSourceWallet(t, f, f.actor, "0")
	grant := pgSourceGrant(t, f, 25)
	p, e := progressionpg.NewFundedSourcePoints(progressionpg.SourcePointConfig{TreasuryAccountID: treasury, TreasuryWalletID: payer, Currency: "synthetic-earned-points", MaxPerGrant: 25, MaxPerDay: 25})
	if e != nil {
		t.Fatal(e)
	}
	newWorker := func() *progressionpg.Repository {
		return progressionpg.NewWithAuthority(f.db, f.guard).WithSourcePointPayout(p)
	}
	// Author the scheduled policy before the account UPDATE and wallet locks.
	// No concurrent writer bypasses those locks to create a late punishment.
	var starts, expires time.Time
	if e = f.db.QueryRow(`INSERT INTO owned_authority_punishments(account_id,kind,starts_at,expires_at)SELECT $1,'disable_account',clock_timestamp()+interval '5 seconds',clock_timestamp()+interval '8 seconds'RETURNING starts_at,expires_at`, f.actor).Scan(&starts, &expires); e != nil {
		t.Fatal(e)
	}
	blocker, pid := pgIdentityTableBlocker(t, f.db, f.ctx, "owned_progression_outbox")
	type result struct {
		n int
		e error
	}
	done := make(chan result, 1)
	go func() { n, e := newWorker().ProcessSourcePoints(f.ctx, 100); done <- result{n, e} }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: f.ctx}, pid, "owned_progression_outbox")
	pgIdentityWaitUntil(t, f.db, f.ctx, starts)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	r := <-done
	if r.n != 0 || !errors.Is(r.e, authority.ErrForbidden) {
		t.Fatal("actual worker ignored terminal scheduled policy after outbox wait", r.n, r.e)
	}
	var marked, outbox, transfer int
	if e = f.db.QueryRow(`SELECT (SELECT count(*)FROM owned_progression_grants WHERE id=$1 AND source_points_granted_at IS NOT NULL),(SELECT count(*)FROM owned_progression_outbox WHERE grant_id=$1 AND packet_type='progression.points.granted'),(SELECT count(*)FROM wallet_v1.transactions WHERE idempotency_key=$2)`, grant, "progression:"+grant).Scan(&marked, &outbox, &transfer); e != nil || marked != 0 || outbox != 0 || transfer != 0 {
		t.Fatal("terminal wait leaked payout/marker/outbox/budget", marked, outbox, transfer, e)
	}
	if pgPointBalance(t, f.db, payer).String() != "100.000" || pgPointBalance(t, f.db, payee).Sign() != 0 {
		t.Fatal("terminal worker failure leaked treasury funds")
	}
	pgIdentityWaitUntil(t, f.db, f.ctx, expires)
	if n, e := newWorker().ProcessSourcePoints(f.ctx, 100); e != nil || n != 1 {
		t.Fatal("rolled-back payout consumed the entire daily budget", n, e)
	}
	if pgPointBalance(t, f.db, payer).String() != "75.000" || pgPointBalance(t, f.db, payee).String() != "25.000" {
		t.Fatal("post-expiry retry failed conservation")
	}
}
