// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"sync"
	"testing"
	"time"
)

type locationFixture struct {
	db             *sql.DB
	r              *locationpg.Repository
	service        *authority.Service
	v              *authority.Verifier
	g              *authoritypg.TransactionGuard
	actors, tokens []string
	contexts       []context.Context
}

func newLocationFixture(t *testing.T, n int) locationFixture {
	t.Helper()
	db := postDB(t)
	query(t, db, socialpg.Schema)
	query(t, db, locationpg.Schema)
	f := locationFixture{db: db}
	for i := 0; i < 3; i++ {
		id := postAccount(t, db, n+i)
		f.actors = append(f.actors, id)
		if e := authoritypg.New(db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	f.service, f.v, f.g, _ = interactionAuthority(t, db, f.actors[0])
	f.r = locationpg.NewWithAuthority(db, f.g)
	for _, id := range f.actors {
		token := pgIssue(t, f.service, id).Token
		f.tokens = append(f.tokens, token)
		f.contexts = append(f.contexts, locationProof(t, f, token))
	}
	return f
}
func locationProof(t *testing.T, f locationFixture, token string) context.Context {
	t.Helper()
	q := httptest.NewRequest("GET", "/api/meets", nil)
	q.Header.Set("Authorization", "Bearer "+token)
	p, e := f.v.AuthenticateRequest(q)
	if e != nil {
		t.Fatal(e)
	}
	return f.g.Bind(context.Background(), p)
}
func locationMeet(t *testing.T, f locationFixture, v int) locations.Meet {
	t.Helper()
	m, e := f.r.CreateMeet(f.contexts[0], f.actors[0], locations.CreateMeet{Visibility: &v, LocationWKT: postString("POINT(10 20)"), Notes: postString("Synthetic rendezvous")})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func locationFriends(t *testing.T, f locationFixture, a, b int) {
	t.Helper()
	r := socialpg.New(f.db)
	if _, e := r.ChangeRelationship(context.Background(), f.actors[a], f.actors[b], social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.ChangeRelationship(context.Background(), f.actors[b], f.actors[a], social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
}
func TestPGLocationsMeetLifecycleAndPrivacy(t *testing.T) {
	f := newLocationFixture(t, 4500)
	m := locationMeet(t, f, locations.Private)
	locationFriends(t, f, 0, 1)
	if _, e := f.r.Meet(f.contexts[1], f.actors[1], m.ID, nil); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("private meet friendship leak", e)
	}
	if _, e := f.r.Complete(f.contexts[1], f.actors[1], m.ID); !errors.Is(e, locations.ErrForbidden) {
		t.Fatal("nonhost completion", e)
	}
	if _, e := f.r.Visibility(f.contexts[0], f.actors[0], m.ID, locations.Public); e != nil {
		t.Fatal(e)
	}
	if _, e := f.r.Meet(f.contexts[1], f.actors[1], m.ID, nil); e != nil {
		t.Fatal("friend public detail", e)
	}
	near := &locations.Point{Longitude: 10.001, Latitude: 20}
	far := &locations.Point{Longitude: 30, Latitude: 40}
	if _, e := f.r.Meet(f.contexts[2], f.actors[2], m.ID, far); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("distant discovery", e)
	}
	joined, e := f.r.Join(f.contexts[2], f.actors[2], m.ID, near)
	if e != nil || len(joined.Participants) != 1 {
		t.Fatal(joined, e)
	}
	joined, e = f.r.Join(f.contexts[2], f.actors[2], m.ID, nil)
	if e != nil || len(joined.Participants) != 1 {
		t.Fatal("duplicate join", joined, e)
	}
	if _, e = f.r.Visibility(f.contexts[0], f.actors[0], m.ID, locations.Private); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.Meet(f.contexts[1], f.actors[1], m.ID, near); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("location granted private access", e)
	}
	if _, e = f.r.Meet(f.contexts[2], f.actors[2], m.ID, nil); e != nil {
		t.Fatal("participant access", e)
	}
	if got, n, e := f.r.Meets(f.contexts[1], f.actors[1], locations.Page{Take: 20, Nearby: true, Radius: 1000, Origin: near}); e != nil || n != 0 || len(got) != 0 {
		t.Fatal("private nearby count", got, n, e)
	}
	done, e := f.r.Complete(f.contexts[0], f.actors[0], m.ID)
	if e != nil || done.Status != locations.Completed || done.CompletedAt == nil {
		t.Fatal(done, e)
	}
	if _, e = f.r.Join(f.contexts[2], f.actors[2], m.ID, nil); !errors.Is(e, locations.ErrConflict) {
		t.Fatal("terminal join", e)
	}
	if e = f.r.DeleteMeet(f.contexts[0], f.actors[0], m.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.Meet(f.contexts[0], f.actors[0], m.ID, nil); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("deleted meet", e)
	}
}
func TestPGLocationsMeetDeduplicationAndAuditRollback(t *testing.T) {
	f := newLocationFixture(t, 4510)
	db2 := database(t)
	g2 := authoritypg.NewTransactionGuard(authoritypg.New(db2), f.v)
	r2 := locationpg.NewWithAuthority(db2, g2)
	req := httptest.NewRequest("POST", "/api/meets", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens[0])
	proof, e := f.v.AuthenticateRequest(req)
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	out := make(chan locations.Meet, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, ctx := f.r, f.contexts[0]
			if i == 1 {
				r = r2
				ctx = g2.Bind(context.Background(), proof)
			}
			m, e := r.CreateMeet(ctx, f.actors[0], locations.CreateMeet{Notes: postString("same request")})
			out <- m
			errs <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	a, b := <-out, <-out
	if a.ID == "" || a.ID != b.ID {
		t.Fatal("concurrent duplicate", a, b)
	}
	query(t, f.db, `ALTER TABLE owned_location_audit ADD CONSTRAINT location_meet_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000004510'::uuid OR action<>'meet.complete')`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_location_audit DROP CONSTRAINT IF EXISTS location_meet_failure`)
	})
	if _, e = f.r.Complete(f.contexts[0], f.actors[0], a.ID); e == nil {
		t.Fatal("audit failure hidden")
	}
	m, e := f.r.Meet(f.contexts[0], f.actors[0], a.ID, nil)
	if e != nil || m.Status != locations.Active {
		t.Fatal("partial completion", m, e)
	}
	query(t, f.db, `UPDATE owned_meets SET expires_at=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, a.ID)
	m, e = f.r.Meet(f.contexts[0], f.actors[0], a.ID, nil)
	if e != nil || m.Status != locations.Expired {
		t.Fatal("durable deadline", m, e)
	}
	if _, e = f.r.Join(f.contexts[0], f.actors[0], a.ID, nil); !errors.Is(e, locations.ErrConflict) {
		t.Fatal("expired join", e)
	}
}
func TestPGLocationsPinsSessionPrivacyRateAndExpiry(t *testing.T) {
	f := newLocationFixture(t, 4520)
	p, e := f.r.PutPin(f.contexts[0], f.actors[0], "", "", locations.PinRequest{LocationWKT: postString("POINT(10 20)")})
	if e != nil || p.Visibility != locations.Private || p.DeviceID == "" {
		t.Fatal(p, e)
	}
	if _, e = f.r.Pin(f.contexts[1], f.actors[1], p.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("private pin", e)
	}
	if _, e = f.r.PutPin(f.contexts[0], f.actors[0], "", p.ID, locations.PinRequest{LocationWKT: postString("POINT(11 21)")}); !errors.Is(e, locations.ErrRateLimit) {
		t.Fatal("persistent update rate", e)
	}
	query(t, f.db, `UPDATE owned_location_pins SET last_heartbeat_at=CURRENT_TIMESTAMP-interval '6 seconds' WHERE id=$1`, p.ID)
	p, e = f.r.PutPin(f.contexts[0], f.actors[0], "", p.ID, locations.PinRequest{Visibility: postInt(locations.Public), LocationWKT: postString("POINT(10.001 20.001)")})
	if e != nil {
		t.Fatal(e)
	}
	visible, e := f.r.Pin(f.contexts[1], f.actors[1], p.ID)
	if e != nil || visible.DeviceID != "" || visible.LocationWKT == nil {
		t.Fatal("public projection", visible, e)
	}
	second := pgIssue(t, f.service, f.actors[0])
	secondCtx := locationProof(t, f, second.Token)
	if e = f.r.DeletePin(secondCtx, f.actors[0], p.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("different session deleted pin", e)
	}
	if e = f.r.Disconnect(f.contexts[0], f.actors[0], p.ID, true); e != nil {
		t.Fatal(e)
	}
	offline, e := f.r.Pin(f.contexts[1], f.actors[1], p.ID)
	if e != nil || offline.Status != locations.PinOffline || offline.ExpiresAt == nil {
		t.Fatal(offline, e)
	}
	query(t, f.db, `UPDATE owned_location_pins SET expires_at=CURRENT_TIMESTAMP-interval '1 second' WHERE id=$1`, p.ID)
	if _, e = f.r.Pin(f.contexts[0], f.actors[0], p.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("expired pin", e)
	}
	if _, e = f.r.Expire(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	var status int
	if e = f.db.QueryRow(`SELECT status FROM owned_location_pins WHERE id=$1`, p.ID).Scan(&status); e != nil || status != locations.PinRemoved {
		t.Fatal(status, e)
	}
}
func TestPGLocationsMeetPinPrivacyAndBlock(t *testing.T) {
	f := newLocationFixture(t, 4530)
	m := locationMeet(t, f, locations.Public)
	near := &locations.Point{Longitude: 10, Latitude: 20}
	if _, e := f.r.Join(f.contexts[1], f.actors[1], m.ID, near); e != nil {
		t.Fatal(e)
	}
	p, e := f.r.PutPin(f.contexts[1], f.actors[1], m.ID, "", locations.PinRequest{LocationWKT: postString("POINT(10 20)")})
	if e != nil {
		t.Fatal(e)
	}
	got, e := f.r.Meet(f.contexts[0], f.actors[0], m.ID, nil)
	if e != nil || len(got.Pins) != 0 {
		t.Fatal("host saw participant private pin", got, e)
	}
	if _, e = f.r.Pin(f.contexts[0], f.actors[0], p.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("private participant pin leaked", e)
	}
	if _, e = socialpg.New(f.db).ChangeRelationship(context.Background(), f.actors[0], f.actors[1], social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.Meet(f.contexts[1], f.actors[1], m.ID, nil); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("blocked participant", e)
	}
	if _, e = f.r.Pin(f.contexts[1], f.actors[1], p.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("blocked meet trail", e)
	}
}
func TestPGLocationsNFCOwnershipAndResolution(t *testing.T) {
	f := newLocationFixture(t, 4540)
	tag, e := f.r.RegisterTag(f.contexts[0], f.actors[0], locations.RegisterTag{UID: "04aabbcc4540", Label: postString("Synthetic plain association")})
	if e != nil || tag.Encrypted || tag.Locked {
		t.Fatal(tag, e)
	}
	resolved, e := f.r.ResolveTag(context.Background(), "", tag.UID, false)
	if e != nil || resolved.Account.ID != f.actors[0] || !resolved.Claimed || resolved.Friend {
		t.Fatal(resolved, e)
	}
	if _, e = f.r.UpdateTag(f.contexts[1], f.actors[1], tag.ID, locations.TagPatch{Label: postString("hijack")}); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("foreign tag edit", e)
	}
	if e = f.r.DeleteTag(f.contexts[1], f.actors[1], tag.ID); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("foreign tag delete", e)
	}
	locationFriends(t, f, 0, 1)
	resolved, e = f.r.ResolveTag(f.contexts[1], f.actors[1], tag.ID, true)
	if e != nil || !resolved.Friend {
		t.Fatal("canonical friend projection", resolved, e)
	}
	if _, e = socialpg.New(f.db).ChangeRelationship(context.Background(), f.actors[0], f.actors[1], social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.ResolveTag(f.contexts[1], f.actors[1], tag.UID, false); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("blocked tag profile", e)
	}
	if _, e = f.r.UpdateTag(f.contexts[0], f.actors[0], tag.ID, locations.TagPatch{Active: boolean(false)}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.ResolveTag(context.Background(), "", tag.ID, true); !errors.Is(e, locations.ErrNotFound) {
		t.Fatal("inactive public resolution", e)
	}
	tags, e := f.r.Tags(f.contexts[0], f.actors[0])
	if e != nil || len(tags) != 1 || tags[0].LastSeenAt != nil {
		t.Fatal("invented physical scan", tags, e)
	}
	if e = f.r.DeleteTag(f.contexts[0], f.actors[0], tag.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.r.RegisterTag(f.contexts[0], f.actors[0], locations.RegisterTag{UID: tag.UID}); !errors.Is(e, locations.ErrConflict) {
		t.Fatal("reused deleted tag", e)
	}
}

func TestPGLocationsPinMutationRollbackAndRevokedProof(t *testing.T) {
	f := newLocationFixture(t, 4560)
	p, e := f.r.PutPin(f.contexts[0], f.actors[0], "", "", locations.PinRequest{LocationWKT: postString("POINT(10 20)")})
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_location_pins SET last_heartbeat_at=CURRENT_TIMESTAMP-interval '6 seconds' WHERE id=$1`, p.ID)
	query(t, f.db, `ALTER TABLE owned_location_audit ADD CONSTRAINT location_pin_failure CHECK(actor_id<>'d5100000-0000-4000-8000-000000004560'::uuid OR action<>'pin.put') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_location_audit DROP CONSTRAINT IF EXISTS location_pin_failure`)
	})
	if _, e = f.r.PutPin(f.contexts[0], f.actors[0], "", p.ID, locations.PinRequest{LocationWKT: postString("POINT(11 21)")}); e == nil {
		t.Fatal("audit failure hidden")
	}
	got, e := f.r.Pin(f.contexts[0], f.actors[0], p.ID)
	if e != nil || got.LocationWKT == nil || *got.LocationWKT != "POINT(10 20)" {
		t.Fatal("partial location mutation", got, e)
	}
	query(t, f.db, `ALTER TABLE owned_location_audit DROP CONSTRAINT location_pin_failure`)
	ctx, cancel := context.WithTimeout(f.contexts[0], 10*time.Second)
	defer cancel()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actors[0]); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.r.PutPin(ctx, f.actors[0], "", p.ID, locations.PinRequest{LocationWKT: postString("POINT(12 22)")})
		done <- e
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e = f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%owned_authority_accounts%' AND query LIKE '%FOR UPDATE%')`).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("guard did not reach lock")
		case <-ticker.C:
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, f.actors[0]); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("revoked queued location changed")
	}
	var lon float64
	if e = f.db.QueryRow(`SELECT longitude FROM owned_location_pins WHERE id=$1`, p.ID).Scan(&lon); e != nil || lon != 10 {
		t.Fatal("revoked location persisted", lon, e)
	}
}

type locationRecorder struct {
	*httptest.ResponseRecorder
	ready chan struct{}
	once  sync.Once
}

func (w *locationRecorder) Flush() { w.ResponseRecorder.Flush(); w.once.Do(func() { close(w.ready) }) }
func TestPGLocationsHTTPStreamCompletionAndRevocation(t *testing.T) {
	f := newLocationFixture(t, 4550)
	h := locations.NewVerifiedHandler(f.r, f.v)
	m := locationMeet(t, f, locations.Private)
	req := httptest.NewRequest("POST", "/api/meets/"+m.ID+"/join", nil)
	req.Header.Set("Authorization", "Bearer "+f.tokens[0])
	req.Header.Set("Accept", "text/event-stream")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	w := &locationRecorder{ResponseRecorder: httptest.NewRecorder(), ready: make(chan struct{})}
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()
	select {
	case <-w.ready:
	case <-ctx.Done():
		t.Fatal("missing stream snapshot")
	}
	if _, e := f.r.Complete(f.contexts[0], f.actors[0], m.ID); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("stream did not terminate")
	}
	if w.Code != 200 || !strings.Contains(w.Body.String(), "event: completed") || !strings.Contains(w.Body.String(), "event: snapshot") {
		t.Fatal(w.Code, w.Body.String())
	}
	if response := postHTTP(h, "GET", "/api/nfc?picc_data=opaque", "", ""); response.Code != 501 {
		t.Fatal("unconfigured NFC provider", response.Code)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE account_id=$1`, f.actors[0])
	if response := postHTTP(h, "POST", "/api/meets", `{}`, f.tokens[0]); response.Code != 401 {
		t.Fatal("revoked HTTP", response.Code)
	}
}
func TestPGLocationsRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "locations-restart.json")
	type record struct{ Actor, Meet, Pin, Tag string }
	if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var ids record
		if e = json.Unmarshal(raw, &ids); e != nil {
			t.Fatal(e)
		}
		r := locationpg.New(db)
		m, e := r.Meet(context.Background(), ids.Actor, ids.Meet, nil)
		if e != nil || m.Status != locations.Completed {
			t.Fatal("meet restart", m, e)
		}
		p, e := r.Pin(context.Background(), ids.Actor, ids.Pin)
		if e != nil || p.Status != locations.PinOffline || p.LocationWKT == nil || *p.LocationWKT != "POINT(10 20)" {
			t.Fatal("pin restart", p, e)
		}
		resolved, e := r.ResolveTag(context.Background(), "", ids.Tag, true)
		if e != nil || resolved.Account.ID != ids.Actor {
			t.Fatal("NFC restart", resolved, e)
		}
		return
	}
	f := newLocationFixture(t, 4590)
	m := locationMeet(t, f, locations.Private)
	if _, e := f.r.Complete(f.contexts[0], f.actors[0], m.ID); e != nil {
		t.Fatal(e)
	}
	p, e := f.r.PutPin(f.contexts[0], f.actors[0], "", "", locations.PinRequest{LocationWKT: postString("POINT(10 20)")})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.r.Disconnect(f.contexts[0], f.actors[0], p.ID, true); e != nil {
		t.Fatal(e)
	}
	tag, e := f.r.RegisterTag(f.contexts[0], f.actors[0], locations.RegisterTag{UID: "04AABBCC4590"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(record{f.actors[0], m.ID, p.ID, tag.ID})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
