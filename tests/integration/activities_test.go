// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
)

type activityFixture struct {
	backendFixture
	repo  *activitypg.Repository
	guard *authoritypg.TransactionGuard
	ctx   context.Context
}

func newActivityFixture(t *testing.T) activityFixture {
	t.Helper()
	f := newBackendFixture(t)
	query(t, f.db, activitypg.Schema)
	// Original credentials and canonical session authority precede target state.
	at, wall := time.Now().UTC(), time.Now()
	f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
	f.config.AccessTTL = time.Hour
	f.config.SessionTTL = time.Hour
	issuer, e := authority.New(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.issuer = issuer
	f.token = pgIssue(t, issuer, f.actor).Token
	f.otherToken = pgIssue(t, issuer, f.other).Token
	f.app = backendApp(t, f.db, f.config)
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	proof, e := verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 30*time.Second)
	t.Cleanup(cancel)
	return activityFixture{f, activitypg.New(f.db, guard), guard, ctx}
}
func activityRequest(manual string) activities.Request {
	typ, title, key := "coding", "Synthetic editor", "editor-catalog"
	return activities.Request{Content: activities.Content{Type: &typ, Title: &title, QueryableTerms: []string{" Editor ", "editor"}, Tags: []activities.Tag{{Slug: "work", Name: "Work"}}}, ManualID: &manual, LeaseMinutes: 5, Visibility: activities.Public, CatalogKey: &key}
}
func activityStart(t *testing.T, f activityFixture, manual string) activities.Activity {
	t.Helper()
	v, e := f.repo.Set(f.ctx, f.actor, activityRequest(manual), true, "")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestPGBackendActivityAuthoredRoutesLifecycle(t *testing.T) {
	f := newActivityFixture(t)
	body := `{"type":"coding","manual_id":"editor","title":"Synthetic activity","provider":" OWNED ","reference_id":"ref-1","queryable_terms":[" IDE ","ide"],"tags":[{"slug":"work","name":"Work"}],"catalog_key":"catalog","lease_minutes":5,"visibility":1}`
	first := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/passport/activities/sessions/start", f.token, body, 200))
	var storedStart time.Time
	if e := f.db.QueryRow(`SELECT started_at FROM owned_presence_activities WHERE id=$1`, first.ID).Scan(&storedStart); e != nil || first.StartedAt == nil || !first.StartedAt.Equal(storedStart) {
		t.Fatal("start response differs from persisted transition", e)
	}
	again := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities/sessions/start", f.token, body, 200))
	if first.ID != again.ID || first.CatalogID == nil || first.StartedAt == nil || !first.StartedAt.Equal(*again.StartedAt) || again.Provider == nil || *again.Provider != "owned" || len(again.QueryableTerms) != 1 {
		t.Fatal("renewal lost session identity", first, again)
	}
	page := backendCall(t, f.app, "GET", "/api/activities?provider=owned&referenceId=ref-1&term=ide&tags=work&take=1", f.token, "", 200)
	if page.Header().Get("X-Total") != "1" || !strings.Contains(page.Body.String(), first.ID) {
		t.Fatal(page.Body.String(), page.Header())
	}
	// Real elapsed time establishes the floor-second calculation.
	organizationWaitExpired(t, f.ctx, f.db, first.StartedAt.Add(1100*time.Millisecond))
	ended := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities/sessions/end", f.token, `{"manual_id":"editor"}`, 200))
	var storedEnd time.Time
	if e := f.db.QueryRow(`SELECT ended_at FROM owned_presence_activities WHERE id=$1`, ended.ID).Scan(&storedEnd); e != nil || ended.EndedAt == nil || !ended.EndedAt.Equal(storedEnd) {
		t.Fatal("end response differs from persisted transition", e)
	}
	if ended.EndedAt == nil {
		t.Fatal(ended)
	}
	want := int64(ended.EndedAt.Sub(*ended.StartedAt) / time.Second)
	if want < 1 {
		t.Fatal("duration was not elapsed", want)
	}
	backendCall(t, f.app, "POST", "/api/activities/sessions/end", f.token, `{"manual_id":"editor"}`, 404)
	stats := backendDecode[map[string]int64](t, backendCall(t, f.app, "GET", "/api/activities/stats?tags=work", f.token, "", 200))
	tags := backendDecode[map[string]int64](t, backendCall(t, f.app, "GET", "/api/activities/tags/stats", f.token, "", 200))
	catalog := backendDecode[[]activities.Catalog](t, backendCall(t, f.app, "GET", "/api/activities/catalog?query=IDE&isActive=true", f.token, "", 200))
	if stats[*first.CatalogID] != want || tags["work"] != want || len(catalog) != 1 || catalog[0].TotalSeconds != want || catalog[0].SessionCount != 1 {
		t.Fatal(stats, tags, catalog, want)
	}
	after := ended.EndedAt.Add(time.Microsecond).Format(time.RFC3339Nano)
	window := backendDecode[map[string]int64](t, backendCall(t, f.app, "GET", "/api/activities/stats?from="+after, f.token, "", 200))
	if len(window) != 0 {
		t.Fatal(window)
	}
	backendCall(t, f.app, "GET", "/api/activities?includeExpired=true&catalogId="+*first.CatalogID, f.token, "", 200)
	simple := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/passport/activities", f.token, `{"manual_id":"plain","type":"gaming","title":"Game","tags":[{"slug":"gaming","name":"Gaming"}]}`, 200))
	if simple.StartedAt != nil {
		t.Fatal("invented sensor session", simple)
	}
	backendCall(t, f.app, "PUT", "/api/activities/"+simple.ID, f.token, `{"title":"New title"}`, 200)
	backendCall(t, f.app, "DELETE", "/api/activities?manualId=plain", f.token, "", 204)
	backendCall(t, f.app, "DELETE", "/api/activities?id="+simple.ID, f.token, "", 204)
	backendCall(t, f.app, "DELETE", "/api/activities?id="+simple.ID, f.token, "", 404)
}
func TestPGActivityPrivacyIsolationAndCurrentAuthority(t *testing.T) {
	for _, prefix := range []string{"/api", "/passport"} {
		for _, route := range []string{"/activities", "/activities/sessions/start"} {
			t.Run(prefix+route+"_omitted_fields", func(t *testing.T) {
				f := newActivityFixture(t)
				for _, visibility := range []int{activities.Private, activities.Friends} {
					manual := fmt.Sprintf("renew-%d", visibility)
					body := fmt.Sprintf(`{"manual_id":%q,"type":"coding","visibility":%d,"catalog_key":%q,"catalog_name":"sensitive-catalog","title":"sensitive-title","tags":[{"slug":"sensitive-tag","name":"Sensitive"}]}`, manual, visibility, manual)
					first := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", prefix+route, f.token, body, 200))
					again := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", prefix+route, f.token, fmt.Sprintf(`{"manual_id":%q,"title":"sensitive-renewal","tags":[{"slug":"sensitive-union","name":"Sensitive union"}]}`, manual), 200))
					if again.ID != first.ID || again.Type == nil || *again.Type != "coding" || again.Visibility != visibility {
						t.Fatal("omitted renewal fields changed identity/type/audience", first, again)
					}
					if w := backendCall(t, f.app, "GET", prefix+"/activities/"+f.actor, f.otherToken, "", 200); strings.Contains(w.Body.String(), "sensitive") {
						t.Fatal("private/friends renewal leaked", w.Body.String())
					}
					if first.StartedAt != nil {
						backendCall(t, f.app, "POST", prefix+"/activities/sessions/end", f.token, fmt.Sprintf(`{"manual_id":%q}`, manual), 200)
					}
				}
				for _, suffix := range []string{"/activities/catalog?query=sensitive-catalog", "/activities/catalog?tags=sensitive-union", "/activities/stats?tags=sensitive-union", "/activities/tags/stats"} {
					w := backendCall(t, f.app, "GET", prefix+suffix, f.otherToken, "", 200)
					if strings.Contains(w.Body.String(), "sensitive") || w.Body.String() != "[]\n" && w.Body.String() != "{}\n" {
						t.Fatal("catalog participation or tags crossed accounts", suffix, w.Body.String())
					}
				}
				if route == "/activities/sessions/start" {
					catalog := backendDecode[[]activities.Catalog](t, backendCall(t, f.app, "GET", prefix+"/activities/catalog?tags=sensitive-tag&tags=sensitive-union", f.token, "", 200))
					if len(catalog) != 2 || catalog[0].SessionCount != 1 || catalog[1].SessionCount != 1 {
						t.Fatal("private catalog tag union missing for owner", catalog)
					}
				}
				backendCall(t, f.app, "POST", prefix+route, f.token, `{"manual_id":"new-without-type"}`, 400)
			})
		}
	}
	f := newActivityFixture(t)
	public := activityStart(t, f, "public")
	backendCall(t, f.app, "POST", "/api/activities", f.token, `{"manual_id":"legacy-private","type":"coding","visibility":0,"title":"sensitive-legacy"}`, 200)
	backendCall(t, f.app, "POST", "/api/activities", f.token, `{"manual_id":"legacy-private","type":"coding","title":"sensitive-legacy"}`, 200)

	for _, vis := range []int{activities.Private, activities.Friends} {
		q := activityRequest(fmt.Sprint(vis))
		q.Visibility = vis
		q.Title = postString(fmt.Sprintf("sensitive-%d", vis))
		if _, e := f.repo.Set(f.ctx, f.actor, q, true, ""); e != nil {
			t.Fatal(e)
		}
	}
	anon := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 200)
	if !strings.Contains(anon.Body.String(), public.ID) || strings.Contains(anon.Body.String(), "sensitive") {
		t.Fatal("anonymous activity leak", anon.Body.String())
	}
	other := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 200)
	if strings.Contains(other.Body.String(), "sensitive") {
		t.Fatal(other.Body.String())
	}
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
	friend := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 200)
	if !strings.Contains(friend.Body.String(), "sensitive-2") || strings.Contains(friend.Body.String(), "sensitive-0") {
		t.Fatal(friend.Body.String())
	}
	backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", f.token, `{"online_visibility":"public","status_visibility":"private","last_seen_visibility":"friends"}`, 200)
	if w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 200); strings.Contains(w.Body.String(), public.ID) {
		t.Fatal("status privacy ignored", w.Body.String())
	}
	own := backendCall(t, f.app, "GET", "/api/activities", f.token, "", 200)
	if !strings.Contains(own.Body.String(), "sensitive-0") {
		t.Fatal(own.Body.String())
	}
	backendCall(t, f.app, "PUT", "/api/activities/"+public.ID, f.otherToken, `{"title":"stolen"}`, 404)
	backendCall(t, f.app, "DELETE", "/api/activities?id="+public.ID, f.otherToken, "", 404)
	if w := backendCall(t, f.app, "GET", "/api/activities/catalog", f.otherToken, "", 200); w.Body.String() != "[]\n" {
		t.Fatal("catalog crossed account", w.Body.String())
	}
	backendCall(t, f.app, "GET", "/api/activities/stats", "", "", 401)
	backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "forged", "", 401)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "PUT", "/api/activities/"+public.ID, f.token, `{"title":"denied"}`, 403)
	backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", f.token, `{"online_visibility":"public","status_visibility":"public","last_seen_visibility":"friends"}`, 200)
	if e := f.issuer.Revoke(f.ctx, f.token, "current"); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "POST", "/api/activities/sessions/start", f.token, `{"type":"gaming","manual_id":"revoked"}`, 401)
	// Self history remains account-private; revoked originating sessions cannot
	// continue advertising live presence under another viewer's valid token.
	if w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 200); strings.Contains(w.Body.String(), public.ID) {
		t.Fatal(w.Body.String())
	}
}
func TestPGActivityConcurrentRenewalEndAndCatalogAccounting(t *testing.T) {
	t.Run("simultaneous_start_end", func(t *testing.T) {
		f := newActivityFixture(t)
		v := activityStart(t, f, "start-end")
		start := make(chan struct{})
		type result struct {
			end bool
			err error
		}
		done := make(chan result, 16)
		for i := 0; i < 8; i++ {
			go func() {
				<-start
				_, e := f.repo.Set(f.ctx, f.actor, activityRequest("start-end"), true, "")
				done <- result{err: e}
			}()
			go func() { <-start; _, e := f.repo.End(f.ctx, f.actor, "start-end"); done <- result{end: true, err: e} }()
		}
		close(start)
		ended := 0
		for i := 0; i < 16; i++ {
			r := <-done
			if r.err != nil && (!r.end || !errors.Is(r.err, activities.ErrNotFound)) {
				t.Fatal(r.err)
			}
			if r.end && r.err == nil {
				ended++
			}
		}
		var rows, active, events, count int
		var seconds, catalogSeconds int64
		e := f.db.QueryRow(`SELECT count(*) FILTER(WHERE ended_at IS NOT NULL),count(*) FILTER(WHERE ended_at IS NULL AND lease_expires_at>clock_timestamp()),COALESCE(sum(GREATEST(0,floor(extract(epoch FROM (ended_at-started_at))))) FILTER(WHERE ended_at IS NOT NULL),0)::bigint FROM owned_presence_activities WHERE account_id=$1::uuid`, f.actor).Scan(&rows, &active, &seconds)
		if e != nil || ended == 0 || rows != ended || active > 1 {
			t.Fatal("start/end lost serialization", rows, active, ended, e)
		}
		e = f.db.QueryRow(`SELECT session_count,total_seconds FROM owned_activity_catalog WHERE id=$1::uuid`, *v.CatalogID).Scan(&count, &catalogSeconds)
		if e != nil || count != ended || catalogSeconds != seconds {
			t.Fatal("concurrent catalog accounting diverged", count, ended, catalogSeconds, seconds, e)
		}
		e = f.db.QueryRow(`SELECT count(*) FROM owned_activity_events WHERE account_id=$1::uuid AND action='end'`, f.actor).Scan(&events)
		if e != nil || events != ended {
			t.Fatal("end event counted more than once", events, ended, e)
		}
	})
	f := newActivityFixture(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.repo.Set(f.ctx, f.actor, activityRequest("race"), true, "")
			ids <- v.ID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate session", id, v)
		}
		id = v
	}
	q := activityRequest("race")
	q.CatalogKey = postString("different")
	if _, e := f.repo.Set(f.ctx, f.actor, q, true, ""); !errors.Is(e, activities.ErrConflict) {
		t.Fatal("duration reassigned to another catalog", e)
	}
	ends := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.repo.End(f.ctx, f.actor, "race"); ends <- e }()
	}
	wg.Wait()
	close(ends)
	success := 0
	for e := range ends {
		if e == nil {
			success++
		} else if !errors.Is(e, activities.ErrNotFound) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	var sessions, events, catalogs int
	if e := f.db.QueryRow(`SELECT (SELECT count(*) FROM owned_presence_activities WHERE account_id=$1),(SELECT count(*) FROM owned_activity_events WHERE account_id=$1 AND action='end'),(SELECT count(*) FROM owned_activity_catalog WHERE account_id=$1)`, f.actor).Scan(&sessions, &events, &catalogs); e != nil || sessions != 1 || events != 1 || catalogs != 1 {
		t.Fatal(sessions, events, catalogs, e)
	}
	rows, e := f.repo.Catalog(f.ctx, f.actor, activities.Filter{Take: 20})
	if e != nil || len(rows) != 1 || rows[0].SessionCount != 1 {
		t.Fatal(rows, e)
	}
	// An expired session does not invent a completion or duration; a new start is
	// a new session row and a second successful end adds exactly one count.
	newer := activityStart(t, f, "expired")
	query(t, f.db, `UPDATE owned_presence_activities SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, newer.ID)
	if _, e = f.repo.End(f.ctx, f.actor, "expired"); !errors.Is(e, activities.ErrNotFound) {
		t.Fatal(e)
	}
	renewed := activityStart(t, f, "expired")
	if renewed.ID == newer.ID {
		t.Fatal("revived expired session")
	}
	if _, e = f.repo.End(f.ctx, f.actor, "expired"); e != nil {
		t.Fatal(e)
	}
}
func TestPGActivityAuditRollbackAndForgedProof(t *testing.T) {
	t.Run("end_rolls_back_duration_and_tag_union", func(t *testing.T) {
		f := newActivityFixture(t)
		v := activityStart(t, f, "end-rollback")
		q := activityRequest("end-rollback")
		q.Tags = []activities.Tag{{Slug: "uncommitted-tag", Name: "Uncommitted"}}
		if _, e := f.repo.Set(f.ctx, f.actor, q, true, ""); e != nil {
			t.Fatal(e)
		}
		name := "activity_end_fail_" + strings.ReplaceAll(f.actor, "-", "")
		query(t, f.db, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic activity end failure'; END $$; CREATE TRIGGER %s BEFORE INSERT ON owned_activity_events FOR EACH ROW WHEN (NEW.account_id='%s'::uuid AND NEW.action='end') EXECUTE FUNCTION %s()`, name, name, f.actor, name))
		t.Cleanup(func() {
			query(t, f.db, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON owned_activity_events;DROP FUNCTION IF EXISTS %s()`, name, name))
		})
		if _, e := f.repo.End(f.ctx, f.actor, "end-rollback"); e == nil {
			t.Fatal("failed end audit committed")
		}
		catalog, e := f.repo.Catalog(f.ctx, f.actor, activities.Filter{Take: 20})
		if e != nil || len(catalog) != 1 || catalog[0].SessionCount != 0 || catalog[0].TotalSeconds != 0 || len(catalog[0].Tags) != 1 || catalog[0].Tags[0].Slug != "work" {
			t.Fatal("failed end retained accumulation", catalog, e)
		}
		rows, _, e := f.repo.List(f.ctx, f.actor, f.actor, activities.Filter{Take: 20})
		if e != nil || len(rows) != 1 || rows[0].ID != v.ID || rows[0].EndedAt != nil {
			t.Fatal("failed end changed activity", rows, e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1::uuid AND action='end'`, f.actor); n != 0 {
			t.Fatal(n)
		}
	})
	f := newActivityFixture(t)
	name := "activity_fail_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic activity audit failure'; END $$; CREATE TRIGGER %s BEFORE INSERT ON owned_activity_events FOR EACH ROW WHEN (NEW.account_id='%s'::uuid) EXECUTE FUNCTION %s()`, name, name, f.actor, name))
	t.Cleanup(func() {
		query(t, f.db, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON owned_activity_events;DROP FUNCTION IF EXISTS %s()`, name, name))
	})
	if _, e := f.repo.Set(f.ctx, f.actor, activityRequest("rollback"), true, ""); e == nil {
		t.Fatal("audit failure committed")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_presence_activities WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_catalog WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal(n)
	}
	if _, e := f.repo.Set(context.Background(), f.actor, activityRequest("forged"), true, ""); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("unbound proof", e)
	}
	if _, e := f.repo.Set(f.ctx, f.other, activityRequest("substitute"), true, ""); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("cross-account bound proof", e)
	}
	if _, e := activitypg.New(f.db, nil).Set(f.ctx, f.actor, activityRequest("unguarded"), true, ""); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestPGActivityWaitRechecksExpiryPrivacyAndOriginalProof(t *testing.T) {
	for _, operation := range []string{"list", "renew", "end"} {
		t.Run("lease_after_artwork_wait_"+operation, func(t *testing.T) {
			f := newArtworkFixture(t)
			asset := artworkPost(t, f.app, f.token, f.data)
			body := fmt.Sprintf(`{"type":"coding","manual_id":"artwork-wait","catalog_key":"artwork-wait","large_image":%q}`, asset.Hash)
			v := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities/sessions/start", f.token, body, 200))
			proof, e := f.verifier.Authenticate(context.Background(), f.token)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 20*time.Second)
			defer cancel()
			repo := activitypg.New(f.db, f.guard)
			var fileID string
			if e = f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1::text`, asset.Hash).Scan(&fileID); e != nil {
				t.Fatal(e)
			}
			var deadline time.Time
			if e = f.db.QueryRow(`UPDATE owned_presence_activities SET lease_expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1::uuid RETURNING lease_expires_at`, v.ID).Scan(&deadline); e != nil {
				t.Fatal(e)
			}
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", fileID)
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "list":
					rows, _, err := repo.List(ctx, f.actor, f.actor, activities.Filter{Take: 20})
					if err == nil && len(rows) != 0 {
						err = fmt.Errorf("expired activity exposed after artwork wait")
					}
					done <- err
				case "renew":
					q := activityRequest("artwork-wait")
					q.CatalogKey = postString("artwork-wait")
					q.LargeImage = &asset.Hash
					_, err := repo.Set(ctx, f.actor, q, true, "")
					done <- err
				case "end":
					_, err := repo.End(ctx, f.actor, "artwork-wait")
					done <- err
				}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			organizationWaitExpired(t, ctx, f.db, deadline)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if operation == "list" && e != nil || operation != "list" && !errors.Is(e, activities.ErrNotFound) {
				t.Fatal(operation, e)
			}
			if n := commerceCount(t, f.db, `SELECT session_count FROM owned_activity_catalog WHERE id=$1::uuid`, *v.CatalogID); n != 0 {
				t.Fatal("expired wait accumulated catalog duration", n)
			}
			if n := commerceCount(t, f.db, `SELECT revision FROM owned_presence_activities WHERE id=$1::uuid`, v.ID); n != 1 {
				t.Fatal("expired wait committed a mutation", n)
			}
		})
	}
	t.Run("privacy_after_account_wait", func(t *testing.T) {
		f := newActivityFixture(t)
		v := activityStart(t, f, "visible")
		locks := atomicAuthorityFixture{db: f.db, ctx: f.ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
		if _, e := blocker.ExecContext(f.ctx, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1,'private',clock_timestamp())ON CONFLICT(account_id)DO UPDATE SET status_visibility='private'`, f.actor); e != nil {
			t.Fatal(e)
		}
		type result struct {
			v []activities.Activity
			e error
		}
		done := make(chan result, 1)
		go func() {
			rows, _, e := f.repo.List(context.Background(), "", f.actor, activities.Filter{Take: 20})
			done <- result{rows, e}
		}()
		waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		res := <-done
		if res.e != nil || len(res.v) != 0 {
			t.Fatal("stale public activity", v.ID, res)
		}
	})
	t.Run("lease_after_catalog_wait", func(t *testing.T) {
		f := newActivityFixture(t)
		v := activityStart(t, f, "lease")
		var deadline time.Time
		if e := f.db.QueryRow(`UPDATE owned_presence_activities SET lease_expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING lease_expires_at`, v.ID).Scan(&deadline); e != nil {
			t.Fatal(e)
		}
		locks := atomicAuthorityFixture{db: f.db, ctx: f.ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_activity_catalog", *v.CatalogID)
		done := make(chan error, 1)
		go func() { _, e := f.repo.End(f.ctx, f.actor, "lease"); done <- e }()
		waitAtomicBlocked(t, locks, pid, "owned_activity_catalog")
		organizationWaitExpired(t, f.ctx, f.db, deadline)
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e := <-done; !errors.Is(e, activities.ErrNotFound) {
			t.Fatal(e)
		}
		if n := commerceCount(t, f.db, `SELECT session_count FROM owned_activity_catalog WHERE id=$1`, *v.CatalogID); n != 0 {
			t.Fatal("expired end committed stats", n)
		}
	})
	t.Run("original_proof_after_catalog_wait", func(t *testing.T) {
		f := newActivityFixture(t)
		cfg := f.config
		cfg.AccessTTL = 5 * time.Second
		issuer, e := authority.New(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		token := pgIssue(t, issuer, f.actor).Token
		verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := verifier.Authenticate(f.ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
		ctx := guard.Bind(f.ctx, proof)
		repo := activitypg.New(f.db, guard)
		raw, e := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
		if e != nil {
			t.Fatal(e)
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if e = json.Unmarshal(raw, &claims); e != nil {
			t.Fatal(e)
		}
		deadline := time.Unix(claims.Exp, 0)
		v := activityStart(t, f, "proof")
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_activity_catalog", *v.CatalogID)
		done := make(chan error, 1)
		go func() { _, e := repo.End(ctx, f.actor, "proof"); done <- e }()
		waitAtomicBlocked(t, locks, pid, "owned_activity_catalog")
		var current bool
		if e = f.db.QueryRowContext(ctx, `SELECT clock_timestamp()<$1::timestamptz`, deadline).Scan(&current); e != nil || !current {
			t.Fatal("original proof expired before the catalog wait", e)
		}
		organizationWaitExpired(t, ctx, f.db, deadline)
		if e := blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e := <-done; !errors.Is(e, authority.ErrExpired) {
			t.Fatal("expired original proof accepted or wrong failure", e)
		}
		if n := commerceCount(t, f.db, `SELECT session_count FROM owned_activity_catalog WHERE id=$1`, *v.CatalogID); n != 0 {
			t.Fatal(n)
		}
	})
}

func TestPGActivityArtworkOwnershipRetentionAndReadProjection(t *testing.T) {
	t.Run("earlier_reference_expires_during_later_file_wait", func(t *testing.T) {
		f := newArtworkFixture(t)
		first := artworkPost(t, f.app, f.token, f.data)
		second := artworkPost(t, f.app, f.token, artworkPNG(t, authorityUUID(t)))
		if second.Hash < first.Hash {
			first, second = second, first
		}
		body := fmt.Sprintf(`{"type":"coding","manual_id":"two-images","large_image":%q,"small_image":%q}`, first.Hash, second.Hash)
		created := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities", f.token, body, 200))
		proof, e := f.verifier.Authenticate(context.Background(), f.token)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 20*time.Second)
		defer cancel()
		var deadline time.Time
		e = f.db.QueryRow(`UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-(retention_seconds * interval '1 second')+interval '2 seconds' WHERE hash=$1::text RETURNING last_referenced_at+(retention_seconds * interval '1 second')`, first.Hash).Scan(&deadline)
		if e != nil {
			t.Fatal(e)
		}
		var fileID string
		if e = f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1::text`, second.Hash).Scan(&fileID); e != nil {
			t.Fatal(e)
		}
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_media_files", fileID)
		type result struct {
			rows []activities.Activity
			err  error
		}
		done := make(chan result, 1)
		go func() {
			rows, _, err := activitypg.New(f.db, f.guard).List(ctx, f.actor, f.actor, activities.Filter{Take: 20})
			done <- result{rows, err}
		}()
		waitAtomicBlocked(t, locks, pid, "owned_media_files")
		organizationWaitExpired(t, ctx, f.db, deadline)
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		res := <-done
		if res.err != nil || len(res.rows) != 1 || res.rows[0].ID != created.ID || res.rows[0].LargeImage != nil || res.rows[0].SmallImage == nil || *res.rows[0].SmallImage != second.Hash {
			t.Fatal("stale artwork retention leaked after final wait", res)
		}
	})
	f := newArtworkFixture(t)
	asset := artworkPost(t, f.app, f.token, f.data)
	body := fmt.Sprintf(`{"type":"coding","manual_id":"image","title":"Canonical artwork","large_image":%q}`, asset.Hash)
	a := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities", f.token, body, 200))
	if a.LargeImage == nil || *a.LargeImage != asset.Hash {
		t.Fatal(a)
	}
	backendCall(t, f.app, "POST", "/api/activities", f.otherToken, body, 400)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_presence_activities WHERE account_id=$1`, f.other); n != 0 {
		t.Fatal("foreign artwork mutation committed", n)
	}
	query(t, f.db, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-interval '31 days' WHERE hash=$1`, asset.Hash)
	w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 200)
	if strings.Contains(w.Body.String(), asset.Hash) {
		t.Fatal("expired hash exposed", w.Body.String())
	}
	backendCall(t, f.app, "POST", "/api/activities", f.token, body, 400)
	query(t, f.db, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp() WHERE hash=$1`, asset.Hash)
	query(t, f.db, `UPDATE owned_media_files SET state='deleting' WHERE id=(SELECT file_id FROM owned_presence_artworks WHERE hash=$1)`, asset.Hash)
	w = backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 200)
	if strings.Contains(w.Body.String(), asset.Hash) {
		t.Fatal("deleted canonical file exposed", w.Body.String())
	}
}
func TestPGActivityBlockPunishmentAndScopeWithdrawal(t *testing.T) {
	for _, change := range []string{"scope", "revocation", "disabled"} {
		t.Run("refreshed_source_"+change, func(t *testing.T) {
			f := newActivityFixture(t)
			pair := pgIssue(t, f.issuer, f.actor)
			body := `{"type":"coding","manual_id":"refresh","catalog_key":"refresh","visibility":1}`
			first := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities/sessions/start", pair.Token, body, 200))
			rotated, e := f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, "")
			if e != nil {
				t.Fatal(e)
			}
			backendCall(t, f.app, "GET", "/api/activities", pair.Token, "", 401)
			backendCall(t, f.app, "POST", "/api/activities/sessions/start", pair.Token, body, 401)
			if w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 200); !strings.Contains(w.Body.String(), first.ID) {
				t.Fatal("refresh invalidated existing admission", w.Body.String())
			}
			renewed := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/passport/activities/sessions/start", rotated.Token, `{"manual_id":"refresh"}`, 200))
			created := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities", rotated.Token, `{"manual_id":"after-refresh","type":"coding"}`, 200))
			if renewed.ID != first.ID || renewed.StartedAt == nil || !renewed.StartedAt.Equal(*first.StartedAt) {
				t.Fatal("refresh restarted activity", first, renewed)
			}
			if w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 200); !strings.Contains(w.Body.String(), renewed.ID) || !strings.Contains(w.Body.String(), created.ID) {
				t.Fatal("refreshed admission recorded token generation", w.Body.String())
			}
			verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
			if e != nil {
				t.Fatal(e)
			}
			proof, e := verifier.Authenticate(f.ctx, rotated.Token)
			if e != nil {
				t.Fatal(e)
			}
			status := 200
			switch change {
			case "scope":
				e = authoritypg.New(f.db).WithinAccount(f.ctx, f.actor, func(tx authority.Transaction) error {
					session, err := tx.Session(proof.Session.ID)
					if err != nil {
						return err
					}
					session.Scopes = []string{"accounts.*"}
					return tx.SaveSession(session)
				})
			case "revocation":
				e = f.issuer.Revoke(f.ctx, rotated.Token, "current")
			case "disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
				status = 404
			}
			if e != nil {
				t.Fatal(e)
			}
			w := backendCall(t, f.app, "GET", "/passport/activities/"+f.actor, f.otherToken, "", status)
			if strings.Contains(w.Body.String(), first.ID) || strings.Contains(w.Body.String(), created.ID) {
				t.Fatal("withdrawn source retained activity", change, w.Body.String())
			}
		})
	}
	f := newActivityFixture(t)
	v := activityStart(t, f, "policy")
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{"expires_in":"30m"}`, 200)
	backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 404)
	query(t, f.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
	if w := backendCall(t, f.app, "GET", "/api/activities/"+f.actor, f.otherToken, "", 200); !strings.Contains(w.Body.String(), v.ID) {
		t.Fatal("expired block remained effective", w.Body.String())
	}
	pair, e := f.issuer.IssueSession(f.ctx, f.actor, authority.SessionOptions{Label: "activity scope fence", Scopes: []string{"accounts.*"}})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "PUT", "/api/activities/"+v.ID, pair.Token, `{"title":"scope substitution"}`, 403)
	backendCall(t, f.app, "DELETE", "/api/activities?id="+v.ID, pair.Token, "", 403)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at,expires_at)VALUES($1,'disable_account',clock_timestamp(),clock_timestamp()+interval '1 hour')`, f.actor)
	backendCall(t, f.app, "GET", "/api/activities/"+f.actor, "", "", 404)
	backendCall(t, f.app, "GET", "/api/activities/catalog", f.token, "", 423)
}
func TestPGActivitySavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Other, Token, OtherToken string
		Authority                       restartAuthoritySnapshot
		Activity                        activities.Activity
		Catalog                         []activities.Catalog
		Stats                           map[string]int64
		Events                          int
	}
	var saved record
	if phase == "before" {
		f := newActivityFixture(t)
		a := activityStart(t, f, "restart")
		ended, e := f.repo.End(f.ctx, f.actor, "restart")
		if e != nil {
			t.Fatal(e)
		}
		catalog, e := f.repo.Catalog(f.ctx, f.actor, activities.Filter{Take: 20})
		if e != nil {
			t.Fatal(e)
		}
		stats, e := f.repo.Stats(f.ctx, f.actor, activities.Filter{}, false)
		if e != nil {
			t.Fatal(e)
		}
		if a.ID != ended.ID {
			t.Fatal(a, ended)
		}
		saved = record{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.config), Activity: ended, Catalog: catalog, Stats: stats, Events: commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, f.actor)}
		saveRestartFixture(t, "activities-state.json", saved)
		return
	}
	loadRestartFixture(t, "activities-state.json", &saved)
	cfg := restoreAuthoritySnapshot(t, saved.Authority)
	if cfg.SigningKey != nil {
		t.Fatal("restart snapshot acquired private signing material")
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), saved.Token)
	if e != nil {
		t.Fatal("original credential unavailable", e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 15*time.Second)
	defer cancel()
	repo := activitypg.New(db, guard)
	rows, _, e := repo.List(ctx, saved.Actor, saved.Actor, activities.Filter{Take: 20, IncludeExpired: true})
	if e != nil || len(rows) != 1 || rows[0].ID != saved.Activity.ID || rows[0].EndedAt == nil || !rows[0].EndedAt.Equal(*saved.Activity.EndedAt) {
		t.Fatal(rows, e)
	}
	catalog, e := repo.Catalog(ctx, saved.Actor, activities.Filter{Take: 20})
	if e != nil {
		t.Fatal(e)
	}
	stats, e := repo.Stats(ctx, saved.Actor, activities.Filter{}, false)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := json.Marshal(saved.Catalog)
	got, _ := json.Marshal(catalog)
	if string(want) != string(got) {
		t.Fatal("catalog changed across real restart")
	}
	want, _ = json.Marshal(saved.Stats)
	got, _ = json.Marshal(stats)
	if string(want) != string(got) {
		t.Fatal("stats changed across restart")
	}
	if _, e = repo.End(ctx, saved.Actor, "restart"); !errors.Is(e, activities.ErrNotFound) {
		t.Fatal("ended session replayed", e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, saved.Actor); n != saved.Events {
		t.Fatal("event replay across restart", n)
	}
	app := backendApp(t, db, cfg)
	backendCall(t, app, "GET", "/api/activities/catalog", saved.OtherToken, "", 200)
	if w := backendCall(t, app, "GET", "/api/activities/catalog", saved.Token, "", 200); !strings.Contains(w.Body.String(), catalog[0].ID) {
		t.Fatal(w.Body.String())
	}
	expired := cfg
	expired.Now = func() time.Time { return saved.Authority.Now.Add(2 * time.Hour) }
	expiredVerifier, e := authority.NewVerifier(authoritypg.New(db), expired)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = expiredVerifier.Authenticate(ctx, saved.Token); e == nil {
		t.Fatal("saved original proof deadline lost")
	}
}
