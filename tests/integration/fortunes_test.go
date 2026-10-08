// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fortuneFixture struct {
	presenceProgressionFixture
	reports  *fortunepg.Repository
	now      *atomic.Int64
	config   fortunepg.Config
	preserve *bool
}
type fortuneProvider struct {
	revision string
	fn       func(context.Context, fortunes.Request) (fortunes.Generation, error)
}

func (p fortuneProvider) Revision() string { return p.revision }
func (p fortuneProvider) Generate(ctx context.Context, r fortunes.Request) (fortunes.Generation, error) {
	return p.fn(ctx, r)
}

// Independently authored synthetic fixture content; never a runtime fallback.
func fortuneGeneration(label string) fortunes.Generation {
	return fortunes.Generation{Report: fortunes.Report{Version: 2, Poem: "Synthetic poem " + label, Summary: "Synthetic summary", SummaryDetail: "Synthetic detail", Wish: "Synthetic wish", Love: "Synthetic love", Study: "Synthetic study", Career: "Synthetic career", Health: "Synthetic health", LostItem: "Synthetic lost item", LuckyColor: "Synthetic color", LuckyDirection: "Synthetic direction", LuckyTime: "Synthetic time", LuckyItem: "Synthetic item", LuckyAction: "Synthetic action", AvoidAction: "Synthetic avoidance", Ritual: "Synthetic ritual"}, Tips: []fortunes.Tip{{IsPositive: true, Title: "Synthetic A", Content: "Synthetic text A"}, {IsPositive: false, Title: "Synthetic B", Content: "Synthetic text B"}}}
}
func fortuneCorpus(revision string) fortunes.Corpus {
	v := fortunes.Corpus{Revision: revision, Label: "Owned synthetic corpus", Provenance: "Authored independently for PostgreSQL integration tests", Sayings: []fortunes.Saying{{Content: "Owned synthetic saying", Source: "Synthetic fixture", Language: "en"}, {Content: "Owned synthetic second saying", Source: "Synthetic fixture", Language: "zh"}}}
	for level := 0; level < 5; level++ {
		v.Reports = append(v.Reports, fortunes.Template{ID: fmt.Sprintf("fixture-%d", level), Language: "en", Level: level, Generation: fortuneGeneration(fmt.Sprint(level))})
	}
	return v
}
func newFortuneFixture(t *testing.T, p fortunes.Provider) fortuneFixture {
	t.Helper()
	f := fortuneFixture{presenceProgressionFixture: newPresenceProgressionFixture(t), now: &atomic.Int64{}, preserve: new(bool)}
	f.now.Store(time.Now().UTC().Unix() + 1)
	f.config = fortunepg.Config{Now: func() time.Time { return time.Unix(f.now.Load(), 0).UTC() }, Provider: p, Timeout: time.Second, MaxAttempts: 2}
	query(t, f.db, `UPDATE owned_authority_accounts SET language='en' WHERE id=$1::uuid`, f.actor)
	var e error
	f.reports, e = fortunepg.New(f.db, f.guard, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.reports.Ready(f.ctx); e != nil {
		t.Fatal("guarded runner must install fortunepg.Schema and canonical dependencies", e)
	}
	f.progression = progressionpg.NewWithCheckInReports(f.db, f.guard, f.reports)
	t.Cleanup(func() {
		if *f.preserve {
			return
		}
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM owned_fortune_requests f USING owned_progression_checkins c WHERE c.id=f.checkin_id AND c.account_id=$1::uuid`, f.actor)
	})
	return f
}
func fortuneCheckIn(t *testing.T, f fortuneFixture) progression.CheckIn {
	t.Helper()
	v, e := f.progression.CheckIn(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	if f.now.Load() <= v.CreatedAt.Unix() {
		f.now.Store(v.CreatedAt.Unix() + 2)
	}
	return v
}
func fortuneStep(t *testing.T, f fortuneFixture) {
	t.Helper()
	ok, e := f.reports.ProcessOne(f.ctx)
	if e != nil || !ok {
		t.Fatal("worker step", ok, e)
	}
}
func fortuneView(t *testing.T, f fortuneFixture, id string) fortunes.Projection {
	t.Helper()
	v, e := f.reports.Read(f.ctx, f.actor, id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGFortuneConfiguredCatalogueAndDailyIsolation(t *testing.T) {
	f := newFortuneFixture(t, nil)
	query(t, f.db, `DELETE FROM owned_fortune_config`)
	v := fortuneCheckIn(t, f)
	before := fortuneView(t, f, v.ID)
	if before.Generation != nil || before.Status.State != "unavailable" {
		t.Fatal(before)
	}
	if _, e := f.reports.ProcessOne(f.ctx); !errors.Is(e, fortunes.ErrUnavailable) {
		t.Fatal("missing configuration created content", e)
	}
	corpus := fortuneCorpus("fixture-" + f.actor)
	if e := f.reports.InstallCorpus(f.ctx, corpus); e != nil {
		t.Fatal(e)
	}
	if e := f.reports.InstallCorpus(f.ctx, corpus); e != nil {
		t.Fatal("config replay", e)
	}
	changed := corpus
	changed.Label = "Changed synthetic provenance"
	if e := f.reports.InstallCorpus(f.ctx, changed); !errors.Is(e, fortunes.ErrConflict) {
		t.Fatal("mutable corpus revision", e)
	}
	h := (&fortunes.Service{Repository: f.reports, Authority: f.verifier}).Handler()
	request := httptest.NewRequest("GET", "/api/fortune/random?language=EN", nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Owned synthetic saying") {
		t.Fatal(w.Code, w.Body.String())
	}
	fortuneStep(t, f)
	after := fortuneView(t, f, v.ID)
	if after.Generation == nil || after.Status.State != "ready" || after.Status.Attempts != 1 {
		t.Fatal(after)
	}
	again := fortuneCheckIn(t, f)
	if again.ID != v.ID || again.FortuneReport == nil {
		t.Fatal(again)
	}
	poll := (&progression.Service{Repository: f.progression, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	for _, item := range []struct {
		query  string
		status int
	}{{"version=2", 200}, {"version=3", 400}, {"version=1&version=2", 400}, {"account_id=other", 400}} {
		request := httptest.NewRequest("GET", "/api/accounts/me/check-in?"+item.query, nil)
		request.Header.Set("Authorization", "Bearer "+f.token)
		response := httptest.NewRecorder()
		poll.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatal(item.query, response.Code, response.Body.String())
		}
		if item.status == 200 && !strings.Contains(response.Body.String(), after.Generation.Report.Poem) {
			t.Fatal("today polling lacks report", response.Body.String())
		}
	}
	if n := ppScalar(t, f.presenceProgressionFixture, `SELECT count(*) FROM owned_progression_experience WHERE account_id=$1::uuid AND reward_token=$2::text`, f.actor, "check-in:"+f.actor+":"+v.CreatedAt.UTC().Format("2006-01-02")); n != 1 {
		t.Fatal("duplicate XP", n)
	}
	other := newFortuneFixture(t, nil)
	if _, e := other.reports.Read(other.ctx, other.actor, v.ID); !errors.Is(e, fortunes.ErrNotFound) {
		t.Fatal("cross-account report", e)
	}
	// Completed reports survive active corpus changes, and client calendar polling
	// receives the same report through a read-only canonical activity decorator.
	c2 := fortuneCorpus("new-" + f.actor)
	c2.Reports[0].Generation.Report.Poem = "Different owned fixture"
	if e := f.reports.InstallCorpus(f.ctx, c2); e != nil {
		t.Fatal(e)
	}
	if got := fortuneView(t, f, v.ID); got.Generation.Report != after.Generation.Report {
		t.Fatal("completed result changed")
	}
	calendar, e := notabledaypg.New(f.db, f.guard, notabledaypg.Config{Activity: fortunepg.CalendarOverlay{Base: notabledaypg.CanonicalActivityOverlay{}, Reports: f.reports}})
	if e != nil {
		t.Fatal(e)
	}
	if e = calendar.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	calendarHandler := (&notabledays.Service{Repository: calendar, Authority: f.verifier}).Handler()
	request = httptest.NewRequest("GET", fmt.Sprintf("/api/accounts/me/calendar?year=%d&month=%d", v.CreatedAt.Year(), v.CreatedAt.Month()), nil)
	request.Header.Set("Authorization", "Bearer "+f.token)
	w = httptest.NewRecorder()
	calendarHandler.ServeHTTP(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), after.Generation.Report.Poem) {
		t.Fatal("calendar polling lacks completed report", w.Code, w.Body.String())
	}
	history, _, e := f.progression.CheckInHistory(f.ctx, f.actor, 0, 10)
	if e != nil || len(history) != 1 || history[0].FortuneReport == nil {
		t.Fatal(history, e)
	}
}
func TestPGFortuneEnqueueAtomicityAndFreshAuthority(t *testing.T) {
	p, _ := fortunes.NewCorpusProvider(fortuneCorpus("synthetic-atomic"))
	f := newFortuneFixture(t, p)
	query(t, f.db, `CREATE FUNCTION synthetic_fortune_enqueue_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.request_data->>'account_id'=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic fortune enqueue failure'; END IF; RETURN NEW; END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_fortune_enqueue_fail BEFORE INSERT ON owned_fortune_requests FOR EACH ROW EXECUTE FUNCTION synthetic_fortune_enqueue_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_fortune_enqueue_fail ON owned_fortune_requests;DROP FUNCTION IF EXISTS synthetic_fortune_enqueue_fail()`)
	})
	if _, e := f.progression.CheckIn(f.ctx, f.actor); e == nil {
		t.Fatal("enqueue failure hidden")
	}
	for _, table := range []string{"owned_progression_checkins", "owned_progression_experience"} {
		if n := ppScalar(t, f.presenceProgressionFixture, "SELECT count(*) FROM "+table+" WHERE account_id=$1::uuid", f.actor); n != 0 {
			t.Fatal("partial check-in/XP", table, n)
		}
	}
	query(t, f.db, `DROP TRIGGER synthetic_fortune_enqueue_fail ON owned_fortune_requests;DROP FUNCTION synthetic_fortune_enqueue_fail()`)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.progression.CheckIn(ctx, f.actor); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("stale disabled account proof accepted")
	}
	if n := ppScalar(t, f.presenceProgressionFixture, `SELECT count(*) FROM owned_progression_checkins WHERE account_id=$1::uuid`, f.actor); n != 0 {
		t.Fatal(n)
	}
}
func TestPGFortuneRetryLeaseRecoveryAndImmutableCompletion(t *testing.T) {
	var calls atomic.Int64
	provider := fortuneProvider{"synthetic.retry", func(_ context.Context, r fortunes.Request) (fortunes.Generation, error) {
		n := calls.Add(1)
		if n == 1 {
			return fortunes.Generation{}, errors.New("synthetic provider failure")
		}
		if n == 2 {
			v := fortuneGeneration("invalid version")
			v.Report.Version = 1
			return v, nil
		}
		return fortuneGeneration("recovered"), nil
	}}
	f := newFortuneFixture(t, provider)
	v := fortuneCheckIn(t, f)
	fortuneStep(t, f)
	if got := fortuneView(t, f, v.ID); got.Status.State != "pending" || got.Generation != nil {
		t.Fatal(got)
	}
	f.now.Add(31)
	fortuneStep(t, f)
	if got := fortuneView(t, f, v.ID); got.Status.State != "failed" || got.Status.Attempts != 2 {
		t.Fatal(got)
	}
	if e := f.reports.Retry(f.ctx, f.actor, v.ID); e != nil {
		t.Fatal(e)
	}
	fortuneStep(t, f)
	ready := fortuneView(t, f, v.ID)
	if ready.Generation == nil || ready.Generation.Report.Poem != "Synthetic poem recovered" {
		t.Fatal(ready)
	}
	if e := f.reports.Retry(f.ctx, f.actor, v.ID); !errors.Is(e, fortunes.ErrConflict) {
		t.Fatal("ready result reset", e)
	}
	// A process can die after claiming; an expired lease is reclaimed durably.
	g := newFortuneFixture(t, provider)
	queued := fortuneCheckIn(t, g)
	query(t, g.db, `UPDATE owned_fortune_requests SET state='processing',provider_revision='synthetic.retry',attempts=1,lease_id=$2::uuid,lease_until=$3::timestamptz WHERE checkin_id=$1::uuid`, queued.ID, authorityUUID(t), time.Unix(g.now.Load()-1, 0))
	fortuneStep(t, g)
	if got := fortuneView(t, g, queued.ID); got.Status.State != "ready" || got.Status.Attempts != 2 {
		t.Fatal(got)
	}
}
func TestPGFortuneWorkerRevocationAndStaleLease(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	provider := fortuneProvider{"synthetic.blocking", func(context.Context, fortunes.Request) (fortunes.Generation, error) {
		close(entered)
		<-release
		return fortuneGeneration("revoked"), nil
	}}
	f := newFortuneFixture(t, provider)
	v := fortuneCheckIn(t, f)
	done := make(chan error, 1)
	go func() { _, e := f.reports.ProcessOne(f.ctx); done <- e }()
	<-entered
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	var state string
	var payload []byte
	if e := f.db.QueryRow(`SELECT state,generation FROM owned_fortune_requests WHERE checkin_id=$1::uuid`, v.ID).Scan(&state, &payload); e != nil || state != "cancelled" || len(payload) != 0 {
		t.Fatal(state, len(payload), e)
	}
	entered = make(chan struct{})
	release = make(chan struct{})
	var calls atomic.Int64
	provider = fortuneProvider{"synthetic.stale", func(context.Context, fortunes.Request) (fortunes.Generation, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return fortuneGeneration("stale"), nil
		}
		return fortuneGeneration("current"), nil
	}}
	g := newFortuneFixture(t, provider)
	queued := fortuneCheckIn(t, g)
	go func() { _, e := g.reports.ProcessOne(g.ctx); done <- e }()
	<-entered
	g.now.Add(32)
	fortuneStep(t, g)
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if got := fortuneView(t, g, queued.ID); got.Generation == nil || got.Generation.Report.Poem != "Synthetic poem current" {
		t.Fatal("late result overwrote current lease", got)
	}
}
func TestPGFortuneConcurrentCheckInAndProfileSnapshot(t *testing.T) {
	requests := make(chan fortunes.Request, 1)
	provider := fortuneProvider{"synthetic.snapshot", func(_ context.Context, r fortunes.Request) (fortunes.Generation, error) {
		requests <- r
		return fortuneGeneration("snapshot"), nil
	}}
	f := newFortuneFixture(t, provider)
	query(t, f.db, `UPDATE owned_accounts_profiles SET time_zone='Pacific/Kiritimati' WHERE account_id=$1::uuid`, f.actor)
	otherDB := database(t)
	// Reuse the verifier's proof with a new guard bound to the second pool.
	otherGuard := authoritypg.NewTransactionGuard(authoritypg.New(otherDB), f.verifier)
	otherCtx := otherGuard.Bind(f.ctx, f.principal)
	otherReports, e := fortunepg.New(otherDB, otherGuard, f.config)
	if e != nil {
		t.Fatal(e)
	}
	otherProgression := progressionpg.NewWithCheckInReports(otherDB, otherGuard, otherReports)
	type result struct {
		v progression.CheckIn
		e error
	}
	start := make(chan struct{})
	out := make(chan result, 2)
	go func() { <-start; v, e := f.progression.CheckIn(f.ctx, f.actor); out <- result{v, e} }()
	go func() { <-start; v, e := otherProgression.CheckIn(otherCtx, f.actor); out <- result{v, e} }()
	close(start)
	var got progression.CheckIn
	for i := 0; i < 2; i++ {
		r := <-out
		if r.e != nil {
			t.Fatal(r.e)
		}
		if got.ID != "" && got.ID != r.v.ID {
			t.Fatal("duplicate daily row")
		}
		got = r.v
	}
	if f.now.Load() <= got.CreatedAt.Unix() {
		f.now.Store(got.CreatedAt.Unix() + 2)
	}
	query(t, f.db, `UPDATE owned_accounts_profiles SET time_zone='UTC' WHERE account_id=$1::uuid`, f.actor)
	query(t, f.db, `UPDATE owned_authority_accounts SET language='zh' WHERE id=$1::uuid`, f.actor)
	fortuneStep(t, f)
	request := <-requests
	zone, _ := time.LoadLocation("Pacific/Kiritimati")
	if request.Language != "en" || request.TimeZone != "Pacific/Kiritimati" || request.LocalDate != got.CreatedAt.In(zone).Format("2006-01-02") {
		t.Fatal("request snapshot drifted", request)
	}
	if n := ppScalar(t, f.presenceProgressionFixture, `SELECT count(*) FROM owned_fortune_requests WHERE checkin_id=$1::uuid`, got.ID); n != 1 {
		t.Fatal(n)
	}
}
func TestPGFortuneRestartPersistence(t *testing.T) {
	_ = database(t) // Enforce the disposable database guard before phase/file access.
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "fortune-record.json")
	type saved struct{ Actor, CheckIn, PendingActor, PendingCheckIn, Revision string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newFortuneFixture(t, nil)
		c := fortuneCorpus("restart-" + f.actor)
		if e := f.reports.InstallCorpus(f.ctx, c); e != nil {
			t.Fatal(e)
		}
		v := fortuneCheckIn(t, f)
		fortuneStep(t, f)
		g := newFortuneFixture(t, nil)
		pending := fortuneCheckIn(t, g)
		b, _ := json.Marshal(saved{f.actor, v.ID, g.actor, pending.ID, c.Revision})
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		*f.preserve = true
		*g.preserve = true
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var s saved
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	f := newFortuneFixture(t, nil)
	// Reissue synthetic authority for the persisted owner; no report restoration.
	f.actor = s.Actor
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.principal, e = f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.guard.Bind(f.ctx, f.principal)
	if got := fortuneView(t, f, s.CheckIn); got.Generation == nil || got.Status.State != "ready" {
		t.Fatal(got)
	}
	fortuneStep(t, f)
	f.actor = s.PendingActor
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.principal, e = f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = f.guard.Bind(f.ctx, f.principal)
	if got := fortuneView(t, f, s.PendingCheckIn); got.Generation == nil || got.Status.State != "ready" {
		t.Fatal(got)
	}
}
