// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/friends"
	"strings"
	"sync"
	"testing"
	"time"
)

func overviewAccept(t *testing.T, f activityFixture) {
	t.Helper()
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
}
func overviewRead(t *testing.T, f activityFixture, token, query string) []friends.Item {
	t.Helper()
	return backendDecode[[]friends.Item](t, backendCall(t, f.app, "GET", "/api/friends/overview"+query, token, "", 200))
}

func TestPGFriendsOverviewAuthoredSelectionAndWire(t *testing.T) {
	f := newActivityFixture(t)
	third := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(context.Background(), f.db, third, "synthetic_overview_"+strings.ReplaceAll(third, "-", ""), time.Now().UTC()); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), third, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	thirdToken := pgIssue(t, f.issuer, third).Token
	overviewAccept(t, f)
	if got := overviewRead(t, f, f.otherToken, ""); len(got) != 0 {
		t.Fatal(got)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"Recent friend","type":0}`, 200)
	got := overviewRead(t, f, f.otherToken, "")
	if len(got) != 1 || got[0].Account.ID != f.actor || got[0].Account.Profile == nil || got[0].Status.IsOnline || got[0].Activities == nil || len(got[0].Activities) != 0 {
		t.Fatal(got)
	}
	// An active owner makes the default list prefer active friends. The offline
	// flag is still the authored recent-status expansion, not all friendships.
	a := activityStart(t, f, "overview-public")
	got = overviewRead(t, f, f.otherToken, "")
	if len(got) != 1 || len(got[0].Activities) != 1 || got[0].Activities[0].ID != a.ID {
		t.Fatal(got)
	}
	backendCall(t, f.app, "POST", "/stargate/relationships/"+third+"/friends", f.otherToken, "", 200)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends/accept", thirdToken, "", 200)
	backendCall(t, f.app, "POST", "/api/accounts/me/statuses", thirdToken, `{"label":"Recent offline friend"}`, 200)
	if rows := overviewRead(t, f, f.otherToken, ""); len(rows) != 1 || rows[0].Account.ID != f.actor {
		t.Fatal("offline fallback displaced active friend", rows)
	}
	if rows := overviewRead(t, f, f.otherToken, "?includeOffline=true"); len(rows) != 2 {
		t.Fatal("offline expansion missing", rows)
	}
	query(t, f.db, `UPDATE owned_presence_statuses SET updated_at=clock_timestamp()-interval '25 hours' WHERE account_id=$1`, third)
	w := backendCall(t, f.app, "GET", "/passport/friends/overview?includeOffline=true&take=1", f.otherToken, "", 200)
	if w.Header().Get("X-Total") != "1" || !strings.Contains(w.Body.String(), a.ID) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Body.String(), w.Header())
	}
	if got = overviewRead(t, f, f.otherToken, "?offset=1&take=1"); len(got) != 0 {
		t.Fatal(got)
	}
	backendCall(t, f.app, "DELETE", "/api/activities?manualId=overview-public", f.token, "", 204)
	query(t, f.db, `UPDATE owned_presence_statuses SET updated_at=clock_timestamp()-interval '25 hours' WHERE account_id=$1`, f.actor)
	if got = overviewRead(t, f, f.otherToken, "?includeOffline=true"); len(got) != 0 {
		t.Fatal("old/offline account included", got)
	}
	backendCall(t, f.app, "POST", "/api/accounts/me/presence/heartbeat", f.token, `{"is_idle":true}`, 200)
	if got = overviewRead(t, f, f.otherToken, ""); len(got) != 1 || !got[0].Status.IsOnline || !got[0].Status.IsIdle {
		t.Fatal(got)
	}
}

func TestPGFriendsOverviewPrivateNestedAndCurrentAudience(t *testing.T) {
	for _, change := range []string{"none", "status_private", "invisible", "block_owner", "block_reader", "forget", "disabled", "punished", "expired_block", "source_revoke", "source_scope"} {
		t.Run(change, func(t *testing.T) {
			f := newActivityFixture(t)
			// Both original credentials precede all target state.
			overviewAccept(t, f)
			public := activityStart(t, f, "public")
			secret := activityRequest("private")
			secret.Visibility = activities.Private
			secret.Title = postString("private-title")
			secret.CatalogName = postString("private-catalog")
			secret.Meta = json.RawMessage(`{"private-meta":"secret"}`)
			secret.Tags = []activities.Tag{{Slug: "private-tag", Name: "Secret"}}
			hidden, e := f.repo.Set(f.ctx, f.actor, secret, true, "")
			if e != nil {
				t.Fatal(e)
			}
			for _, prefix := range []string{"/api", "/passport"} {
				w := backendCall(t, f.app, "GET", prefix+"/friends/overview?includeOffline=true", f.otherToken, "", 200)
				for _, value := range []string{hidden.ID, "private-title", "private-tag", "private-meta", "private-catalog"} {
					if strings.Contains(w.Body.String(), value) {
						t.Fatal("nested private data", value, w.Body.String())
					}
				}
			}
			switch change {
			case "status_private":
				backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"private-status","meta":{"secret":"private-meta"}}`, 200)
				backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", f.token, `{"online_visibility":"private","status_visibility":"private","last_seen_visibility":"private"}`, 200)
			case "invisible":
				backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"type":3,"label":"private-invisible","meta":{"secret":"private-meta"}}`, 200)
			case "block_owner":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{}`, 200)
			case "block_reader":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/block", f.otherToken, `{}`, 200)
			case "forget":
				query(t, f.db, `DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
			case "disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor)
			case "punished":
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at,expires_at)VALUES($1,'disable_account',clock_timestamp(),clock_timestamp()+interval '1 hour')`, f.actor)
			case "expired_block":
				backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, `{"expires_in":"30m"}`, 200)
				query(t, f.db, `UPDATE owned_social_relationships SET expired_at=clock_timestamp()-interval '1 second',degrade_to=NULL WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
			case "source_revoke":
				query(t, f.db, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
			case "source_scope":
				query(t, f.db, `UPDATE owned_authority_sessions SET scopes='["accounts.*"]'::jsonb,epoch=epoch+1,authorization_epoch=epoch+1 WHERE account_id=$1`, f.actor)
			}
			w := backendCall(t, f.app, "GET", "/passport/friends/overview?includeOffline=true", f.otherToken, "", 200)
			if change == "none" {
				if !strings.Contains(w.Body.String(), public.ID) {
					t.Fatal(w.Body.String())
				}
			} else if w.Body.String() != "[]\n" {
				t.Fatal(change, "ineligible account leaked through fallback", w.Body.String())
			}
		})
	}
	f := newActivityFixture(t)
	activityStart(t, f, "pending")
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
	if got := overviewRead(t, f, f.otherToken, "?includeOffline=true"); len(got) != 0 {
		t.Fatal("pending friendship exposed", got)
	}
}

func TestPGFriendsOverviewConcurrentReadsAndBounds(t *testing.T) {
	t.Run("final_proof_delay_expires_activity_before_count", func(t *testing.T) {
		f := newActivityFixture(t)
		overviewAccept(t, f)
		a := activityStart(t, f, "final-fence")
		var deadline time.Time
		if e := f.db.QueryRow(`UPDATE owned_presence_activities SET lease_expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1::uuid RETURNING lease_expires_at`, a.ID).Scan(&deadline); e != nil {
			t.Fatal(e)
		}
		verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		proof, e := verifier.Authenticate(context.Background(), f.otherToken)
		if e != nil {
			t.Fatal(e)
		}
		guard := &overviewFinalFenceGuard{TransactionGuard: authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier), deadline: deadline}
		ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 10*time.Second)
		defer cancel()
		f.db.SetMaxOpenConns(1)
		rows, total, e := activitypg.New(f.db, guard).Overview(ctx, f.other, friends.Filter{Paged: true, Take: 1})
		if e != nil || len(rows) != 0 || total != 0 || guard.checks != 3 {
			t.Fatal("expired activity influenced final page/count", rows, total, e, guard.checks)
		}
	})
	f := newActivityFixture(t)
	overviewAccept(t, f)
	activityStart(t, f, "left")
	backendCall(t, f.app, "POST", "/api/activities", f.otherToken, `{"type":"gaming","manual_id":"right"}`, 200)
	// Projection must reuse its transaction even when no second connection can
	// be borrowed. Opposite readers also exercise canonical sorted account locks.
	f.db.SetMaxOpenConns(1)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token, actor := f.token, f.actor
			if i%2 == 1 {
				token, actor = f.otherToken, f.other
			}
			v, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
			if e != nil {
				errs <- e
				return
			}
			g := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
			p, e := v.Authenticate(context.Background(), token)
			if e != nil {
				errs <- e
				return
			}
			ctx, cancel := context.WithTimeout(g.Bind(context.Background(), p), 10*time.Second)
			defer cancel()
			rows, _, e := activitypg.New(f.db, g).Overview(ctx, actor, friends.Filter{Take: 1, Paged: true})
			if e == nil && len(rows) != 1 {
				e = fmt.Errorf("missing accepted active friend: %d", len(rows))
			}
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{"?take=201", "?offset=-1", "?offset=100001", "?includeOffline=null", "?account_id=" + f.actor} {
		backendCall(t, f.app, "GET", "/api/friends/overview"+q, f.otherToken, "", 400)
	}
	// Capacity is checked before artwork or metadata hydration. All copied rows
	// retain the real canonical source admission from the original API creation.
	query(t, f.db, `INSERT INTO owned_presence_activities(id,account_id,manual_id,document,visibility,session_id,account_version,session_epoch,lease_minutes,lease_expires_at,created_at,updated_at,revision) SELECT gen_random_uuid(),a.account_id,'bound-'||g::text,a.document,a.visibility,a.session_id,a.account_version,a.session_epoch,a.lease_minutes,a.lease_expires_at,a.created_at,a.updated_at,1 FROM owned_presence_activities a CROSS JOIN generate_series(1,1000) g WHERE a.account_id=$1`, f.actor)
	backendCall(t, f.app, "GET", "/api/friends/overview?take=1", f.otherToken, "", 503)
}

// Delay only the last original-proof check in this synthetic fixture. The real
// guard still authenticates the exact original proof after elapsed database time;
// no token, clock, source session or admission is replaced to manufacture a pass.
type overviewFinalFenceGuard struct {
	*authoritypg.TransactionGuard
	deadline time.Time
	checks   int
}

func (g *overviewFinalFenceGuard) Authenticate(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) (authority.Principal, error) {
	g.checks++
	if g.checks == 3 {
		if _, e := tx.ExecContext(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, g.deadline); e != nil {
			return authority.Principal{}, e
		}
	}
	return g.TransactionGuard.Authenticate(ctx, db, tx, actor)
}

func TestPGFriendsOverviewWaitRechecksPolicyAndOriginalProof(t *testing.T) {
	for _, change := range []string{"privacy", "friendship", "reader_revocation", "owner_revocation", "reader_expiry"} {
		t.Run(change, func(t *testing.T) {
			f := newActivityFixture(t)
			var proofDeadline time.Time
			if change == "reader_expiry" {
				cfg := f.config
				cfg.AccessTTL = 5 * time.Second
				issuer, e := authority.New(authoritypg.New(f.db), cfg)
				if e != nil {
					t.Fatal(e)
				}
				f.otherToken = pgIssue(t, issuer, f.other).Token
				raw, e := base64.RawURLEncoding.DecodeString(strings.Split(f.otherToken, ".")[1])
				if e != nil {
					t.Fatal(e)
				}
				var claims struct{ Exp int64 }
				if e = json.Unmarshal(raw, &claims); e != nil {
					t.Fatal(e)
				}
				proofDeadline = time.Unix(claims.Exp, 0)
			}
			overviewAccept(t, f)
			activityStart(t, f, "wait")
			verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
			if e != nil {
				t.Fatal(e)
			}
			guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
			proof, e := verifier.Authenticate(context.Background(), f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 15*time.Second)
			defer cancel()
			lockID := f.actor
			if change == "reader_revocation" {
				lockID = f.other
			}
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", lockID)
			switch change {
			case "privacy":
				_, e = blocker.Exec(`INSERT INTO owned_presence_preferences(account_id,online_visibility,last_seen_visibility,status_visibility,updated_at)VALUES($1,'private','private','private',clock_timestamp())`, f.actor)
			case "friendship":
				_, e = blocker.Exec(`DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2`, f.actor, f.other)
			case "reader_revocation", "owner_revocation":
				_, e = blocker.Exec(`UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, lockID)
			}
			if e != nil {
				t.Fatal(e)
			}
			type result struct {
				rows []friends.Item
				e    error
			}
			done := make(chan result, 1)
			go func() {
				rows, _, e := activitypg.New(f.db, guard).Overview(ctx, f.other, friends.Filter{Take: 1000, IncludeOffline: true})
				done <- result{rows, e}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			if change == "reader_expiry" {
				organizationWaitExpired(t, ctx, f.db, proofDeadline)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if change == "reader_revocation" || change == "reader_expiry" {
				if got.e == nil {
					t.Fatal("substituted reader authority", got)
				}
			} else if got.e != nil || len(got.rows) != 0 {
				t.Fatal(change, got)
			}
		})
	}
}

func TestPGFriendsOverviewArtworkWaitExpiryAndCancellation(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			f := newArtworkFixture(t)
			at, wall := f.config.Now(), time.Now()
			f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
			var setupErr error
			f.verifier, setupErr = authority.NewVerifier(authoritypg.New(f.db), f.config)
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			f.guard = authoritypg.NewTransactionGuard(authoritypg.New(f.db), f.verifier)
			for _, id := range []string{f.actor, f.other} {
				if setupErr = authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "media.files.*"}, {Pattern: "presences.artwork.manage"}, {Pattern: "relationships.friends.manage"}}, nil); setupErr != nil {
					t.Fatal(setupErr)
				}
			}
			artworkApp(t, &f)
			asset := artworkPost(t, f.app, f.token, f.data)
			backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
			backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
			body := fmt.Sprintf(`{"type":"coding","manual_id":"wait-image","large_image":%q}`, asset.Hash)
			a := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities", f.token, body, 200))
			var deadline time.Time
			if e := f.db.QueryRow(`UPDATE owned_presence_activities SET lease_expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING lease_expires_at`, a.ID).Scan(&deadline); e != nil {
				t.Fatal(e)
			}
			proof, e := f.verifier.Authenticate(context.Background(), f.otherToken)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.guard.Bind(context.Background(), proof), 15*time.Second)
			defer cancel()
			var file string
			if e = f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1`, asset.Hash).Scan(&file); e != nil {
				t.Fatal(e)
			}
			before := commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, f.actor)
			// Reader cancellation must not cancel the independent transaction
			// supplying its lock barrier; Commit below tests barrier release.
			lockCtx, releaseLockContext := context.WithTimeout(context.Background(), 15*time.Second)
			defer releaseLockContext()
			locks := atomicAuthorityFixture{db: f.db, ctx: lockCtx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			type result struct {
				rows []friends.Item
				e    error
			}
			done := make(chan result, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				rows, _, e := activitypg.New(f.db, f.guard).Overview(ctx, f.other, friends.Filter{Take: 1000})
				done <- result{rows, e}
			}()
			t.Cleanup(func() { cancel(); _ = blocker.Rollback(); <-finished })
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if cancelRead {
				cancel()
			} else {
				organizationWaitExpired(t, ctx, f.db, deadline)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if cancelRead {
				if got.e == nil {
					t.Fatal("cancelled read succeeded")
				}
			} else if got.e != nil || len(got.rows) != 0 {
				t.Fatal("expired nested activity survived wait", got)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, f.actor); n != before {
				t.Fatal("read mutated source outbox", n, before)
			}
		})
	}
}

func TestPGFriendsOverviewSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type record struct {
		Actor, Other, Token, OtherToken string
		Authority                       restartAuthoritySnapshot
		ActivityID                      string
		Overview                        []friends.Item
		Events                          int
	}
	var saved record
	if phase == "before" {
		f := newActivityFixture(t)
		overviewAccept(t, f)
		backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"label":"Durable friend"}`, 200)
		a := backendDecode[activities.Activity](t, backendCall(t, f.app, "POST", "/api/activities", f.token, `{"type":"coding","manual_id":"restart","lease_minutes":60}`, 200))
		got := overviewRead(t, f, f.otherToken, "?includeOffline=true")
		if len(got) != 1 || len(got[0].Activities) != 1 {
			t.Fatal(got)
		}
		saved = record{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.config), ActivityID: a.ID, Overview: got, Events: commerceCount(t, f.db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, f.actor)}
		saveRestartFixture(t, "friends-overview-state.json", saved)
		return
	}
	loadRestartFixture(t, "friends-overview-state.json", &saved)
	cfg := restoreAuthoritySnapshot(t, saved.Authority)
	if cfg.SigningKey != nil {
		t.Fatal("private signing material persisted")
	}
	app := backendApp(t, db, cfg)
	got := backendDecode[[]friends.Item](t, backendCall(t, app, "GET", "/passport/friends/overview?includeOffline=true", saved.OtherToken, "", 200))
	wantJSON, _ := json.Marshal(saved.Overview)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatal("overview changed across restart", string(wantJSON), string(gotJSON))
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_activity_events WHERE account_id=$1`, saved.Actor); n != saved.Events {
		t.Fatal(n, saved.Events)
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	original, e := verifier.Authenticate(context.Background(), saved.OtherToken)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	if _, _, e = activitypg.New(db, guard).Overview(guard.Bind(context.Background(), original), saved.Actor, friends.Filter{Take: 1000}); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("proof allowed actor substitution", e)
	}
	// No target reseeding, issuer, refresh or authority rewrite in the after phase.
}
