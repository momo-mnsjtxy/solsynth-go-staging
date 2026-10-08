// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/translation"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type syntheticTranslation func(context.Context, translation.Request) (string, error)

func (syntheticTranslation) Namespace() string { return "synthetic-translation-v1" }
func (f syntheticTranslation) Translate(ctx context.Context, r translation.Request) (string, error) {
	return f(ctx, r)
}

type translationFixture struct {
	atomicAuthorityFixture
	plan, subscription string
	repo               *translationpg.Repository
	calls              atomic.Int32
}

func newTranslationFixture(t *testing.T) *translationFixture {
	t.Helper()
	f := &translationFixture{atomicAuthorityFixture: newAtomicAuthorityFixture(t)}
	query(t, f.db, translationpg.Schema)
	f.plan = "synthetic-translation-" + f.actor
	d := commerce.Definition{Identifier: f.plan, GroupIdentifier: f.plan, DisplayName: "Synthetic translation eligibility", Currency: "synthetic-translation", BasePrice: walletAmount(t, "1.000"), CycleDurationDays: 2, AllowedPaymentMethods: []string{"solian.wallet"}, ProviderMappings: map[string][]string{}}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id)VALUES($1,$2,$3)`, f.plan, b, f.wallet.ID)
	f.subscription = f.grant(t, f.actor)
	f.repo = f.repository(t, syntheticTranslation(func(context.Context, translation.Request) (string, error) {
		f.calls.Add(1)
		return "Synthetic translated output", nil
	}), 1000)
	return f
}

// Seed only an isolated canonical entitlement fixture. This is not a purchase,
// settlement or a claim that a live provider granted paid access.
func (f *translationFixture) grant(t *testing.T, actor string) string {
	t.Helper()
	id := authorityUUID(t)
	now := f.config.Now()
	s := commerce.Subscription{ID: id, AccountID: actor, Identifier: f.plan, GroupIdentifier: f.plan, DisplayName: "Synthetic entitlement", Status: commerce.SubscriptionActive, IsActive: true, PaymentMethod: "synthetic-fixture", BegunAt: now.Add(-time.Minute), EndedAt: now.Add(48 * time.Hour), CreatedAt: now, UpdatedAt: now}
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO commerce_v1.subscriptions(id,account_id,identifier,status,begun_at,ended_at,cycle_days,auto_renew,document)VALUES($1,$2,$3,1,$4,$5,2,false,$6)`, id, actor, f.plan, s.BegunAt, s.EndedAt, b)
	return id
}
func (f *translationFixture) repository(t *testing.T, p translation.Provider, limit int) *translationpg.Repository {
	t.Helper()
	gate, e := translationpg.NewSystemSubscriptions([]string{f.plan})
	if e != nil {
		t.Fatal(e)
	}
	r, e := translationpg.New(f.db, f.guard, translationpg.Config{Provider: p, Entitlement: gate, Now: f.config.Now, MaxEntriesPerAccount: limit})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func translationRequest() translation.Request {
	return translation.Request{Text: "Synthetic original text", Target: "fr", Source: "en"}
}
func TestPGTranslationEntitlementCacheAndIsolation(t *testing.T) {
	f := newTranslationFixture(t)
	req := translationRequest()
	for i := 0; i < 2; i++ {
		v, e := f.repo.Translate(f.ctx, f.actor, req)
		if e != nil || v != "Synthetic translated output" {
			t.Fatal(v, e)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatal("cache miss", f.calls.Load())
	}
	other := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, other, "synthetic_translation_other_"+strings.ReplaceAll(other, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, f.issuer, other).Token
	proof, e := f.verifier.Authenticate(f.ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	bound := f.guard.Bind(f.ctx, proof)
	if v, e := f.repo.Translate(bound, other, req); !errors.Is(e, translation.ErrEntitlement) || v != "" || f.calls.Load() != 1 {
		t.Fatal("ineligible actor reached shared cache or provider", v, e)
	}
	f.grant(t, other)
	if _, e = f.repo.Translate(bound, other, req); e != nil || f.calls.Load() != 2 {
		t.Fatal("cross-account cache reused private output", e, f.calls.Load())
	}
	req.Source = "de"
	if _, e = f.repo.Translate(f.ctx, f.actor, req); e != nil || f.calls.Load() != 3 {
		t.Fatal("source language omitted from cache key", e)
	}
	if v, e := f.repo.Translate(context.Background(), f.actor, req); !errors.Is(e, authority.ErrInvalid) || v != "" {
		t.Fatal("actor ID substituted for signed proof", v, e)
	}
	query(t, f.db, `UPDATE owned_translation_cache SET created_at=$2,expires_at=$3 WHERE account_id=$1`, f.actor, f.config.Now().Add(-48*time.Hour), f.config.Now().Add(-time.Minute))
	if _, e = f.repo.Translate(f.ctx, f.actor, req); e != nil || f.calls.Load() != 4 {
		t.Fatal("expired translation not refreshed", e)
	}
}
func TestPGTranslationRejectsIneligibleCanonicalGrants(t *testing.T) {
	_ = database(t)
	for _, kind := range []string{"testing", "inactive", "future", "expired", "cancelled", "disabled_definition", "app_definition", "unmapped"} {
		t.Run(kind, func(t *testing.T) {
			f := newTranslationFixture(t)
			switch kind {
			case "testing":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{is_testing}','true')WHERE id=$1`, f.subscription)
			case "inactive":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET document=jsonb_set(document,'{is_active}','false')WHERE id=$1`, f.subscription)
			case "future":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET begun_at=$2 WHERE id=$1`, f.subscription, f.config.Now().Add(time.Hour))
			case "expired":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2 WHERE id=$1`, f.subscription, f.config.Now())
			case "cancelled":
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET status=2 WHERE id=$1`, f.subscription)
			case "disabled_definition":
				query(t, f.db, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1`, f.plan)
			case "app_definition":
				query(t, f.db, `UPDATE commerce_v1.definitions SET document=document||jsonb_build_object('app_identifier',$2::text)WHERE identifier=$1`, f.plan, authorityUUID(t))
			case "unmapped":
				f.plan = "synthetic-unmapped-plan"
				f.repo = f.repository(t, syntheticTranslation(func(context.Context, translation.Request) (string, error) {
					t.Error("unmapped plan reached provider")
					return "wrong", nil
				}), 1000)
			}
			if v, e := f.repo.Translate(f.ctx, f.actor, translationRequest()); !errors.Is(e, translation.ErrEntitlement) || v != "" || f.calls.Load() != 0 {
				t.Fatal("ineligible canonical grant", kind, v, e)
			}
		})
	}
}
func TestPGTranslationProviderWaitRechecksAuthorityAndEntitlement(t *testing.T) {
	_ = database(t)
	for _, mode := range []string{"revoked", "cancelled", "provider_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			p := syntheticTranslation(func(ctx context.Context, r translation.Request) (string, error) {
				close(entered)
				select {
				case <-release:
					if mode == "provider_failure" {
						return "", translation.ErrProvider
					}
					return "Synthetic completed output", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			})
			r := f.repository(t, p, 1000)
			go func() {
				value, e := r.Translate(f.ctx, f.actor, translationRequest())
				if e != nil && value != "" {
					e = errors.New("failed translation returned result")
				}
				done <- e
			}()
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal("provider not entered", f.ctx.Err())
			}
			check, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			_, e = check.ExecContext(f.ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, f.actor)
			_ = check.Rollback()
			if e != nil {
				t.Fatal("outbound work held authority lock", e)
			}
			want := error(translation.ErrProvider)
			if mode == "revoked" {
				if e = f.issuer.Revoke(f.ctx, f.token, "current"); e != nil {
					t.Fatal(e)
				}
				want = authority.ErrRevoked
			} else if mode == "cancelled" {
				query(t, f.db, `UPDATE commerce_v1.subscriptions SET status=2 WHERE id=$1`, f.subscription)
				want = translation.ErrEntitlement
			}
			close(release)
			if e = <-done; !errors.Is(e, want) {
				t.Fatal("late recheck", mode, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_translation_cache WHERE account_id=$1`, f.actor); n != 0 {
				t.Fatal("failed request cached output", n)
			}
		})
	}
}
func TestPGTranslationCacheCapacityRollbackAndConcurrency(t *testing.T) {
	f := newTranslationFixture(t)
	req := translationRequest()
	if _, e := f.repo.Translate(f.ctx, f.actor, req); e != nil {
		t.Fatal(e)
	}
	r := f.repository(t, syntheticTranslation(func(context.Context, translation.Request) (string, error) { return "Synthetic alternate output", nil }), 1)
	other := req
	other.Text = "Synthetic distinct text"
	if _, e := r.Translate(f.ctx, f.actor, other); !errors.Is(e, translation.ErrBusy) {
		t.Fatal("unbounded account cache", e)
	}
	query(t, f.db, `ALTER TABLE owned_translation_cache ADD CONSTRAINT translation_cache_failure CHECK(account_id<>'`+f.actor+`'::uuid)NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_translation_cache DROP CONSTRAINT IF EXISTS translation_cache_failure`)
	})
	_, e := f.repo.Translate(f.ctx, f.actor, other)
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "translation_cache_failure" {
		t.Fatal("wrong cache rollback failure", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_translation_cache WHERE account_id=$1`, f.actor); n != 1 {
		t.Fatal("failed fill changed cache", n)
	}
	query(t, f.db, `ALTER TABLE owned_translation_cache DROP CONSTRAINT translation_cache_failure`)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	done := make(chan error, 2)
	r = f.repository(t, syntheticTranslation(func(ctx context.Context, r translation.Request) (string, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return "Synthetic concurrent output", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}), 1000)
	for i := 0; i < 2; i++ {
		go func() { _, e := r.Translate(f.ctx, f.actor, other); done <- e }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-f.ctx.Done():
			t.Fatal("concurrent fetch did not enter", f.ctx.Err())
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_translation_cache WHERE account_id=$1`, f.actor); n != 2 {
		t.Fatal("same request duplicated cache entry", n)
	}
}
func TestPGTranslationFreshProofWhileAccountLocked(t *testing.T) {
	f := newTranslationFixture(t)
	block, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
	if _, e := block.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=$2,epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.Translate(f.ctx, f.actor, translationRequest()); done <- e }()
	waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
	if e := block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrRevoked) || f.calls.Load() != 0 {
		t.Fatal("revoked waiter reached provider", e, f.calls.Load())
	}
}
func TestPGTranslationRestartPersistence(t *testing.T) {
	const name = "synthetic_translation_restart"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var actor, plan string
		var created time.Time
		if e := db.QueryRow(`SELECT a.id::text,s.identifier,c.created_at FROM owned_authority_accounts a JOIN commerce_v1.subscriptions s ON s.account_id=a.id JOIN owned_translation_cache c ON c.account_id=a.id WHERE a.name=$1`, name).Scan(&actor, &plan, &created); e != nil {
			t.Fatal(e)
		}
		clock := &atomic.Int64{}
		clock.Store(created.Unix())
		cfg := authorityConfig(t, authorityKeys(t), clock)
		store := authoritypg.New(db)
		issuer, e := authority.New(store, cfg)
		if e != nil {
			t.Fatal(e)
		}
		token := pgIssue(t, issuer, actor).Token
		verifier, e := authority.NewVerifier(store, cfg)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := verifier.Authenticate(context.Background(), token)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(store, verifier)
		gate, e := translationpg.NewSystemSubscriptions([]string{plan})
		if e != nil {
			t.Fatal(e)
		}
		r, e := translationpg.New(db, guard, translationpg.Config{Entitlement: gate, Now: cfg.Now, Provider: syntheticTranslation(func(context.Context, translation.Request) (string, error) {
			t.Error("restart cache hit reached provider")
			return "", translation.ErrProvider
		})})
		if e != nil {
			t.Fatal(e)
		}
		v, e := r.Translate(guard.Bind(context.Background(), proof), actor, translationRequest())
		if e != nil || v != "Synthetic translated output" {
			t.Fatal(v, e)
		}
		return
	}
	f := newTranslationFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, f.actor, name)
	if _, e := f.repo.Translate(f.ctx, f.actor, translationRequest()); e != nil {
		t.Fatal(e)
	}
}
