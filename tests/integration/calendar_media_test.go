// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/calendarmediapg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type calendarMediaFixture struct {
	calendarFixture
	app     *application.Backend
	objects string
}

func newCalendarMediaFixture(t *testing.T) *calendarMediaFixture {
	t.Helper()
	f := &calendarMediaFixture{calendarFixture: newCalendarFixture(t), objects: t.TempDir()}
	for _, actor := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), actor, []authority.Rule{{Pattern: "calendar.*"}, {Pattern: "media.files.*"}}, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_media_quotas(account_id,limit_bytes)VALUES($1::uuid,1048576)`, actor)
	}
	f.open(t)
	return f
}
func (f *calendarMediaFixture) open(t *testing.T) {
	t.Helper()
	if e := os.Chmod(f.objects, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(f.objects, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	f.app, e = application.NewPostgres(f.db, nil, application.Config{Authority: f.authConfig, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: store, MaxMediaBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.repo.WithMedia(calendarmediapg.New(f.db))
}
func (f *calendarMediaFixture) upload(t *testing.T, token string) string {
	t.Helper()
	return profileUpload(t, f.app, token, "image/png", profilePNG(t))
}
func calendarMediaID(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("invalid file projection %T", v)
	}
	id, ok := m["id"].(string)
	if !ok {
		t.Fatal(m)
	}
	return id
}
func (f *calendarMediaFixture) create(t *testing.T, file string, visibility int) notabledays.Event {
	t.Helper()
	body := map[string]any{"title": "Synthetic calendar image", "start_time": "2026-10-08T13:00:00Z", "end_time": "2026-10-08T14:00:00Z", "time_zone": "America/New_York", "visibility": visibility, "icon_id": file, "recurrence": map[string]any{"frequency": 1, "interval": 1, "occurrences": 3}}
	return backendDecode[notabledays.Event](t, backendJSONCall(t, f.app, "POST", "/passport/accounts/me/calendar/events", f.token, body, 201))
}
func calendarMediaCount(t *testing.T, f *calendarMediaFixture, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if e := f.db.QueryRow(sql, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPGCalendarMediaComposedAudienceAndProjection(t *testing.T) {
	f := newCalendarMediaFixture(t)
	file := f.upload(t, f.token)
	for _, token := range []string{"", f.otherToken} {
		backendCall(t, f.app, "GET", "/api/files/"+file, token, "", 404)
	}
	event := f.create(t, file, notabledays.Friends)
	if calendarMediaID(t, event.Icon) != file || event.Background != nil {
		t.Fatal(event)
	}
	backendCall(t, f.app, "GET", "/api/files/"+file, f.otherToken, "", 404)
	calendarFriend(t, f.calendarFixture)
	for _, path := range []string{
		"/api/accounts/" + f.actor + "/calendar/events/" + event.ID,
		"/api/accounts/" + f.actor + "/calendar/events?startTime=2026-10-08T00%3A00%3A00Z&endTime=2026-10-12T00%3A00%3A00Z",
		"/api/accounts/" + f.actor + "/calendar?year=2026&month=10&timeZone=America%2FNew_York",
		"/api/accounts/" + f.actor + "/calendar/merged?year=2026&month=10",
		"/api/accounts/me/calendar/search?query=Synthetic&startTime=2026-10-01T00%3A00%3A00Z&endTime=2026-11-01T00%3A00%3A00Z",
		"/api/accounts/" + f.actor + "/calendar/countdown?includeNotableDays=false&take=100",
	} {
		w := backendCall(t, f.app, "GET", path, f.otherToken, "", 200)
		if !strings.Contains(w.Body.String(), file) {
			t.Fatal("missing media projection", path, w.Body.String())
		}
	}
	w := backendCall(t, f.app, "GET", "/api/files/"+file, f.otherToken, "", 200)
	if !bytes.Equal(w.Body.Bytes(), profilePNG(t)) || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Header(), w.Body.String())
	}
	backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"visibility": notabledays.Public}, 200)
	backendCall(t, f.app, "GET", "/api/files/"+file, "", "", 200)
	if _, e := socialpg.New(f.db).ChangeRelationship(context.Background(), f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/api/files/"+file, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+event.ID, f.otherToken, "", 404)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind)VALUES($1::uuid,'disable_account')`, f.actor)
	backendCall(t, f.app, "GET", "/api/files/"+file, "", "", 404)
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+event.ID, "", "", 404)
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account'`, f.actor)
	backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 204)
	backendCall(t, f.app, "GET", "/api/files/"+file, "", "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+file, f.token, "", 200)
	if n := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_event_media WHERE event_id=$1::uuid`, event.ID); n != 0 {
		t.Fatal("deleted event retained audience", n)
	}
}

func TestPGCalendarMediaOwnershipReplayQuotaAndRollback(t *testing.T) {
	f := newCalendarMediaFixture(t)
	file, second, foreign := f.upload(t, f.token), f.upload(t, f.token), f.upload(t, f.otherToken)
	textFile := profileUpload(t, f.app, f.token, "text/plain", []byte("not an image"))
	event := f.create(t, file, notabledays.Public)
	before := calendarMediaCount(t, f, `SELECT used_bytes FROM owned_media_quotas WHERE account_id=$1::uuid`, f.actor)
	for _, bad := range []struct {
		id     string
		status int
	}{{foreign, 403}, {textFile, 400}, {"https://example.invalid/image.png", 400}, {authorityUUID(t), 404}, {"", 400}} {
		backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"background_id": bad.id, "title": "Must roll back"}, bad.status)
	}
	// Readers may download a referenced file, but may never attach it as their own.
	body := map[string]any{"title": "Copied file", "start_time": "2026-10-08T13:00:00Z", "end_time": "2026-10-08T14:00:00Z", "icon_id": file}
	backendJSONCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.otherToken, body, 403)
	body["title"] = "Idempotent media"
	raw, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	first := backendDecode[notabledays.Event](t, calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-image-key", string(raw), 201))
	body["icon_id"] = strings.ToUpper(file)
	raw, _ = json.Marshal(body)
	replay := backendDecode[notabledays.Event](t, calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-image-key", string(raw), 201))
	if first.ID != replay.ID || calendarMediaID(t, replay.Icon) != file {
		t.Fatal(first, replay)
	}
	body["icon_id"] = second
	raw, _ = json.Marshal(body)
	calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-image-key", string(raw), 409)
	view := backendDecode[notabledays.Event](t, backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"background_id": second}, 200))
	if calendarMediaID(t, view.Icon) != file || calendarMediaID(t, view.Background) != second || view.Title != event.Title {
		t.Fatal(view)
	}
	constraint := "calendar_media_rollback_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_calendar_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_calendar_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	beforeEvents := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_events WHERE account_id=$1::uuid`, f.actor)
	calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-atomic-create", string(raw), 400)
	if n := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_events WHERE account_id=$1::uuid`, f.actor); n != beforeEvents {
		t.Fatal("failed create retained event", n)
	}
	if n := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_event_media l JOIN owned_calendar_events e ON e.id=l.event_id WHERE e.account_id=$1::uuid`, f.actor); n != 3 {
		t.Fatal("failed create retained attachment", n)
	}
	backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": second, "background_id": nil, "title": "Atomic failure"}, 400)
	backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 400)
	query(t, f.db, `ALTER TABLE owned_calendar_audit DROP CONSTRAINT `+constraint)
	view = backendDecode[notabledays.Event](t, backendCall(t, f.app, "GET", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 200))
	if calendarMediaID(t, view.Icon) != file || calendarMediaID(t, view.Background) != second || view.Title != event.Title || view.Version != 2 {
		t.Fatal("mutation escaped rollback", view)
	}
	view = backendDecode[notabledays.Event](t, backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": nil, "background_id": file}, 200))
	if view.Icon != nil || calendarMediaID(t, view.Background) != file {
		t.Fatal(view)
	}
	backendCall(t, f.app, "GET", "/api/files/"+second, "", "", 404)
	backendCall(t, f.app, "DELETE", "/api/files/"+file, f.token, "", 200)
	view = backendDecode[notabledays.Event](t, backendCall(t, f.app, "GET", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 200))
	if view.Icon != nil || view.Background != nil {
		t.Fatal("deleted file remained projected", view)
	}
	backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": file}, 404)
	body["icon_id"] = file
	raw, _ = json.Marshal(body)
	replay = backendDecode[notabledays.Event](t, calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-image-key", string(raw), 201))
	if replay.ID != first.ID || replay.Icon != nil {
		t.Fatal("replay revived deleted media", replay)
	}
	backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+first.ID, f.token, "", 204)
	calendarCall(t, f.app, "POST", "/api/accounts/me/calendar/events", f.token, "calendar-image-key", string(raw), 404)
	if n := calendarMediaCount(t, f, `SELECT used_bytes FROM owned_media_quotas WHERE account_id=$1::uuid`, f.actor); n != before-int64(len(profilePNG(t))) {
		t.Fatal("association changed quota", before, n)
	}
}

func TestPGCalendarMediaFileWaitRevalidatesStateAndAuthority(t *testing.T) {
	for _, change := range []string{"deleted", "expired"} {
		t.Run(change, func(t *testing.T) {
			f := newCalendarMediaFixture(t)
			file := f.upload(t, f.token)
			event := f.create(t, file, notabledays.Private)
			clock := &atomic.Int64{}
			clock.Store(f.now.Unix())
			f.authConfig.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
			f.calendarFixture = calendarPool(t, f.calendarFixture, f.db)
			f.repo.WithMedia(calendarmediapg.New(f.db))
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			if change == "deleted" {
				if _, e := blocker.Exec(`UPDATE owned_media_files SET state='deleting' WHERE id=$1::uuid`, file); e != nil {
					t.Fatal(e)
				}
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.repo.Execute(ctx, f.actor, notabledays.Command{Operation: "event.update", ID: event.ID, Patch: &notabledays.EventPatch{BackgroundID: &file}})
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if change == "expired" {
				clock.Add(601)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			want := error(notabledays.ErrNotFound)
			if change == "expired" {
				want = authority.ErrExpired
			}
			if e := <-done; !errors.Is(e, want) {
				t.Fatal(change, e)
			}
			if n := calendarMediaCount(t, f, `SELECT version FROM owned_calendar_events WHERE id=$1::uuid`, event.ID); n != 1 {
				t.Fatal("expired/deleted waiter mutated event", n)
			}
			if n := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_event_media WHERE event_id=$1::uuid AND slot='background'`, event.ID); n != 0 {
				t.Fatal("waiter attached", n)
			}
		})
	}
}

func TestPGCalendarMediaReadWaitObservesVisibilityWithdrawal(t *testing.T) {
	for _, change := range []string{"private", "detach", "delete"} {
		t.Run(change, func(t *testing.T) {
			f := newCalendarMediaFixture(t)
			file := f.upload(t, f.token)
			event := f.create(t, file, notabledays.Public)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			done := make(chan int, 1)
			go func() {
				w := httptest.NewRecorder()
				f.app.ServeHTTP(w, fixtureNoBodyRequest("GET", "/api/files/"+file, f.otherToken).WithContext(ctx))
				done <- w.Code
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			switch change {
			case "private":
				backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"visibility": notabledays.Private}, 200)
			case "detach":
				backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": nil}, 200)
			case "delete":
				backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 204)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if code := <-done; code != 404 {
				t.Fatal("stale calendar audience granted bytes", code)
			}
		})
	}
}

func TestPGCalendarMediaRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type saved struct {
		Actor, Other, Public, Private, Deleted, File, Hidden string
		Token, OtherToken                                    string
		Authority                                            restartAuthoritySnapshot
	}
	objects := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "calendar-media-objects")
	if phase == "before" {
		f := newCalendarMediaFixture(t)
		f.calendarFixture = calendarRestartAuthority(t, f.calendarFixture)
		if e := os.Mkdir(objects, 0700); e != nil {
			t.Fatal(e)
		}
		f.objects = objects
		f.open(t)
		file, hidden := f.upload(t, f.token), f.upload(t, f.token)
		public, private, deleted := f.create(t, file, notabledays.Public), f.create(t, hidden, notabledays.Private), f.create(t, hidden, notabledays.Public)
		backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+deleted.ID, f.token, "", 204)
		saveRestartFixture(t, "calendar-media.json", saved{Actor: f.actor, Other: f.other, Public: public.ID, Private: private.ID, Deleted: deleted.ID, File: file, Hidden: hidden, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.authConfig)})
		return
	}
	var state saved
	loadRestartFixture(t, "calendar-media.json", &state)
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	f := &calendarMediaFixture{calendarFixture: calendarFixture{db: db, actor: state.Actor, other: state.Other, authConfig: cfg, token: state.Token, otherToken: state.OtherToken, now: state.Authority.Now}, objects: objects}
	f.calendarFixture = calendarPool(t, f.calendarFixture, db)
	f.open(t)
	for _, entry := range []struct{ id, token, file string }{{state.Public, "", state.File}, {state.Private, f.token, state.Hidden}} {
		v := backendDecode[notabledays.Event](t, backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+entry.id, entry.token, "", 200))
		if calendarMediaID(t, v.Icon) != entry.file {
			t.Fatal(v)
		}
		w := backendCall(t, f.app, "GET", "/api/files/"+entry.file, entry.token, "", 200)
		if !bytes.Equal(w.Body.Bytes(), profilePNG(t)) {
			t.Fatal("saved object lost")
		}
	}
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+state.Private, f.otherToken, "", 404)
	backendCall(t, f.app, "GET", "/api/accounts/unknown/calendar/events/"+state.Deleted, f.token, "", 404)
	backendCall(t, f.app, "GET", "/api/files/"+state.Hidden, "", "", 404)
	events := backendDecode[[]notabledays.Event](t, backendCall(t, f.app, "GET", "/api/accounts/"+state.Actor+"/calendar/events?startTime=2026-10-08T00%3A00%3A00Z&endTime=2026-10-12T00%3A00%3A00Z", "", "", 200))
	if len(events) != 3 {
		t.Fatal(events)
	}
	for _, v := range events {
		if v.ID != state.Public || v.OccurrenceKey == "" || calendarMediaID(t, v.Icon) != state.File {
			t.Fatal(v)
		}
	}
	if n := calendarMediaCount(t, f, `SELECT count(*) FROM owned_calendar_event_media WHERE event_id=$1::uuid`, state.Deleted); n != 0 {
		t.Fatal("deleted grant revived", n)
	}
}

func TestPGCalendarMediaComposedReadProofWait(t *testing.T) {
	for _, change := range []string{"expired", "expired_info", "revoked"} {
		t.Run(change, func(t *testing.T) {
			f := newCalendarMediaFixture(t)
			file := f.upload(t, f.token)
			f.create(t, file, notabledays.Public)
			clock := &atomic.Int64{}
			clock.Store(f.now.Unix())
			f.authConfig.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
			f.open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_media_files", file
			if change == "revoked" {
				table, id = "owned_authority_accounts", f.other
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			if change == "revoked" {
				if _, e := blocker.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid`, f.other); e != nil {
					t.Fatal(e)
				}
			}
			path := "/api/files/" + file
			if change == "expired_info" {
				path += "/info"
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				f.app.ServeHTTP(w, fixtureNoBodyRequest("GET", path, f.otherToken).WithContext(ctx))
				done <- w
			}()
			waitAtomicBlocked(t, locks, pid, table)
			if strings.HasPrefix(change, "expired") {
				clock.Add(601)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			w := <-done
			want := 403
			if strings.HasPrefix(change, "expired") {
				want = 401
			}
			if w.Code != want || strings.Contains(w.Body.String(), file) || bytes.Contains(w.Body.Bytes(), profilePNG(t)) || strings.Contains(w.Body.String(), "synthetic-profile.png") {
				t.Fatal("stale proof disclosed media", w.Code, w.Body.String())
			}
			// Authentication failure never downgrades to the anonymous audience, while
			// a genuinely anonymous request retains this deliberately public grant.
			backendCall(t, f.app, "GET", "/api/files/"+file, "", "", 200)
		})
	}
}

func TestPGCalendarMediaRealmGrantCannotRedistribute(t *testing.T) {
	f := newRealmPublishingFixture(t)
	upload := mediaUpload("calendar-realm-source", string(profilePNG(t)), false)
	upload.Name = "synthetic.png"
	upload.MIMEType = "image/png"
	file, _, e := f.repo.Upload(f.ctx, f.actor, upload, bytes.NewReader(profilePNG(t)))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.privatePub.ID, posts.Patch{Content: postString("Synthetic private realm image"), Attachments: mediaIDs(file.ID)}); e != nil {
		t.Fatal(e)
	}
	links := calendarmediapg.New(f.db)
	f.repo.WithReadVisibility(mediapg.AnyReadVisibility{links, postmediapg.New(f.db, f.interactions).WithVisibility(f.social)})
	mediaRead(t, f.repo, f.otherCtx, f.other, file.ID, string(profilePNG(t)))
	calendar, e := notabledaypg.New(f.db, f.guard, notabledaypg.Config{Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	calendar.WithMedia(links)
	patch := eventPatch("Cannot redistribute realm image", notabledays.Public, f.config.Now())
	patch.IconID = &file.ID
	if _, e = calendar.Execute(f.otherCtx, f.other, notabledays.Command{Operation: "event.create", Patch: patch}); !errors.Is(e, notabledays.ErrForbidden) {
		t.Fatal("realm read became ownership", e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, body, e := f.repo.Open(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) || body != nil {
		if body != nil {
			body.Close()
		}
		t.Fatal("realm withdrawal ignored", e)
	}
}
