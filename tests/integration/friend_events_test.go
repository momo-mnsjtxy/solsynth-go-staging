// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/activities"
	"strings"
	"sync"
	"testing"
	"time"
)

type friendEventFixture struct {
	activityFixture
	events    *notificationpg.Repository
	readerCtx context.Context
}

func newFriendEventFixture(t *testing.T) friendEventFixture {
	t.Helper()
	f := newActivityFixture(t)
	v, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
	p, e := v.Authenticate(context.Background(), f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	provider := friendeventpg.New(f.db, g, activitypg.New(f.db, g)).WithStatus(presencepg.NewWithAuthority(f.db, g))
	events := notificationpg.New(f.db, g, notifications.DefaultApp).WithFriendEvents(provider)
	ctx, cancel := context.WithTimeout(g.Bind(context.Background(), p), 30*time.Second)
	t.Cleanup(cancel)
	return friendEventFixture{f, events, ctx}
}
func friendProject(t *testing.T, f friendEventFixture) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		n := commerceCount(t, f.db, `SELECT count(*) FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE e.account_id=$1 AND a.processed_at IS NULL`, f.actor)
		if n == 0 {
			return
		}
		if _, e := f.events.ProjectFriends(context.Background(), 100); e != nil {
			t.Fatal(e)
		}
	}
	t.Fatal("bounded source queue did not drain")
}
func friendReplay(t *testing.T, f friendEventFixture, after int64) notifications.Page {
	t.Helper()
	p, e := f.events.Replay(f.readerCtx, f.other, notifications.DefaultApp, after, 100)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestPGFriendEventsCanonicalActivityAndStatusReplay(t *testing.T) {
	f := newFriendEventFixture(t)
	overviewAccept(t, f.activityFixture)
	a := activityStart(t, f.activityFixture, "event-public")
	friendProject(t, f)
	p := friendReplay(t, f, 0)
	if len(p.Events) != 1 || p.Events[0].Type != "account.presence.activities.updated" || !strings.Contains(string(p.Events[0].Data), a.ID) {
		t.Fatal(p)
	}
	var payload map[string]json.RawMessage
	if e := json.Unmarshal(p.Events[0].Data, &payload); e != nil {
		t.Fatal(e)
	}
	if len(payload) != 3 || payload["account_id"] == nil || payload["activities"] == nil || payload["timestamp"] == nil {
		t.Fatal(payload)
	}
	before := p.HighWater
	friendProject(t, f)
	if got := friendReplay(t, f, 0); got.HighWater != before {
		t.Fatal("duplicate local import", got)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"Available for testing","type":1}`, 200)
	friendProject(t, f)
	p = friendReplay(t, f, before)
	if len(p.Events) != 1 || p.Events[0].Type != "account.status.updated" || !strings.Contains(string(p.Events[0].Data), "Available for testing") || strings.Contains(string(p.Events[0].Data), "device_id") || strings.Contains(string(p.Events[0].Data), "session_id") {
		t.Fatal(p)
	}
	backend := backendDecode[notifications.Page](t, backendCall(t, f.app, "GET", fmt.Sprintf("/api/realtime/replay?after=%d", before), f.otherToken, "", 200))
	if len(backend.Events) != 1 || backend.Events[0].Type != "account.status.updated" {
		t.Fatal("authored event transport disconnected", backend)
	}
	// Neither local friend projection nor replay creates an external delivery.
	for _, table := range []string{"notification_v1.inbox", "notification_v1.outbox"} {
		if n := commerceCount(t, f.db, `SELECT count(*) FROM `+table+` WHERE account_id=$1`, f.other); n != 0 {
			t.Fatal(table, n)
		}
	}
	before = p.HighWater
	backendCall(t, f.app, "POST", "/api/accounts/me/presence/heartbeat", f.token, `{"is_idle":false}`, 200)
	friendProject(t, f)
	p = friendReplay(t, f, before)
	if len(p.Events) != 1 || !strings.Contains(string(p.Events[0].Data), `"is_online":true`) {
		t.Fatal(p)
	}
	before = p.HighWater
	backendCall(t, f.app, "DELETE", "/api/accounts/me/presence", f.token, "", 204)
	friendProject(t, f)
	p = friendReplay(t, f, before)
	if len(p.Events) != 1 || !strings.Contains(string(p.Events[0].Data), `"is_online":false`) {
		t.Fatal(p)
	}
}

func TestPGFriendEventsPrivacyRevisionsAndSourceAuthority(t *testing.T) {
	for _, change := range []string{"activity_private", "status_private", "invisible", "privacy_aba", "friendship_aba", "owner_revoke", "owner_scope", "owner_expiry", "punishment", "reader_revoke"} {
		t.Run(change, func(t *testing.T) {
			f := newFriendEventFixture(t)
			overviewAccept(t, f.activityFixture)
			a := activityStart(t, f.activityFixture, "visible")
			friendProject(t, f)
			page := friendReplay(t, f, 0)
			if len(page.Events) != 1 {
				t.Fatal(page)
			}
			queued := page.Events[0]
			switch change {
			case "activity_private":
				backendCall(t, f.app, "POST", "/api/activities/sessions/start", f.token, `{"manual_id":"visible","visibility":0,"title":"hidden activity","meta":{"secret":"never"},"tags":[{"slug":"hidden-tag","name":"Hidden"}]}`, 200)
			case "status_private", "privacy_aba":
				backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", f.token, `{"online_visibility":"private","status_visibility":"private","last_seen_visibility":"private"}`, 200)
				if change == "privacy_aba" {
					backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", f.token, `{"online_visibility":"public","status_visibility":"public","last_seen_visibility":"friends"}`, 200)
				}
			case "invisible":
				backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"type":3,"label":"hidden invisible"}`, 200)
			case "friendship_aba":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{"expires_in":"30m"}`, 200)
				query(t, f.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second' WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
				overviewAccept(t, f.activityFixture)
			case "owner_revoke":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
			case "owner_scope":
				query(t, f.db, `UPDATE owned_authority_sessions SET epoch=epoch+1,authorization_epoch=epoch+1,scopes='["accounts.*"]'::jsonb WHERE account_id=$1`, f.actor)
			case "owner_expiry":
				query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.actor)
			case "punishment":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at,expires_at)VALUES($1,'disable_account',clock_timestamp(),clock_timestamp()+interval '1 hour')`, f.actor)
			case "reader_revoke":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.other)
			}
			got, allowed, e := f.events.RevalidateEvent(f.readerCtx, f.other, notifications.DefaultApp, queued)
			if change == "reader_revoke" {
				if e == nil {
					t.Fatal("revoked reader used queued packet", got)
				}
			} else if e != nil || allowed {
				t.Fatal("stale audience/source survived", change, a.ID, allowed, e, string(got.Data))
			}
			if change != "reader_revoke" {
				p := friendReplay(t, f, 0)
				if len(p.Events) != 0 {
					t.Fatal("old event replayed", p)
				}
			}
		})
	}
	f := newFriendEventFixture(t)
	overviewAccept(t, f.activityFixture)
	q := activityRequest("private-only")
	q.Visibility = activities.Private
	if _, e := f.repo.Set(f.ctx, f.actor, q, true, ""); e != nil {
		t.Fatal(e)
	}
	friendProject(t, f)
	if p := friendReplay(t, f, 0); p.HighWater != 0 || len(p.Events) != 0 {
		t.Fatal("private activity produced invalidation or cursor", p)
	}
}

func TestPGFriendEventsConcurrentImportRollbackAndBounds(t *testing.T) {
	f := newFriendEventFixture(t)
	overviewAccept(t, f.activityFixture)
	activityStart(t, f.activityFixture, "concurrent")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.events.ProjectFriends(context.Background(), 100); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	friendProject(t, f)
	p := friendReplay(t, f, 0)
	if len(p.Events) != 1 || p.HighWater != 1 {
		t.Fatal("duplicate stream event", p)
	}
	for _, take := range []int{0, 101} {
		if _, e := f.events.ProjectFriends(context.Background(), take); !errors.Is(e, notifications.ErrInvalid) {
			t.Fatal(take, e)
		}
	}
	// A failed source admission rolls back activity and canonical activity-event
	// writes as well as its friend outbox. Constraint is scoped to this owner.
	constraint := "synthetic_friend_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_friend_events ADD CONSTRAINT `+constraint+` CHECK(account_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE owned_friend_events DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.Set(f.ctx, f.actor, activityRequest("rollback"), true, ""); e == nil {
		t.Fatal("source outbox failure did not abort")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_presence_activities WHERE account_id=$1 AND manual_id='rollback'`, f.actor); n != 0 {
		t.Fatal(n)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"must roll back"}`, 503)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_presence_statuses WHERE account_id=$1`, f.actor); n != 0 {
		t.Fatal("status escaped source outbox rollback", n)
	}
	query(t, f.db, `ALTER TABLE owned_friend_events DROP CONSTRAINT `+constraint)
	activityStart(t, f.activityFixture, "import-rollback")
	constraint = "synthetic_stream_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE notification_v1.events ADD CONSTRAINT `+constraint+` CHECK(account_id<>'`+f.other+`'::uuid) NOT VALID`)
	t.Cleanup(func() { f.db.Exec(`ALTER TABLE notification_v1.events DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.events.ProjectFriends(context.Background(), 100); e == nil {
		t.Fatal("stream failure acknowledged outbox")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE e.account_id=$1 AND a.processed_at IS NULL`, f.actor); n != 1 {
		t.Fatal(n)
	}
	query(t, f.db, `ALTER TABLE notification_v1.events DROP CONSTRAINT `+constraint)
	friendProject(t, f)
	if p = friendReplay(t, f, 1); len(p.Events) != 1 || p.HighWater != 2 {
		t.Fatal(p)
	}
}

func TestPGFriendEventsOversizedSnapshotsDoNotPoisonQueue(t *testing.T) {
	t.Run("encoded_status", func(t *testing.T) {
		f := newFriendEventFixture(t)
		overviewAccept(t, f.activityFixture)
		// Valid input metadata is below 16 KiB, but '<' expands to six bytes
		// during JSON encoding. Bound the actual event dictionary, not its input.
		backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"oversized encoded status","meta":{"padding":"`+strings.Repeat("<", 15000)+`"}}`, 200)
		friendProject(t, f)
		if p := friendReplay(t, f, 0); p.HighWater != 0 || len(p.Events) != 0 {
			t.Fatal("oversized status packet or cursor emitted", p)
		}
		backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"bounded recovery","meta":{"visible":true}}`, 200)
		friendProject(t, f)
		p := friendReplay(t, f, 0)
		if p.HighWater != 1 || len(p.Events) != 1 || len(p.Events[0].Data) > notifications.MaxPayload || !strings.Contains(string(p.Events[0].Data), "bounded recovery") {
			t.Fatal("oversized status poisoned later bounded update", p)
		}
	})
	f := newFriendEventFixture(t)
	overviewAccept(t, f.activityFixture)
	var survivor activities.Activity
	for i := 0; i < 5; i++ {
		q := activityRequest(fmt.Sprintf("large-%d", i))
		q.Meta = json.RawMessage(`{"padding":"` + strings.Repeat("x", 15000) + `"}`)
		v, e := f.repo.Set(f.ctx, f.actor, q, true, "")
		if e != nil {
			t.Fatal(e)
		}
		if i == 4 {
			survivor = v
		}
	}
	friendProject(t, f)
	if p := friendReplay(t, f, 0); p.HighWater != 0 || len(p.Events) != 0 {
		t.Fatal("oversized partial packet emitted", p)
	}
	for i := 0; i < 4; i++ {
		backendCall(t, f.app, "DELETE", fmt.Sprintf("/api/activities?manualId=large-%d", i), f.token, "", 204)
	}
	friendProject(t, f)
	p := friendReplay(t, f, 0)
	if len(p.Events) == 0 || !strings.Contains(string(p.Events[0].Data), survivor.ID) {
		t.Fatal("oversized source poisoned later bounded updates", p)
	}
}

func TestPGFriendEventsAccountWaitRechecksBothAudienceAndProof(t *testing.T) {
	for _, change := range []string{"reader", "source", "friendship", "private"} {
		t.Run(change, func(t *testing.T) {
			f := newFriendEventFixture(t)
			overviewAccept(t, f.activityFixture)
			activityStart(t, f.activityFixture, "wait")
			friendProject(t, f)
			locks := atomicAuthorityFixture{db: f.db, ctx: f.readerCtx}
			owner := f.actor
			if change == "reader" {
				owner = f.other
			}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", owner)
			var e error
			switch change {
			case "reader", "source":
				_, e = blocker.Exec(`UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, owner)
			case "friendship":
				_, e = blocker.Exec(`DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
			case "private":
				_, e = blocker.Exec(`UPDATE owned_presence_activities SET visibility=0,revision=revision+1 WHERE account_id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			type result struct {
				p notifications.Page
				e error
			}
			done := make(chan result, 1)
			go func() {
				p, e := f.events.Replay(f.readerCtx, f.other, notifications.DefaultApp, 0, 100)
				done <- result{p, e}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if change == "reader" {
				if got.e == nil {
					t.Fatal("reader proof replaced", got)
				}
			} else if got.e != nil || len(got.p.Events) != 0 {
				t.Fatal(change, got)
			}
		})
	}
}

func TestPGFriendEventsSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Other, Token, OtherToken string
		Authority                       restartAuthoritySnapshot
		Before                          notifications.Page
		Pending                         int
	}
	var saved record
	if phase == "before" {
		f := newFriendEventFixture(t)
		overviewAccept(t, f.activityFixture)
		backendCall(t, f.app, "POST", "/api/activities", f.token, `{"type":"coding","manual_id":"restart-public","lease_minutes":60}`, 200)
		friendProject(t, f)
		page := friendReplay(t, f, 0)
		if len(page.Events) != 1 {
			t.Fatal(page)
		}
		saved = record{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.config), Before: page, Pending: commerceCount(t, f.db, `SELECT count(*) FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE e.account_id=$1 AND a.processed_at IS NULL`, f.actor)}
		saveRestartFixture(t, "friend-events-state.json", saved)
		return
	}
	loadRestartFixture(t, "friend-events-state.json", &saved)
	cfg := restoreAuthoritySnapshot(t, saved.Authority)
	if cfg.SigningKey != nil {
		t.Fatal("private key in replay marker")
	}
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	g := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	proof, e := v.Authenticate(context.Background(), saved.OtherToken)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(g.Bind(context.Background(), proof), 15*time.Second)
	defer cancel()
	provider := friendeventpg.New(db, g, activitypg.New(db, g)).WithStatus(presencepg.NewWithAuthority(db, g))
	events := notificationpg.New(db, g, notifications.DefaultApp).WithFriendEvents(provider)
	page, e := events.Replay(ctx, saved.Other, notifications.DefaultApp, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	want, _ := json.Marshal(saved.Before)
	got, _ := json.Marshal(page)
	if string(want) != string(got) {
		t.Fatal("durable replay changed", string(want), string(got))
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_friend_audiences a JOIN owned_friend_events e ON e.id=a.event_id WHERE e.account_id=$1 AND a.processed_at IS NULL`, saved.Actor); n != saved.Pending {
		t.Fatal(n, saved.Pending)
	}
	app := backendApp(t, db, cfg)
	httpPage := backendDecode[notifications.Page](t, backendCall(t, app, "GET", "/api/realtime/replay?after=0", saved.OtherToken, "", 200))
	if len(httpPage.Events) != 1 || httpPage.Events[0].Type != "account.presence.activities.updated" {
		t.Fatal(httpPage)
	}
	// Existing rows and original proof only after restart; no issuance or rewrite.
}
