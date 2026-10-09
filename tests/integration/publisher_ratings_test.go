// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	rating "solsynth.local/go-migration/ports/sphere/publisherrating"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	"strings"
	"sync"
	"testing"
	"time"
)

type ratingFixture struct {
	*realmPublishingFixture
	ratings *publisherratingpg.Repository
	day     time.Time
}

func newRatingFixture(t *testing.T) *ratingFixture {
	t.Helper()
	f := &ratingFixture{realmPublishingFixture: newRealmPublishingFixture(t)}
	query(t, f.db, publisherratingpg.Schema)
	f.day = f.config.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	var e error
	f.ratings, e = publisherratingpg.New(f.db, f.social, publisherratingpg.Config{ReactionOnly: true, EarliestDay: f.day, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.ratings.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *ratingFixture) dated(t *testing.T, pub accounts.Publisher, body string, visibility int) posts.Post {
	p := f.post(t, pub, body, visibility)
	query(t, f.db, `UPDATE owned_text_posts SET created_at=$2::timestamptz WHERE id=$1::uuid`, p.ID, f.day.Add(12*time.Hour))
	return p
}
func (f *ratingFixture) react(t *testing.T, p posts.Post, actor string, ctx context.Context, symbol string, attitude int) {
	t.Helper()
	if _, _, e := f.interactions.React(ctx, actor, p.ID, interactions.ReactionRequest{Symbol: symbol, Attitude: attitude}); e != nil {
		t.Fatal(e)
	}
}
func TestPGPublisherRatingProducerCountsPrivacyAndReplay(t *testing.T) {
	f := newRatingFixture(t)
	p := f.dated(t, f.individual, "Synthetic rating canonical activity", posts.Public)
	f.react(t, p, f.actor, f.ctx, "thumb_up", interactions.Positive)
	f.react(t, p, f.other, f.otherCtx, "heart", interactions.Positive)
	f.react(t, p, f.actor, f.ctx, "thumb_down", interactions.Negative)
	f.react(t, p, f.actor, f.ctx, "confuse", interactions.Neutral)
	f.react(t, p, f.outside, f.outsideCtx, "thumb_up", interactions.Positive)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.outside)
	private := f.dated(t, f.individual, "SYNTHETIC_PRIVATE_RATING_INPUT", posts.Private)
	f.react(t, private, f.actor, f.ctx, "heart", interactions.Positive)
	linked := f.dated(t, f.individual, "Synthetic realm rating input", posts.Public)
	if _, e := f.posts.Update(f.ctx, f.actor, linked.ID, posts.Patch{RealmID: &f.private.ID}); e != nil {
		t.Fatal(e)
	}
	f.react(t, linked, f.actor, f.ctx, "heart", interactions.Positive)
	processed, e := f.ratings.ProcessPublisherDay(f.ctx, f.individual.ID, f.day)
	if e != nil || processed != 1 {
		t.Fatal(processed, e)
	}
	again, e := f.ratings.ProcessPublisherDay(f.ctx, f.individual.ID, f.day)
	if e != nil || again != 0 {
		t.Fatal("daily reward duplicated", again, e)
	}
	overview, e := f.ratings.Overview(f.ctx, "", f.individual.Name)
	if e != nil || overview.Rating != 101 || overview.Policy != rating.Policy || overview.AwardScoreIncluded {
		t.Fatal(overview, e)
	}
	history, n, e := f.ratings.History(f.ctx, f.actor, f.individual.ID, s.Page{})
	if e != nil || n != 1 || len(history.Records) != 1 || history.Records[0].Delta != 1 || !history.IsMember {
		t.Fatal(history, n, e)
	}
	var positive, negative int
	if e = f.db.QueryRow(`SELECT positive_count,negative_count FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid AND period_start=$2::timestamptz`, f.individual.ID, f.day).Scan(&positive, &negative); e != nil || positive != 2 || negative != 1 {
		t.Fatal(positive, negative, e)
	}
	disabled, e := publisherratingpg.New(f.db, f.social, publisherratingpg.Config{Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = disabled.ProcessYesterday(f.ctx, 10); !errors.Is(e, s.ErrUnavailable) {
		t.Fatal("unconfigured producer ran", e)
	}
}
func TestPGPublisherRatingRanksAliasesAndCurrentPrivacy(t *testing.T) {
	f := newRatingFixture(t)
	a := f.dated(t, f.individual, "Synthetic first tied publisher", posts.Public)
	b := f.dated(t, f.publicPub, "Synthetic second tied publisher", posts.Public)
	f.react(t, a, f.actor, f.ctx, "heart", interactions.Positive)
	f.react(t, b, f.actor, f.ctx, "heart", interactions.Positive)
	for _, pub := range []accounts.Publisher{f.individual, f.publicPub} {
		if _, e := f.ratings.ProcessPublisherDay(f.ctx, pub.ID, f.day); e != nil {
			t.Fatal(e)
		}
	}
	first, e := f.ratings.Overview(f.ctx, "", f.individual.ID)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.ratings.Overview(f.ctx, "", f.publicPub.ID)
	if e != nil || first.Rating != second.Rating || first.Rank != second.Rank || first.Percentile != second.Percentile {
		t.Fatal(first, second, e)
	}
	alias := "synthetic-rating-alias-" + f.actor
	query(t, f.db, `INSERT INTO owned_publisher_name_reservations(folded_name,name,publisher_id,created_at)VALUES($1::text,$1::text,$2::uuid,$3::timestamptz)`, alias, f.individual.ID, f.config.Now())
	byAlias, e := f.ratings.Overview(f.ctx, "", alias)
	if e != nil || byAlias != first {
		t.Fatal("canonical alias lookup diverged", byAlias, first, e)
	}
	if _, e = f.ratings.Overview(f.ctx, "", f.privatePub.ID); !errors.Is(e, s.ErrNotFound) {
		t.Fatal("private organization rating leaked", e)
	}
	query(t, f.db, `UPDATE owned_realms SET is_public=false WHERE id=$1::uuid`, f.public.ID)
	if _, e = f.ratings.Overview(f.outsideCtx, f.outside, f.publicPub.ID); !errors.Is(e, s.ErrNotFound) {
		t.Fatal("revoked realm visibility ignored", e)
	}
	if _, e = f.ratings.Overview(f.ctx, f.actor, f.publicPub.ID); e != nil {
		t.Fatal("realm member lost own visibility", e)
	}
	fixturePublisherShadowban(t, f.db, f.individual.ID, "spam", f.config.Now())
	if _, e = f.ratings.Overview(f.ctx, "", alias); !errors.Is(e, s.ErrNotFound) {
		t.Fatal("alias bypassed publisher moderation", e)
	}
}
func TestPGPublisherRatingBatchRollbackAndConcurrency(t *testing.T) {
	f := newRatingFixture(t)
	p := f.dated(t, f.individual, "Synthetic rating rollback", posts.Public)
	f.react(t, p, f.actor, f.ctx, "thumb_up", interactions.Positive)
	name := "synthetic_rating_rollback_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_publisher_rating_records ADD CONSTRAINT `+name+` CHECK(publisher_id<>'`+f.individual.ID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		_, _ = f.db.Exec(`ALTER TABLE owned_publisher_rating_records DROP CONSTRAINT IF EXISTS ` + name)
	})
	_, e := f.ratings.ProcessPublisherDay(f.ctx, f.individual.ID, f.day)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != name {
		t.Fatal("exact rating rollback constraint", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid`, f.individual.ID); n != 0 {
		t.Fatal(n)
	}
	query(t, f.db, `ALTER TABLE owned_publisher_rating_records DROP CONSTRAINT `+name)
	var wg sync.WaitGroup
	type result struct {
		n int
		e error
	}
	results := make(chan result, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := f.ratings.ProcessPublisherDay(f.ctx, f.individual.ID, f.day)
			results <- result{n, e}
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for r := range results {
		if r.e != nil {
			t.Fatal(r.e)
		}
		count += r.n
	}
	if count != 1 {
		t.Fatal("concurrent producer double reward", count)
	}
	v, e := f.ratings.Rating(f.ctx, "", f.individual.ID)
	if e != nil || v != 101 {
		t.Fatal(v, e)
	}
	if _, e = f.ratings.ProcessDay(f.ctx, f.day.AddDate(0, 0, 1), 1); !errors.Is(e, s.ErrInvalid) {
		t.Fatal("unfinished day accepted", e)
	}
	if _, e = f.ratings.ProcessDay(f.ctx, f.day, 1001); !errors.Is(e, s.ErrInvalid) {
		t.Fatal("unbounded batch", e)
	}
}
func TestPGPublisherRatingRestartPersistence(t *testing.T) {
	const marker = "Synthetic durable reaction-only publisher rating"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var publisher string
		if e := db.QueryRow(`SELECT publisher_id::text FROM owned_text_posts WHERE content=$1::text`, marker).Scan(&publisher); e != nil {
			t.Fatal(e)
		}
		r, e := publisherratingpg.New(db, socialpg.New(db), publisherratingpg.Config{})
		if e != nil {
			t.Fatal(e)
		}
		v, e := r.Overview(context.Background(), "", publisher)
		if e != nil || v.Rating != 101 || v.Policy != rating.Policy || v.AwardScoreIncluded {
			t.Fatal(v, e)
		}
		if n := commerceCount(t, db, `SELECT count(*) FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid`, publisher); n != 1 {
			t.Fatal(n)
		}
		return
	}
	f := newRatingFixture(t)
	p := f.dated(t, f.individual, marker, posts.Public)
	f.react(t, p, f.actor, f.ctx, "thumb_up", interactions.Positive)
	if n, e := f.ratings.ProcessPublisherDay(f.ctx, f.individual.ID, f.day); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
