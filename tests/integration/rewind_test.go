// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/rewind"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strings"
	"testing"
	"time"
)

type rewindFixture struct {
	chatRelationsFixture
	rewinds    *rewindpg.Repository
	reportHTTP http.Handler
	publisher  string
	postRepo   *postpg.Repository
	options    rewindpg.Options
}

func newRewindFixture(t *testing.T) rewindFixture {
	t.Helper()
	f := rewindFixture{chatRelationsFixture: newChatRelationsFixture(t), options: rewindpg.Options{Now: time.Now, ReportingYear: time.Now().UTC().Year()}}
	query(t, f.db, rewindpg.Schema)
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,now()-interval '1 hour')`, f.owner)
	name := "rewind_" + f.owner
	a := accountpg.NewWithAuthority(f.db, f.guard).WithRealms(f.social)
	p, e := a.CreatePublisher(f.contexts[f.owner], f.owner, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	f.publisher = p.ID
	f.postRepo = postpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social)
	return configureRewind(t, f)
}
func configureRewind(t *testing.T, f rewindFixture) rewindFixture {
	t.Helper()
	a := accountpg.NewWithAuthority(f.db, f.guard).WithRealms(f.social)
	var e error
	f.rewinds, e = rewindpg.New(f.db, f.guard, a, f.chats, f.social, f.options)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.rewinds.Ready(f.base); e != nil {
		t.Fatal(e)
	}
	f.reportHTTP = (&rewind.Service{Repository: f.rewinds, Authority: f.verifier}).Handler()
	return f
}
func rewindPost(t *testing.T, f rewindFixture) posts.Post {
	t.Helper()
	body := "Synthetic annual-report post"
	visible := posts.Public
	p, e := f.postRepo.Create(f.contexts[f.owner], f.owner, f.publisher, posts.Patch{Content: &body, Visibility: &visible})
	if e != nil {
		t.Fatal(e)
	}
	return p
}

type rewindOwnedLogs struct{ times []time.Time }

func (s rewindOwnedLogs) ActionTimes(context.Context, *sql.DB, *sql.Tx, string, time.Time, time.Time, int) ([]time.Time, error) {
	return s.times, nil
}

type rewindOwnedTokenizer struct{}

func (rewindOwnedTokenizer) TopWords(context.Context, []string, int) ([]rewind.Word, error) {
	return []rewind.Word{{Word: "owned_synthetic_token", Count: 3}}, nil
}

func TestPGRewindCanonicalSnapshotSharingAndRevocation(t *testing.T) {
	f := newRewindFixture(t)
	p := rewindPost(t, f)
	for _, day := range []int{1, 2, 3} {
		at := time.Date(f.options.ReportingYear, 2, day, 0, 0, 0, 0, time.UTC)
		query(t, f.db, `INSERT INTO owned_progression_checkins(id,account_id,day,level,reward_experience,created_at)VALUES($1::uuid,$2::uuid,$3::timestamptz::date,0,100,$3::timestamptz)`, authorityUUID(t), f.owner, at)
	}
	query(t, f.db, `INSERT INTO owned_post_reactions(id,post_id,account_id,symbol,attitude,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,'synthetic',0,now())`, authorityUUID(t), p.ID, f.owner)
	direct := relationDirect(t, f.chatRelationsFixture)
	relationSend(t, f.chatRelationsFixture, f.owner, direct.ID, "Synthetic private report message")
	var member string
	if e := f.db.QueryRow(`SELECT id::text FROM chat_members WHERE chat_room_id=$1::uuid AND account_id=$2::uuid`, direct.ID, f.owner).Scan(&member); e != nil {
		t.Fatal(e)
	}
	call := authorityUUID(t)
	query(t, f.db, `INSERT INTO call_v1.calls(id,room_id,sender_id,provider,provider_endpoint,session_id,state,created_at,updated_at,expires_at,ended_at)VALUES($1::uuid,$2::uuid,$3::uuid,'synthetic','https://synthetic.invalid',$4::text,'ended',now()-interval '10 minutes',now(),now()+interval '1 hour',now()-interval '5 minutes')`, call, direct.ID, member, "synthetic-"+call)
	point := backendDecode[rewind.Point](t, settingsCall(t, f.reportHTTP, "GET", "/api/rewind/me", f.token, "", 200))
	if point.SharableCode != nil || len(point.UnavailableComponents) != 2 {
		t.Fatal("missing providers not explicit", point)
	}
	own, e := f.rewinds.Current(f.contexts[f.owner], f.owner)
	if e != nil || own.ID != point.ID {
		t.Fatal("snapshot duplicated", e)
	}
	passport := own.Data["passport"].(rewind.Passport)
	if passport.MaxCheckInStreak != 3 || passport.CheckInCompleteness != float64(3)/365 {
		t.Fatal(passport)
	}
	raw, _ := json.Marshal(own)
	if !strings.Contains(string(raw), direct.ID) || !strings.Contains(string(raw), f.other) {
		t.Fatal("owner direct/call report missing", string(raw))
	}
	shared, e := f.rewinds.Share(f.contexts[f.owner], f.owner, point.Year, true)
	if e != nil || shared.SharableCode == nil {
		t.Fatal(shared, e)
	}
	code := *shared.SharableCode
	public := settingsCall(t, f.reportHTTP, "GET", "/api/rewind/"+code, "", "", 200)
	if strings.Contains(public.Body.String(), direct.ID) || strings.Contains(public.Body.String(), f.other) || !strings.Contains(public.Body.String(), p.ID) || strings.Contains(public.Body.String(), "birthday") {
		t.Fatal("shared reference privacy", public.Body.String())
	}
	private := posts.Private
	if _, e = f.postRepo.Update(f.contexts[f.owner], f.owner, p.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	public = settingsCall(t, f.reportHTTP, "GET", "/api/rewind/"+code, "", "", 200)
	if strings.Contains(public.Body.String(), p.ID) || strings.Contains(public.Body.String(), p.Content) {
		t.Fatal("cached post bypassed new privacy", public.Body.String())
	}
	if _, e = f.rewinds.Share(f.contexts[f.owner], f.owner, point.Year, false); e != nil {
		t.Fatal(e)
	}
	settingsCall(t, f.reportHTTP, "GET", "/api/rewind/"+code, "", "", 404)
	shared, e = f.rewinds.Share(f.contexts[f.owner], f.owner, point.Year, true)
	if e != nil || *shared.SharableCode == code {
		t.Fatal("old share code retained", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.owner)
	settingsCall(t, f.reportHTTP, "GET", "/api/rewind/"+*shared.SharableCode, "", "", 404)
}
func TestPGRewindConfiguredComponentsAndIsolation(t *testing.T) {
	f := newRewindFixture(t)
	rewindPost(t, f)
	f.options.ActionLogs = rewindOwnedLogs{[]time.Time{time.Date(f.options.ReportingYear, 3, 4, 1, 23, 0, 0, time.UTC)}}
	f.options.Tokenizer = rewindOwnedTokenizer{}
	f = configureRewind(t, f)
	p, e := f.rewinds.Current(f.contexts[f.owner], f.owner)
	if e != nil || len(p.UnavailableComponents) != 0 {
		t.Fatal(p, e)
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), "owned_synthetic_token") || !strings.Contains(string(raw), "01:23") {
		t.Fatal(string(raw))
	}
	if _, e = f.rewinds.Current(context.Background(), f.owner); e == nil {
		t.Fatal("asserted owner accepted")
	}
	if _, e = f.rewinds.Share(f.contexts[f.other], f.owner, p.Year, true); e == nil {
		t.Fatal("other account shared report")
	}
	if _, e = f.rewinds.Share(f.contexts[f.other], f.other, p.Year, true); !errors.Is(e, rewind.ErrNotFound) {
		t.Fatal("foreign report lookup", e)
	}
	shared, e := f.rewinds.Share(f.contexts[f.owner], f.owner, p.Year, true)
	if e != nil {
		t.Fatal(e)
	}
	public, e := f.rewinds.Public(f.base, *shared.SharableCode)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(public)
	if strings.Contains(string(raw), "owned_synthetic_token") {
		t.Fatal("private text aggregation shared")
	}
	wrongPool := database(t)
	tx, e := wrongPool.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	a := accountpg.NewWithAuthority(f.db, f.guard)
	if _, e = a.RewindAccount(f.base, wrongPool, tx, f.owner); e == nil {
		t.Fatal("reference adapter accepted another pool")
	}
}
func TestPGRewindAtomicCreationAndBlockedPublicRevocation(t *testing.T) {
	f := newRewindFixture(t)
	query(t, f.db, `CREATE FUNCTION synthetic_rewind_audit_fail()RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.account_id::text=TG_ARGV[0] THEN RAISE EXCEPTION 'synthetic rewind rollback';END IF;RETURN NEW;END$$`)
	query(t, f.db, fmt.Sprintf(`CREATE TRIGGER synthetic_rewind_audit_fail BEFORE INSERT ON owned_account_rewind_audit FOR EACH ROW EXECUTE FUNCTION synthetic_rewind_audit_fail('%s')`, f.owner))
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DROP TRIGGER IF EXISTS synthetic_rewind_audit_fail ON owned_account_rewind_audit;DROP FUNCTION IF EXISTS synthetic_rewind_audit_fail()`)
	})
	if _, e := f.rewinds.Current(f.contexts[f.owner], f.owner); e == nil {
		t.Fatal("audit failure hidden")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_account_rewind_points WHERE account_id=$1::uuid`, f.owner); n != 0 {
		t.Fatal("partial snapshot")
	}
	query(t, f.db, `DROP TRIGGER synthetic_rewind_audit_fail ON owned_account_rewind_audit`)
	start := make(chan struct{})
	results := make(chan rewind.Point, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; p, e := f.rewinds.Current(f.contexts[f.owner], f.owner); results <- p; errs <- e }()
	}
	close(start)
	a, b := <-results, <-results
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if a.ID == "" || a.ID != b.ID {
		t.Fatal("concurrent generation duplicated", a.ID, b.ID)
	}
	shared, e := f.rewinds.Share(f.contexts[f.owner], f.owner, a.Year, true)
	if e != nil {
		t.Fatal(e)
	}
	locks := atomicAuthorityFixture{db: f.db, ctx: f.base}
	blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.owner)
	done := make(chan error, 1)
	go func() { _, e := f.rewinds.Public(f.base, *shared.SharableCode); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
	if _, e = blocker.ExecContext(f.base, `UPDATE owned_account_rewind_points SET sharable_code=NULL WHERE id=$1::uuid`, a.ID); e != nil {
		t.Fatal(e)
	}
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, rewind.ErrNotFound) {
		t.Fatal("stale public code after blocked revoke", e)
	}
}
func TestPGRewindRestartPersistence(t *testing.T) {
	_ = database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "rewind-record.json")
	type saved struct {
		Actor, Point, Code string
		Year               int
	}
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newRewindFixture(t)
		rewindPost(t, f)
		p, e := f.rewinds.Current(f.contexts[f.owner], f.owner)
		if e != nil {
			t.Fatal(e)
		}
		p, e = f.rewinds.Share(f.contexts[f.owner], f.owner, p.Year, true)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(saved{f.owner, p.ID, *p.SharableCode, p.Year})
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if os.Getenv("PG_TEST_PHASE") != "after" {
		t.Fatal("explicit restart phase required")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var data saved
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	f := newRewindFixture(t)
	f.owner = data.Actor
	f.options.ReportingYear = data.Year
	f.token = pgIssue(t, f.issuer, f.owner).Token
	p, e := f.verifier.Authenticate(f.base, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.contexts[f.owner] = f.guard.Bind(f.base, p)
	f = configureRewind(t, f)
	own, e := f.rewinds.Current(f.contexts[f.owner], f.owner)
	if e != nil || own.ID != data.Point {
		t.Fatal("report did not persist", own, e)
	}
	shared, e := f.rewinds.Public(f.base, data.Code)
	if e != nil || shared.ID != data.Point {
		t.Fatal("sharing did not persist", shared, e)
	}
}
