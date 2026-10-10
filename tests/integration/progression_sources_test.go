// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/sphere/posts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

func progressionSourceID(t *testing.T, f postMediaFixture, kind, post string, newest bool) string {
	t.Helper()
	order := ""
	if newest {
		order = " ORDER BY occurred_at DESC,id DESC"
	}
	var id string
	if e := f.db.QueryRow(`SELECT id::text FROM owned_post_progression_sources WHERE account_id=$1 AND kind=$2 AND post_id=$3`+order+` LIMIT 1`, f.actor, kind, post).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestPGProgressionSourceSavedRestartPersistence(t *testing.T) {
	_ = database(t) // Skip ordinary unit runs before requiring a restart phase.
	type saved struct {
		Actor, Source, SourceState string
		Authority                  restartAuthoritySnapshot
		Database                   isolatedRestartDatabase
	}
	var state saved
	switch os.Getenv("PG_TEST_PHASE") {
	case "before":
		db, identity := createIsolatedRestartDatabase(t, "progression")
		f := newPostMediaFixtureInDatabase(t, db)
		f.config.Now = time.Now
		f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
		f = postSchedulingAuthority(t, f)
		// The sealed template carries schema only; restore this worker's reviewed
		// canonical definitions before recording the original pending source.
		query(t, db, progressionpg.Schema)
		category := authorityUUID(t)
		slug := "progression-restart-category-" + category
		query(t, f.db, `INSERT INTO owned_taxonomy.post_categories(id,slug,created_at,updated_at)VALUES($1,$2,clock_timestamp(),clock_timestamp())`, category, slug)
		tags, categories := []string{"progression-restart-tag-" + category}, []string{slug}
		p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Restart-persisted topical source"), Tags: &tags, Categories: &categories})
		if e != nil {
			t.Fatal(e)
		}
		state = saved{Actor: f.actor, Source: progressionSourceID(t, f, "post.create_topical", p.ID, false), Authority: saveAuthoritySnapshot(t, f.config), Database: identity}
		if e = db.QueryRow(`SELECT to_jsonb(s)::text FROM owned_post_progression_sources s WHERE id=$1`, state.Source).Scan(&state.SourceState); e != nil {
			t.Fatal(e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id=$2`, state.Actor, state.Source); n != 0 {
			t.Fatal("source projected before the process restart", n)
		}
		saveRestartFixture(t, "progression-sources.json", state)
	case "after":
		loadRestartFixture(t, "progression-sources.json", &state)
		db := openIsolatedRestartDatabase(t, state.Database)
		var original string
		if e := db.QueryRow(`SELECT to_jsonb(s)::text FROM owned_post_progression_sources s WHERE id=$1`, state.Source).Scan(&original); e != nil || original != state.SourceState {
			t.Fatal("original source changed across restart", e)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id=$2`, state.Actor, state.Source); n != 0 {
			t.Fatal("isolated pending source was consumed before restart", n)
		}
		store := authoritypg.New(db)
		verifier, e := authority.NewVerifier(store, restoreAuthoritySnapshot(t, state.Authority))
		if e != nil {
			t.Fatal(e)
		}
		r := progressionpg.NewWithAuthority(db, authoritypg.NewTransactionGuard(store, verifier))
		processProgressionSource(t, r, func() bool {
			return commerceCount(t, db, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id=$2`, state.Actor, state.Source) == 1
		})
		if _, e := r.ProcessDue(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id=$2`, state.Actor, state.Source); n != 1 {
			t.Fatal("restart replay duplicated the exact source fact", n)
		}
	default:
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
}

func processProgressionSource(t *testing.T, r *progressionpg.Repository, dbQuery func() bool) {
	t.Helper()
	for i := 0; i < 20 && !dbQuery(); i++ {
		if _, e := r.ProcessDue(context.Background(), 1000); e != nil {
			t.Fatal(e)
		}
	}
	if !dbQuery() {
		t.Fatal("canonical progression source was not projected")
	}
}

func TestPGProgressionPostSourcesBusinessReplayAndDisabled(t *testing.T) {
	f := newPostSchedulingFixture(t)
	category := authorityUUID(t)
	categorySlug := "progression-category-" + category
	query(t, f.db, `INSERT INTO owned_taxonomy.post_categories(id,slug,created_at,updated_at)VALUES($1,$2,clock_timestamp(),clock_timestamp())`, category, categorySlug)
	tags, categories := []string{"progression-tag-" + category}, []string{categorySlug}
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Canonical topical source"), Tags: &tags, Categories: &categories})
	if e != nil {
		t.Fatal(e)
	}
	topical := progressionSourceID(t, f, "post.create_topical", p.ID, false)
	boost, e := f.posts.BoostPost(f.ctx, f.actor, p.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	if boost.ID != progressionSourceID(t, f, "post.boost", p.ID, false) {
		t.Fatal("boost source is not bound to canonical boost identity")
	}
	otherPublisher, e := accountpg.NewForCompatibilityTests(f.db).CreatePublisher(context.Background(), f.other, accounts.PublisherPatch{Name: postString("progression-target-" + authorityUUID(t)), Nick: postString("Progression target")})
	if e != nil {
		t.Fatal(e)
	}
	target, e := f.posts.Create(f.otherCtx, f.other, otherPublisher.ID, posts.Patch{Content: postString("Cross publisher boost target")})
	if e != nil {
		t.Fatal(e)
	}
	crossBoost, e := f.posts.BoostPost(f.ctx, f.actor, target.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	var targetPublisher, actionPublisher string
	if e = f.db.QueryRow(`SELECT target_publisher_id::text,action_publisher_id::text FROM owned_post_progression_sources WHERE id=$1`, crossBoost.ID).Scan(&targetPublisher, &actionPublisher); e != nil || targetPublisher != otherPublisher.ID || actionPublisher != f.pub.ID {
		t.Fatal("cross-publisher boost fact lost target/origin", targetPublisher, actionPublisher, e)
	}
	if e = f.posts.UnboostPost(f.ctx, f.actor, target.ID); e != nil {
		t.Fatal(e)
	}
	scheduled := scheduledPost(t, f, posts.Patch{Tags: &tags, Categories: &categories})
	var premature int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.create_topical' AND post_id=$1`, scheduled.ID).Scan(&premature); e != nil || premature != 0 {
		t.Fatal("scheduled draft produced progression early", premature, e)
	}
	scheduleDue(t, f.db, scheduled)
	processSchedule(t, f)
	scheduledTopical := progressionSourceID(t, f, "post.create_topical", scheduled.ID, false)
	late, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Published without topics")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, late.ID, posts.Patch{Tags: &tags, Categories: &categories}); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.create_topical' AND post_id=$1`, late.ID).Scan(&premature); e != nil || premature != 0 {
		t.Fatal("topics added after publication became a create action", premature, e)
	}
	if e = f.posts.UnboostPost(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.posts.Delete(f.ctx, f.actor, scheduled.ID); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_taxonomy.post_tags SET deleted_at=clock_timestamp() WHERE slug=$1`, tags[0])
	query(t, f.db, `UPDATE owned_taxonomy.post_categories SET deleted_at=clock_timestamp() WHERE id=$1`, category)
	if _, e = f.db.Exec(`UPDATE owned_post_progression_sources SET occurred_at=occurred_at WHERE id=$1`, topical); e == nil {
		t.Fatal("post source immutability trigger accepted an update")
	}
	// The producer and projector share no memory; reopening the projector proves
	// the committed receipt is sufficient after a process restart.
	r := progressionpg.NewWithAuthority(f.db, f.guard)
	if e = r.RecordCanonicalEvent(f.ctx, f.actor, "post.boost", authorityUUID(t)); !errors.Is(e, progression.ErrNotFound) {
		t.Fatal("client-selected source accepted", e)
	}
	processProgressionSource(t, r, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND kind IN('post.boost','post.create_topical')`, f.actor).Scan(&n)
		return n == 4
	})
	if _, e = r.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	var events int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id IN($2::uuid,$3::uuid,$4::uuid,$5::uuid)`, f.actor, topical, boost.ID, scheduledTopical, crossBoost.ID).Scan(&events); e != nil || events != 4 {
		t.Fatal("source replay duplicated or lost events", events, e)
	}
	second, e := f.posts.BoostPost(f.ctx, f.actor, p.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	processProgressionSource(t, r, func() bool {
		var outcome string
		_ = f.db.QueryRow(`SELECT outcome FROM owned_progression_projection_receipts WHERE kind='post.boost' AND account_id=$1 AND source_id=$2`, f.actor, second.ID).Scan(&outcome)
		return outcome == "disabled"
	})
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND source_id=$2`, f.actor, second.ID).Scan(&events); e != nil || events != 0 {
		t.Fatal("disabled account received progression", events, e)
	}
}

func TestPGProgressionFeaturedUsesPreviousUTCEligibleTopFive(t *testing.T) {
	f := newPostSchedulingFixture(t)
	r := progressionpg.NewWithAuthority(f.db, f.guard)
	query(t, f.db, `SET TIME ZONE 'Pacific/Kiritimati'`)
	t.Cleanup(func() { _, _ = f.db.Exec(`SET TIME ZONE 'UTC'`) })
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(-24 * time.Hour)
	// Seed a durable watermark two days behind. Two independent workers must
	// claim the missed day and yesterday exactly once in UTC order.
	query(t, f.db, `INSERT INTO owned_post_featured_days(day,completed_at,candidate_count)VALUES($1::date,clock_timestamp(),0)`, start.Add(-48*time.Hour))
	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		id := authorityUUID(t)
		ids = append(ids, id)
		at := start.Add(time.Duration(i+1) * time.Hour)
		query(t, f.db, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at,awarded_score)VALUES($1,$2,$3,0,0,$4,$4,$4,$5)`, id, f.pub.ID, fmt.Sprintf("UTC featured %d", i), at, 100-i*10)
	}
	gatePublisher, gatePost := authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_publishers(id,name,nick,type,account_id,created_at,updated_at)VALUES($1,$2,'Gated progression',0,$3,clock_timestamp(),clock_timestamp())`, gatePublisher, "gated_progression_"+gatePublisher, f.other)
	query(t, f.db, `INSERT INTO owned_publisher_features(publisher_id,flag,actor_id,created_at,updated_at)VALUES($1,'postsRequireFollow',$2,clock_timestamp(),clock_timestamp())`, gatePublisher, f.other)
	query(t, f.db, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at,awarded_score)VALUES($1,$2,'Gated high score',0,0,$3,$3,$3,100000)`, gatePost, gatePublisher, start.Add(time.Hour))
	current := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_text_posts(id,publisher_id,content,visibility,type,published_at,created_at,updated_at,awarded_score)VALUES($1,$2,'Current UTC day',0,0,$3,$3,$3,100000)`, current, f.pub.ID, start.Add(25*time.Hour))
	otherDB := database(t)
	workers := []*progressionpg.Repository{r, progressionpg.NewWithAuthority(otherDB, f.guard)}
	errs := make(chan error, len(workers))
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Add(1)
		go func(worker *progressionpg.Repository) {
			defer wg.Done()
			_, err := worker.ProcessDue(context.Background(), 1000)
			errs <- err
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	processProgressionSource(t, r, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.featured' AND account_id=$1 AND post_id=ANY($2::uuid[])`, f.actor, ids[:5]).Scan(&n)
		return n == 5
	})
	processProgressionSource(t, r, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM owned_progression_events event JOIN owned_post_progression_sources source ON source.id=event.source_id WHERE event.account_id=$1 AND event.kind='post.featured' AND source.post_id=ANY($2::uuid[])`, f.actor, ids[:5]).Scan(&n)
		return n == 5
	})
	var selected, excluded int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.featured' AND account_id=$1 AND post_id=ANY($2::uuid[])`, f.actor, ids).Scan(&selected); e != nil {
		t.Fatal(e)
	}
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.featured' AND post_id IN($1,$2,$3)`, gatePost, ids[5], current).Scan(&excluded); e != nil {
		t.Fatal(e)
	}
	if selected != 5 || excluded != 0 {
		t.Fatal("featured window/eligibility/top-five mismatch", selected, excluded)
	}
	query(t, f.db, `UPDATE owned_text_posts SET awarded_score=1000000 WHERE id=$1`, ids[5])
	if _, e := r.ProcessDue(context.Background(), 1000); e != nil {
		t.Fatal(e)
	}
	var snapshotDays, reranked int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_post_featured_days WHERE day BETWEEN $1::date AND $2::date`, start.Add(-24*time.Hour), start).Scan(&snapshotDays); e != nil || snapshotDays != 2 {
		t.Fatal("missed UTC days were not backfilled exactly once", snapshotDays, e)
	}
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.featured' AND featured_day=$1::date`, start).Scan(&reranked); e != nil || reranked != 5 {
		t.Fatal("same-day rerank changed the immutable top five", reranked, e)
	}
}

func TestPGProgressionStellarFundedMonthsAndReplay(t *testing.T) {
	f := newCommerceFixture(t)
	var definition c.Definition
	var raw []byte
	if e := f.db.QueryRow(`SELECT document FROM commerce_v1.definitions WHERE identifier=$1`, f.plan).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal(raw, &definition); e != nil {
		t.Fatal(e)
	}
	definition.GroupIdentifier = "solian.stellar"
	definition.CycleDurationDays = 90
	raw, _ = json.Marshal(definition)
	query(t, f.db, `UPDATE commerce_v1.definitions SET document=$2 WHERE identifier=$1`, f.plan, raw)
	req := c.CreateSubscription{Identifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}
	if _, e := f.r.CreateSubscription(f.ctx, f.actor, req, "stellar-source", false); e != nil {
		t.Fatal(e)
	}
	o, e := f.r.SubscriptionOrder(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	paid, e := f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID)
	if e != nil || paid.Status != c.Finished {
		t.Fatal(paid, e)
	}
	var receipts int
	if e = f.db.QueryRow(`SELECT count(*) FROM commerce_v1.stellar_support_months WHERE order_id=$1`, o.ID).Scan(&receipts); e != nil || receipts != 3 {
		t.Fatal("credited month receipts", receipts, e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, o.ID, &f.wallet.ID); e != nil {
		t.Fatal("payment replay", e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM commerce_v1.stellar_support_months WHERE order_id=$1`, o.ID).Scan(&receipts); e != nil || receipts != 3 {
		t.Fatal("payment replay duplicated months", receipts, e)
	}
	if _, e = f.db.Exec(`UPDATE commerce_v1.stellar_support_months SET occurred_at=occurred_at WHERE order_id=$1`, o.ID); e == nil {
		t.Fatal("stellar source immutability trigger accepted an update")
	}
	// The standing internal-wallet authorization produces another immutable
	// funded cycle; replaying maintenance cannot duplicate it.
	subscription, e := f.r.Subscription(f.ctx, f.actor, f.plan)
	if e != nil {
		t.Fatal(e)
	}
	past := time.Unix(f.clock.Load(), 0).UTC().Add(-time.Second)
	subscription.EndedAt, subscription.RenewalAt = past, &past
	raw, _ = json.Marshal(subscription)
	query(t, f.db, `UPDATE commerce_v1.subscriptions SET ended_at=$2,renewal_at=$2,document=$3 WHERE id=$1`, subscription.ID, past, raw)
	if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM commerce_v1.stellar_support_months WHERE subscription_id=$1`, subscription.ID).Scan(&receipts); e != nil || receipts != 6 {
		t.Fatal("funded renewal did not credit the next three months", receipts, e)
	}
	if _, e = f.r.ProcessDue(f.ctx, 1000); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT count(*) FROM commerce_v1.stellar_support_months WHERE subscription_id=$1`, subscription.ID).Scan(&receipts); e != nil || receipts != 6 {
		t.Fatal("renewal replay duplicated months", receipts, e)
	}

	cloneDefinition := func(suffix string, mutate func(*c.Definition)) string {
		t.Helper()
		d := definition
		d.Identifier = f.plan + "-" + suffix
		mutate(&d)
		encoded, _ := json.Marshal(d)
		query(t, f.db, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id)VALUES($1,$2,$3)`, d.Identifier, encoded, f.payee.ID)
		return d.Identifier
	}
	trialPlan := cloneDefinition("trial", func(d *c.Definition) { d.FreeTrialDays = 7 })
	trial, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: trialPlan, PaymentMethod: "solian.wallet", IsFreeTrial: true, PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "stellar-trial", false)
	if e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.stellar_support_months WHERE subscription_id=$1`, trial.ID); n != 0 {
		t.Fatal("free trial minted funded months", n)
	}
	testingPlan := cloneDefinition("testing", func(*c.Definition) {})
	testingSub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: testingPlan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "stellar-testing", true)
	if e != nil {
		t.Fatal(e)
	}
	testingOrder, e := f.r.SubscriptionOrder(f.ctx, f.actor, testingPlan)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, testingOrder.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.stellar_support_months WHERE subscription_id=$1`, testingSub.ID); n != 0 {
		t.Fatal("testing subscription minted funded months", n)
	}
	appPlan := cloneDefinition("app", func(d *c.Definition) {
		v := "developer.app:" + f.app
		d.AppIdentifier = &v
	})
	appSub, e := f.r.CreateSubscription(f.ctx, f.actor, c.CreateSubscription{Identifier: appPlan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "stellar-app", false)
	if e != nil {
		t.Fatal(e)
	}
	appOrder, e := f.r.SubscriptionOrder(f.ctx, f.actor, appPlan)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, appOrder.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.stellar_support_months WHERE subscription_id=$1`, appSub.ID); n != 0 {
		t.Fatal("developer-app subscription minted internal Stellar months", n)
	}
	gift, e := f.r.PurchaseGift(f.ctx, f.actor, c.PurchaseGift{SubscriptionIdentifier: f.plan, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: "synthetic-commerce", PayerWalletID: &f.wallet.ID}}, "stellar-gift")
	if e != nil {
		t.Fatal(e)
	}
	giftOrder, e := f.r.GiftOrder(f.ctx, f.actor, gift.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, giftOrder.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.stellar_support_months WHERE order_id=$1`, giftOrder.ID); n != 0 {
		t.Fatal("gift order minted Stellar months", n)
	}
	r := progressionpg.NewWithAuthority(f.db, f.guard)
	processProgressionSource(t, r, func() bool {
		var n int
		_ = f.db.QueryRow(`SELECT count(*) FROM owned_progression_events WHERE account_id=$1 AND kind='stellar.support_month'`, f.actor).Scan(&n)
		return n == 6
	})
	if e = r.RecordCanonicalEvent(f.ctx, f.actor, "stellar.support_month", authorityUUID(t)); !errors.Is(e, progression.ErrNotFound) {
		t.Fatal("forged stellar source", e)
	}
}
