// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/social"
)

type calendarFixture struct {
	db                              *sql.DB
	repo                            *notabledaypg.Repository
	guard                           *authoritypg.TransactionGuard
	verifier                        *authority.Verifier
	issuer                          *authority.Service
	authConfig                      authority.Config
	config                          notabledaypg.Config
	actor, other, token, otherToken string
	ctx, otherCtx                   context.Context
	now                             time.Time
}

func newCalendarFixture(t *testing.T) calendarFixture {
	t.Helper()
	f := calendarFixture{db: database(t), actor: authorityUUID(t), other: authorityUUID(t), now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	clock := &atomic.Int64{}
	clock.Store(f.now.Unix())
	f.authConfig = authorityConfig(t, authorityKeys(t), clock)
	for _, account := range []string{f.actor, f.other} {
		if e := authoritypg.SeedSynthetic(context.Background(), f.db, account, "synthetic_calendar_"+strings.ReplaceAll(account, "-", ""), f.now); e != nil {
			t.Fatal(e)
		}
		rules := []authority.Rule{{Pattern: "calendar.*"}}
		if account == f.actor {
			rules = []authority.Rule{{Pattern: "*"}}
		}
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), account, rules, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `UPDATE owned_authority_accounts SET region='CN' WHERE id=$1::uuid`, account)
	}
	var e error
	f.issuer, e = authority.New(authoritypg.New(f.db), f.authConfig)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.otherToken = pgIssue(t, f.issuer, f.other).Token
	f.config = notabledaypg.Config{Now: func() time.Time { return f.now }}
	return calendarPool(t, f, f.db)
}
func calendarPool(t *testing.T, f calendarFixture, db *sql.DB) calendarFixture {
	t.Helper()
	f.db = db
	var e error
	f.verifier, e = authority.NewVerifier(authoritypg.New(db), f.authConfig)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.verifier)
	f.repo, e = notabledaypg.New(db, f.guard, f.config)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.Ready(context.Background()); e != nil {
		t.Fatal("guarded runner must explicitly install notabledaypg schema", e)
	}
	f.ctx = calendarProof(t, f, f.token)
	f.otherCtx = calendarProof(t, f, f.otherToken)
	return f
}
func calendarProof(t *testing.T, f calendarFixture, token string) context.Context {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/notable-days", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, e := f.verifier.AuthenticateRequest(r)
	if e != nil {
		t.Fatal(e)
	}
	return f.repo.BindAuthority(context.Background(), p)
}
func calendarExec[T any](t *testing.T, f calendarFixture, ctx context.Context, actor string, c notabledays.Command) T {
	t.Helper()
	if c.Query.Take == 0 {
		c.Query.Take = 50
	}
	out, e := f.repo.Execute(ctx, actor, c)
	if e != nil {
		t.Fatal(c.Operation, e)
	}
	v, ok := out.Value.(T)
	if !ok {
		t.Fatalf("%s returned %T", c.Operation, out.Value)
	}
	return v
}
func calendarHTTP(f calendarFixture) http.Handler {
	return (&notabledays.Service{Repository: f.repo, Authority: f.verifier}).Handler()
}
func calendarCall(t *testing.T, h http.Handler, method, path, token, key, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}
func calendarTime(s string) time.Time {
	v, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return v
}
func eventPatch(title string, visibility int, start time.Time) *notabledays.EventPatch {
	end := start.Add(time.Hour)
	return &notabledays.EventPatch{Title: &title, Visibility: &visibility, StartTime: &start, EndTime: &end}
}
func createCalendarEvent(t *testing.T, f calendarFixture, title string, visibility int) notabledays.Event {
	return calendarExec[notabledays.Event](t, f, f.ctx, f.actor, notabledays.Command{Operation: "event.create", Patch: eventPatch(title, visibility, f.now.Add(time.Hour))})
}
func calendarFriend(t *testing.T, f calendarFixture) {
	t.Helper()
	r := socialpg.New(f.db)
	if _, e := r.ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.ChangeRelationship(context.Background(), f.other, f.actor, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
}

func TestPGNotableDayCatalogLunarAndStableLookup(t *testing.T) {
	f := newCalendarFixture(t)
	h := calendarHTTP(f)
	pattern, key := "01-01", "spring-"+f.actor
	input := notabledays.DayInput{Name: "Synthetic Spring", StartDate: calendarTime("2026-02-16T00:00:00Z"), EndDate: calendarTime("2026-02-20T00:00:00Z"), IsAllDay: true, Region: "CN", Tags: []int{notabledays.Holiday}, IsRecurring: true, IsPeriod: true, RecurrencePattern: &pattern, LocalizableKey: &key, Meta: json.RawMessage(`{"calendar":"lunar","priority":1}`)}
	raw, _ := json.Marshal(input)
	calendarCall(t, h, "POST", "/api/notable-days", f.otherToken, "", string(raw), 403)
	created := backendDecode[notabledays.Day](t, calendarCall(t, h, "POST", "/api/notable-days", f.token, "", string(raw), 200))
	if created.Region != "CN" {
		t.Fatal(created)
	}
	page := calendarCall(t, h, "GET", "/api/notable-days?year=2027&region=cn&tag=Holiday&layer=1", "", "", "", 200)
	if page.Header().Get("X-Total") == "0" {
		t.Fatal(page.Body.String())
	}
	key2027 := notabledays.OccurrenceKey("cn", calendarTime("2027-02-05T00:00:00Z"), key)
	generated := backendDecode[notabledays.Occurrence](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/notable-days/"+url.PathEscape(key2027), f.token, "", "", 200))
	if generated.GlobalName != input.Name || generated.Date.Format("2006-01-02") != "2027-02-05" || generated.SourceID == nil || *generated.SourceID != created.ID {
		t.Fatal(generated)
	}
	input.Region = "US"
	raw, _ = json.Marshal(input)
	calendarCall(t, h, "PUT", "/api/notable-days/"+created.ID, f.token, "", string(raw), 200)
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/notable-days/"+url.PathEscape(key2027), f.token, "", "", 404)
	usKey := notabledays.OccurrenceKey("us", calendarTime("2027-02-05T00:00:00Z"), key)
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/notable-days/"+url.PathEscape(usKey), f.token, "", "", 200)
	calendarCall(t, h, "DELETE", "/api/notable-days/"+created.ID, f.token, "", "", 204)
	calendarCall(t, h, "GET", "/api/notable-days/"+created.ID, "", "", "", 404)
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/notable-days/"+url.PathEscape(usKey), f.token, "", "", 404)
}
func TestPGCalendarPrivacySubscriptionsAndSearch(t *testing.T) {
	f := newCalendarFixture(t)
	h := calendarHTTP(f)
	private := createCalendarEvent(t, f, "Private calendar secret", notabledays.Private)
	public := createCalendarEvent(t, f, "Visible public event", notabledays.Public)
	friend := createCalendarEvent(t, f, "Friends event", notabledays.Friends)
	for _, event := range []notabledays.Event{private, friend} {
		calendarCall(t, h, "GET", "/api/accounts/unknown/calendar/events/"+event.ID, "", "", "", 404)
		calendarCall(t, h, "GET", "/api/accounts/me/calendar/events/"+event.ID, f.otherToken, "", "", 404)
	}
	calendarCall(t, h, "GET", "/api/accounts/unknown/calendar/events/"+public.ID, "", "", "", 200)
	all := backendDecode[[]notabledays.Event](t, calendarCall(t, h, "GET", "/api/accounts/"+f.actor+"/calendar/events", "", "", "", 200))
	if len(all) != 1 || all[0].ID != public.ID {
		t.Fatal("anonymous owner list leak", all)
	}
	subscription := backendDecode[notabledays.Subscription](t, calendarCall(t, h, "POST", "/api/accounts/me/calendar/subscriptions/"+f.actor, f.otherToken, "", "", 201))
	again := backendDecode[notabledays.Subscription](t, calendarCall(t, h, "POST", "/api/accounts/me/calendar/subscriptions/"+f.actor, f.otherToken, "", "", 201))
	if subscription.ID != again.ID {
		t.Fatal("subscription replay", again)
	}
	feed := backendDecode[[]notabledays.Event](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/events", f.otherToken, "", "", 200))
	if len(feed) != 1 || feed[0].ID != public.ID {
		t.Fatal(feed)
	}
	calendarFriend(t, f)
	feed = backendDecode[[]notabledays.Event](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/events", f.otherToken, "", "", 200))
	if len(feed) != 2 {
		t.Fatal(feed)
	}
	calendarCall(t, h, "PUT", "/api/accounts/me/calendar/events/"+public.ID, f.otherToken, "", `{"title":"cross-account mutation"}`, 404)
	calendarCall(t, h, "DELETE", "/api/accounts/me/calendar/events/"+public.ID, f.otherToken, "", "", 404)
	tags := []string{" Work ", "work", "Travel"}
	calendarExec[notabledays.Event](t, f, f.ctx, f.actor, notabledays.Command{Operation: "event.update", ID: public.ID, Patch: &notabledays.EventPatch{Tags: &tags}})
	items := backendDecode[[]notabledays.Event](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/events?tags=work&tags=travel", f.otherToken, "", "", 200))
	if len(items) != 1 || len(items[0].Tags) != 2 || items[0].Tags[0] != "travel" {
		t.Fatal(items)
	}
	search := calendarCall(t, h, "GET", "/api/accounts/me/calendar/search?query=visible&startTime=2026-10-01T00%3A00%3A00Z&endTime=2026-11-01T00%3A00%3A00Z", f.otherToken, "", "", 200)
	if strings.Contains(search.Body.String(), private.Title) || !strings.Contains(search.Body.String(), public.Title) {
		t.Fatal(search.Body.String())
	}
	if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	feed = backendDecode[[]notabledays.Event](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/events", f.otherToken, "", "", 200))
	if len(feed) != 0 {
		t.Fatal("blocked feed", feed)
	}
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/events/"+public.ID, f.otherToken, "", "", 404)
	calendarCall(t, h, "DELETE", "/api/accounts/me/calendar/subscriptions/"+f.actor, f.otherToken, "", "", 204)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
	calendarCall(t, h, "GET", "/api/accounts/unknown/calendar/events/"+public.ID, "", "", "", 404)
}
func TestPGCalendarRecurrenceMutationAndHTTPBounds(t *testing.T) {
	f := newCalendarFixture(t)
	h := calendarHTTP(f)
	body := `{"title":"Morning recurrence","start_time":"2026-03-07T14:00:00Z","end_time":"2026-03-07T15:00:00Z","time_zone":"America/New_York","recurrence":{"frequency":1,"interval":1,"occurrences":3},"tags":[" Daily "]}`
	event := backendDecode[notabledays.Event](t, calendarCall(t, h, "POST", "/api/accounts/me/calendar/events", f.token, "morning", body, 201))
	replayed := backendDecode[notabledays.Event](t, calendarCall(t, h, "POST", "/api/accounts/me/calendar/events", f.token, "morning", body, 201))
	if replayed.ID != event.ID || event.Tags[0] != "daily" {
		t.Fatal(event, replayed)
	}
	path := "/api/accounts/me/calendar/events?startTime=2026-03-07T00%3A00%3A00Z&endTime=2026-03-11T00%3A00%3A00Z&take=1&offset=1"
	w := calendarCall(t, h, "GET", path, f.token, "", "", 200)
	values := backendDecode[[]notabledays.Event](t, w)
	if len(values) != 1 || w.Header().Get("X-Total") != "3" || values[0].StartTime.Format(time.RFC3339) != "2026-03-08T13:00:00Z" || values[0].Recurrence != nil || values[0].OccurrenceKey == "" {
		t.Fatal(values, w.Header())
	}
	month := backendDecode[[]notabledays.DailyCalendar](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar?year=2026&month=3&timeZone=America%2FNew_York", f.token, "", "", 200))
	if len(month) != 31 || len(month[7].UserEvents) != 1 {
		t.Fatal(month)
	}
	changed := backendDecode[notabledays.Event](t, calendarCall(t, h, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", `{"recurrence":null}`, 200))
	if changed.Recurrence != nil || changed.Version != 2 {
		t.Fatal(changed)
	}
	w = calendarCall(t, h, "GET", path, f.token, "", "", 200)
	if w.Header().Get("X-Total") != "1" {
		t.Fatal(w.Header(), w.Body.String())
	}
	calendarCall(t, h, "POST", "/api/accounts/me/calendar/events", f.token, "morning", strings.Replace(body, "Morning recurrence", "Changed", 1), 409)
	for _, patch := range []string{`{"recurrence":{"frequency":1,"interval":0}}`, `{"time_zone":"Not/AZone"}`, `{"visibility":999}`, `{"start_time":"2027-01-01T00:00:00Z"}`, `{"meta":{"x":1,"x":2}}`} {
		calendarCall(t, h, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", patch, 400)
	}
	calendarCall(t, h, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", `{"icon_id":"unconfigured-file"}`, 501)
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/events?take=101", f.token, "", "", 400)
}
func TestPGCalendarCanonicalActivityOverlayPrivacy(t *testing.T) {
	f := newCalendarFixture(t)
	statusID, checkinID := authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_presence_statuses(id,account_id,attitude,type,label,symbol,meta,created_at,updated_at)VALUES($1::uuid,$2::uuid,1,3,'Invisible private status','moon','{"secret":"hidden"}'::jsonb,$3::timestamptz,$3::timestamptz)`, statusID, f.actor, f.now)
	query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1::uuid,'private',$2::timestamptz)`, f.actor, f.now)
	query(t, f.db, `INSERT INTO owned_progression_checkins(id,account_id,day,level,reward_experience,created_at)VALUES($1::uuid,$2::uuid,$3::date,2,100,$4::timestamptz)`, checkinID, f.actor, f.now.Format("2006-01-02"), f.now)
	f.config.Activity = notabledaypg.CanonicalActivityOverlay{}
	f = calendarPool(t, f, f.db)
	h := calendarHTTP(f)
	mine := calendarCall(t, h, "GET", "/api/accounts/me/calendar?year=2026&month=10", f.token, "", "", 200)
	if !strings.Contains(mine.Body.String(), checkinID) || !strings.Contains(mine.Body.String(), statusID) || strings.Contains(mine.Body.String(), "hidden") || strings.Contains(mine.Body.String(), "unavailable_components") {
		t.Fatal(mine.Body.String())
	}
	calendarFriend(t, f)
	other := calendarCall(t, h, "GET", "/api/accounts/"+f.actor+"/calendar?year=2026&month=10", f.otherToken, "", "", 200)
	if strings.Contains(other.Body.String(), statusID) || strings.Contains(other.Body.String(), checkinID) {
		t.Fatal("private activity leaked", other.Body.String())
	}
	query(t, f.db, `UPDATE owned_presence_preferences SET status_visibility='friends' WHERE account_id=$1::uuid`, f.actor)
	other = calendarCall(t, h, "GET", "/api/accounts/"+f.actor+"/calendar?year=2026&month=10", f.otherToken, "", "", 200)
	entries := backendDecode[[]notabledays.DailyCalendar](t, other)
	if len(entries[7].Statuses) != 1 || string(entries[7].CheckInResult) != "null" {
		t.Fatal(entries[7])
	}
	var status map[string]any
	if e := json.Unmarshal(entries[7].Statuses[0], &status); e != nil || status["type"] != float64(0) {
		t.Fatal(status, e)
	}
}
func TestPGCalendarAtomicityAndFreshAuthority(t *testing.T) {
	f := newCalendarFixture(t)
	fresh := calendarPool(t, f, database(t))
	patch := eventPatch("Concurrent calendar", notabledays.Public, f.now.Add(time.Hour))
	type result struct {
		value notabledays.Result
		e     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, x := range []calendarFixture{f, fresh} {
		go func(x calendarFixture) {
			<-start
			v, e := x.repo.Execute(x.ctx, x.actor, notabledays.Command{Operation: "event.create", IdempotencyKey: "same", Patch: patch})
			results <- result{v, e}
		}(x)
	}
	close(start)
	var eventID string
	for i := 0; i < 2; i++ {
		r := <-results
		if r.e != nil {
			t.Fatal(r.e)
		}
		event := r.value.Value.(notabledays.Event)
		if eventID != "" && eventID != event.ID {
			t.Fatal("duplicate event", eventID, event.ID)
		}
		eventID = event.ID
	}
	// Opposing subscriptions use the social writer's canonical account lock
	// order, so their foreign-key locks cannot form a reciprocal deadlock.
	subStart := make(chan struct{})
	subResults := make(chan error, 2)
	go func() {
		<-subStart
		_, e := f.repo.Execute(f.ctx, f.actor, notabledays.Command{Operation: "subscription.add", TargetID: f.other})
		subResults <- e
	}()
	go func() {
		<-subStart
		_, e := fresh.repo.Execute(fresh.otherCtx, f.other, notabledays.Command{Operation: "subscription.add", TargetID: f.actor})
		subResults <- e
	}()
	close(subStart)
	for i := 0; i < 2; i++ {
		if e := <-subResults; e != nil {
			t.Fatal("reciprocal subscription", e)
		}
	}
	query(t, f.db, `CREATE FUNCTION synthetic_calendar_audit_fail()RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic calendar audit failure'; END IF; RETURN NEW; END $$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_calendar_audit_fail BEFORE INSERT ON owned_calendar_audit FOR EACH ROW EXECUTE FUNCTION synthetic_calendar_audit_fail('%s')`, f.actor))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_calendar_audit_fail ON owned_calendar_audit;DROP FUNCTION IF EXISTS synthetic_calendar_audit_fail()`)
	})
	title := "Must roll back"
	if _, e := f.repo.Execute(f.ctx, f.actor, notabledays.Command{Operation: "event.update", ID: eventID, Patch: &notabledays.EventPatch{Title: &title}}); e == nil {
		t.Fatal("audit failure hidden")
	}
	query(t, f.db, `DROP TRIGGER synthetic_calendar_audit_fail ON owned_calendar_audit;DROP FUNCTION synthetic_calendar_audit_fail()`)
	got := calendarExec[notabledays.Event](t, f, f.ctx, f.actor, notabledays.Command{Operation: "event.get", ID: eventID})
	if got.Title != "Concurrent calendar" || got.Version != 1 {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
	if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid`, f.actor); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.Execute(ctx, f.actor, notabledays.Command{Operation: "event.create", Patch: patch})
		done <- e
	}()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e := f.repo.Execute(context.Background(), f.other, notabledays.Command{Operation: "event.create", Patch: patch}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("asserted ID became proof", e)
	}
}
func TestPGCalendarRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "calendar-record.json")
	type saved struct{ Actor, Other, Public, Private, Recurring string }
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newCalendarFixture(t)
		public := createCalendarEvent(t, f, "Durable public", notabledays.Public)
		private := createCalendarEvent(t, f, "Durable private", notabledays.Private)
		patch := eventPatch("Durable recurrence", notabledays.Private, calendarTime("2026-01-31T09:00:00Z"))
		patch.Recurrence = &notabledays.Recurrence{Frequency: notabledays.Monthly, Interval: 1}
		recur := calendarExec[notabledays.Event](t, f, f.ctx, f.actor, notabledays.Command{Operation: "event.create", Patch: patch})
		calendarExec[notabledays.Subscription](t, f, f.otherCtx, f.other, notabledays.Command{Operation: "subscription.add", TargetID: f.actor})
		b, e := json.Marshal(saved{f.actor, f.other, public.ID, private.ID, recur.ID})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var state saved
	if e = json.Unmarshal(b, &state); e != nil {
		t.Fatal(e)
	}
	clock := &atomic.Int64{}
	clock.Store(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC).Unix())
	cfg := authorityConfig(t, authorityKeys(t), clock)
	issuer, e := authority.New(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	repo, e := notabledaypg.New(db, guard, notabledaypg.Config{Now: func() time.Time { return time.Unix(clock.Load(), 0) }})
	if e != nil {
		t.Fatal(e)
	}
	h := (&notabledays.Service{Repository: repo, Authority: v}).Handler()
	token := pgIssue(t, issuer, state.Actor).Token
	otherToken := pgIssue(t, issuer, state.Other).Token
	calendarCall(t, h, "GET", "/api/accounts/unknown/calendar/events/"+state.Public, "", "", "", 200)
	calendarCall(t, h, "GET", "/api/accounts/unknown/calendar/events/"+state.Private, "", "", "", 404)
	calendarCall(t, h, "GET", "/api/accounts/me/calendar/events/"+state.Private, token, "", "", 200)
	w := calendarCall(t, h, "GET", "/api/accounts/me/calendar/events?startTime=2026-03-01T00%3A00%3A00Z&endTime=2026-04-01T00%3A00%3A00Z", token, "", "", 200)
	events := backendDecode[[]notabledays.Event](t, w)
	if len(events) != 1 || events[0].ID != state.Recurring || events[0].StartTime.Day() != 31 || events[0].StartTime.Hour() != 9 {
		t.Fatal(events)
	}
	subscriptions := backendDecode[[]string](t, calendarCall(t, h, "GET", "/api/accounts/me/calendar/subscriptions", otherToken, "", "", 200))
	if len(subscriptions) != 1 || subscriptions[0] != state.Actor {
		t.Fatal(subscriptions)
	}
}
