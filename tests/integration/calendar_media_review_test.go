// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/calendarmediapg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/notabledays"
)

type calendarReadGate struct {
	entered, release chan struct{}
	count            atomic.Int64
	at               int64
	once             sync.Once
}

func newCalendarReadGate(t *testing.T, at int64) *calendarReadGate {
	t.Helper()
	g := &calendarReadGate{entered: make(chan struct{}), release: make(chan struct{}), at: at}
	t.Cleanup(g.unblock)
	return g
}
func (g *calendarReadGate) unblock() { g.once.Do(func() { close(g.release) }) }
func (g *calendarReadGate) pause(ctx context.Context) error {
	if g.count.Add(1) != g.at {
		return nil
	}
	close(g.entered)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *calendarReadGate) wait(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-g.entered:
	case <-ctx.Done():
		t.Fatal("read never reached deterministic wait", ctx.Err())
	}
}

type calendarTrackedReader struct {
	io.ReadSeekCloser
	closed *atomic.Int64
}

func (r calendarTrackedReader) Close() error { r.closed.Add(1); return r.ReadSeekCloser.Close() }

type calendarStorageGate struct {
	media.ObjectStore
	gate   *calendarReadGate
	closed atomic.Int64
}

func (s *calendarStorageGate) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	body, err := s.ObjectStore.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	tracked := calendarTrackedReader{body, &s.closed}
	if err = s.gate.pause(ctx); err != nil {
		tracked.Close()
		return nil, err
	}
	return tracked, nil
}

func TestPGCalendarMediaStorageWaitRevalidatesAudienceAndProof(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		for _, change := range []string{"private", "detach", "replace", "delete", "disabled", "expired", "retained"} {
			if anonymous && change == "expired" {
				continue
			}
			name := change
			if anonymous {
				name += "_anonymous"
			}
			t.Run(name, func(t *testing.T) {
				f := newCalendarMediaFixture(t)
				file, replacement := f.upload(t, f.token), f.upload(t, f.token)
				event := f.create(t, file, notabledays.Public)
				clock := &atomic.Int64{}
				clock.Store(f.now.Unix())
				f.authConfig.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
				f.calendarFixture = calendarPool(t, f.calendarFixture, f.db)
				objects, err := mediastore.NewFilesystem(f.objects, 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { objects.Close() })
				gate := newCalendarReadGate(t, 1)
				store := &calendarStorageGate{ObjectStore: objects, gate: gate}
				repo, err := mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				repo.WithReadVisibility(calendarmediapg.New(f.db))
				base, actor := f.otherCtx, f.other
				if anonymous {
					base, actor = context.Background(), ""
				}
				ctx, cancel := context.WithTimeout(base, 10*time.Second)
				defer cancel()
				type result struct {
					file media.File
					body io.ReadSeekCloser
					err  error
				}
				done := make(chan result, 1)
				go func() { v, body, err := repo.Open(ctx, actor, file); done <- result{v, body, err} }()
				gate.wait(t, ctx)
				switch change {
				case "private":
					backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"visibility": notabledays.Private}, 200)
				case "detach":
					backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": nil}, 200)
				case "replace":
					backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"icon_id": replacement}, 200)
				case "delete":
					backendCall(t, f.app, "DELETE", "/api/accounts/me/calendar/events/"+event.ID, f.token, "", 204)
				case "disabled":
					query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
				case "expired":
					clock.Add(601)
				}
				gate.unblock()
				got := <-done
				if change == "retained" {
					if got.err != nil || got.file.ID != file || got.body == nil {
						t.Fatal("legitimate grant lost", got.err, got.file)
					}
					b, err := io.ReadAll(got.body)
					got.body.Close()
					if err != nil || string(b) != string(profilePNG(t)) {
						t.Fatal("saved bytes changed", err)
					}
					return
				}
				want := error(media.ErrNotFound)
				if change == "expired" {
					want = authority.ErrExpired
				}
				if got.body != nil {
					got.body.Close()
					t.Fatal("denied read returned a reader")
				}
				if !errors.Is(got.err, want) || got.file != (media.File{}) || store.closed.Load() != 1 {
					t.Fatal("storage waiter leaked metadata or handle", got.err, got.file, store.closed.Load())
				}
			})
		}
	}
}

type calendarProjectionGate struct {
	notabledaypg.EventMediaStore
	gate *calendarReadGate
}

func (s calendarProjectionGate) Load(ctx context.Context, tx *sql.Tx, actor string, v *notabledays.Event) error {
	if err := s.EventMediaStore.Load(ctx, tx, actor, v); err != nil {
		return err
	}
	return s.gate.pause(ctx)
}

func TestPGCalendarMediaProjectionWaitRevalidatesCompleteResponse(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		for _, operation := range []string{"detail", "list", "recurrence", "month", "merged", "search", "countdown", "list_final_validation", "list_final_file_deletion"} {
			if anonymous && operation == "search" {
				continue
			}
			name := operation
			if anonymous {
				name += "_anonymous"
			}
			t.Run(name, func(t *testing.T) {
				f := newCalendarMediaFixture(t)
				file := f.upload(t, f.token)
				event := f.create(t, file, notabledays.Public)
				second := f.create(t, file, notabledays.Public)
				if second.ID < event.ID {
					event = second
				}
				calendarFriend(t, f.calendarFixture)
				root := "/api/accounts/" + f.actor + "/calendar"
				path, at := root+"/events/"+event.ID, int64(2)
				switch operation {
				case "detail":
					at = 1
				case "list":
					path = root + "/events"
				case "list_final_validation", "list_final_file_deletion":
					path, at = root+"/events", 4
				case "recurrence":
					path = root + "/events?startTime=2026-10-08T00%3A00%3A00Z&endTime=2026-10-12T00%3A00%3A00Z"
				case "month":
					path = root + "?year=2026&month=10"
				case "merged":
					path = root + "/merged?year=2026&month=10"
				case "search":
					path = "/api/accounts/me/calendar/search?query=Synthetic&startTime=2026-10-01T00%3A00%3A00Z&endTime=2026-11-01T00%3A00%3A00Z"
				case "countdown":
					path = root + "/countdown?includeNotableDays=false&take=100"
				}
				gate := newCalendarReadGate(t, at)
				f.repo.WithMedia(calendarProjectionGate{calendarmediapg.New(f.db), gate})
				token := f.otherToken
				if anonymous {
					token = ""
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					w := httptest.NewRecorder()
					calendarHTTP(f.calendarFixture).ServeHTTP(w, fixtureNoBodyRequest("GET", path, token).WithContext(ctx))
					done <- w
				}()
				gate.wait(t, ctx)
				want := 404
				if operation == "list_final_file_deletion" {
					backendCall(t, f.app, "DELETE", "/api/files/"+file, f.token, "", 200)
					want = 409
				} else {
					backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"visibility": notabledays.Private}, 200)
				}
				gate.unblock()
				w := <-done
				if w.Code != want || strings.Contains(w.Body.String(), event.ID) || strings.Contains(w.Body.String(), file) || strings.Contains(w.Body.String(), event.Title) {
					t.Fatal("assembled response leaked withdrawn event", w.Code, w.Body.String())
				}
			})
		}
	}
}

type calendarActivityGate struct {
	notabledaypg.CanonicalActivityOverlay
	gate *calendarReadGate
}

func (a calendarActivityGate) CalendarActivity(ctx context.Context, tx *sql.Tx, owner, viewer string, from, to time.Time, zone string) ([]notabledays.Overlay, error) {
	v, err := a.CanonicalActivityOverlay.CalendarActivity(ctx, tx, owner, viewer, from, to, zone)
	if err == nil {
		err = a.gate.pause(ctx)
	}
	return v, err
}
func TestPGCalendarActivityProjectionWaitRevalidatesPrivacy(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		for _, operation := range []string{"month", "merged"} {
			name := operation
			if anonymous {
				name += "_anonymous"
			}
			t.Run(name, func(t *testing.T) {
				f := newCalendarFixture(t)
				status := authorityUUID(t)
				query(t, f.db, `INSERT INTO owned_presence_statuses(id,account_id,attitude,type,label,created_at,updated_at)VALUES($1::uuid,$2::uuid,0,0,'Withdrawn status',$3::timestamptz,$3::timestamptz)`, status, f.actor, f.now)
				query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1::uuid,'public',$2::timestamptz)`, f.actor, f.now)
				gate := newCalendarReadGate(t, 1)
				f.config.Activity = calendarActivityGate{gate: gate}
				f = calendarPool(t, f, f.db)
				path := "/api/accounts/" + f.actor + "/calendar"
				if operation == "merged" {
					path += "/merged"
				}
				path += "?year=2026&month=10"
				token := f.otherToken
				if anonymous {
					token = ""
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					w := httptest.NewRecorder()
					calendarHTTP(f).ServeHTTP(w, fixtureNoBodyRequest("GET", path, token).WithContext(ctx))
					done <- w
				}()
				gate.wait(t, ctx)
				query(t, f.db, `UPDATE owned_presence_preferences SET status_visibility='private' WHERE account_id=$1::uuid`, f.actor)
				gate.unblock()
				w := <-done
				if w.Code != 409 || strings.Contains(w.Body.String(), status) || strings.Contains(w.Body.String(), "Withdrawn status") {
					t.Fatal("assembled response leaked withdrawn activity", w.Code, w.Body.String())
				}
			})
		}
	}
}

type calendarVisibilityGate struct {
	mediapg.ReadVisibility
	gate *calendarReadGate
	fail atomic.Bool
}

func (v *calendarVisibilityGate) CanReadInTransaction(ctx context.Context, tx *sql.Tx, actor, id string) (bool, error) {
	allowed, err := v.ReadVisibility.CanReadInTransaction(ctx, tx, actor, id)
	if err != nil {
		return false, err
	}
	if err = v.gate.pause(ctx); err != nil {
		return false, err
	}
	if v.fail.Load() {
		return false, media.ErrUnavailable
	}
	return allowed, nil
}
func TestPGCalendarMediaPolicyWaitReturnsNoMetadataOnFailure(t *testing.T) {
	for _, change := range []string{"expired", "private", "policy_error", "disabled", "disabled_final_policy"} {
		t.Run(change, func(t *testing.T) {
			f := newCalendarMediaFixture(t)
			file := f.upload(t, f.token)
			event := f.create(t, file, notabledays.Public)
			clock := &atomic.Int64{}
			clock.Store(f.now.Unix())
			f.authConfig.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
			f.calendarFixture = calendarPool(t, f.calendarFixture, f.db)
			objects, err := mediastore.NewFilesystem(f.objects, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { objects.Close() })
			repo, err := mediapg.New(f.db, objects, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			at := int64(1)
			if change == "disabled_final_policy" {
				at = 2
			}
			gate := newCalendarReadGate(t, at)
			visibility := &calendarVisibilityGate{ReadVisibility: calendarmediapg.New(f.db), gate: gate}
			repo.WithReadVisibility(visibility)
			ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
			defer cancel()
			type result struct {
				file media.File
				err  error
			}
			done := make(chan result, 1)
			go func() { file, err := repo.Info(ctx, f.other, file); done <- result{file, err} }()
			gate.wait(t, ctx)
			want := error(media.ErrNotFound)
			switch change {
			case "expired":
				clock.Add(601)
				want = authority.ErrExpired
			case "private":
				backendJSONCall(t, f.app, "PUT", "/api/accounts/me/calendar/events/"+event.ID, f.token, map[string]any{"visibility": notabledays.Private}, 200)
			case "disabled", "disabled_final_policy":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.actor)
			case "policy_error":
				visibility.fail.Store(true)
				want = media.ErrUnavailable
			}
			gate.unblock()
			got := <-done
			if !errors.Is(got.err, want) || got.file != (media.File{}) {
				t.Fatal("failed policy exposed metadata", got.err, got.file)
			}
		})
	}
}
