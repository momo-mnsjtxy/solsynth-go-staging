// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func pinPost(t *testing.T, f postMediaFixture) posts.Post {
	t.Helper()
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Synthetic pin body")})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func pinSnapshot(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	e := db.QueryRow(`SELECT jsonb_build_object('post',(SELECT to_jsonb(p) FROM owned_text_posts p WHERE id=$1),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM owned_text_post_audit a WHERE post_id=$1))::text`, id).Scan(&s)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPGPostPinHTTPActiveModesAndHydration(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	file := privatePostFile(t, f, "pin", "Synthetic pin bytes")
	p := attachPost(t, f, posts.Public, file.ID)
	for _, prefix := range []string{"/api", "/sphere"} {
		pinned := backendDecode[posts.Post](t, backendCall(t, h, "POST", prefix+"/posts/"+p.ID+"/pin", f.token, `{"mode":0}`, 200))
		if pinned.PinMode == nil || *pinned.PinMode != 0 || len(pinned.Attachments) != 1 {
			t.Fatal(pinned)
		}
		page := backendDecode[[]posts.Post](t, backendCall(t, h, "GET", prefix+"/posts?pub="+f.pub.ID+"&pinned=true", "", "", 200))
		if len(page) != 1 || page[0].ID != p.ID || len(page[0].Attachments) != 1 {
			t.Fatal(page)
		}
		unpinned := backendDecode[posts.Post](t, backendCall(t, h, "DELETE", prefix+"/posts/"+p.ID+"/pin", f.token, "", 200))
		if unpinned.PinMode != nil {
			t.Fatal(unpinned)
		}
	}
	for _, body := range []string{`{}`, `{"mode":null}`, `{"mode":0,"mode":1}`} {
		backendCall(t, h, "POST", "/api/posts/"+p.ID+"/pin", f.token, body, 400)
	}
	backendCall(t, h, "POST", "/api/posts/"+p.ID+"/pin", f.token, `{"mode":2}`, 400)
	backendCall(t, h, "POST", "/api/posts/"+p.ID+"/pin", f.token, `{"mode":1}`, 400)
	backendCall(t, h, "POST", "/api/posts/"+p.ID+"/pin", f.otherToken, `{"mode":0}`, 403)
	backendCall(t, h, "POST", "/api/posts/"+p.ID+"/pin", "", `{"mode":0}`, 401)
	backendCall(t, h, "GET", "/api/posts?pinned=true", "", "", 400)
	// Ordinary post writers cannot smuggle a pin transition.
	backendCall(t, h, "PATCH", "/api/posts/"+p.ID, f.token, `{"pin_mode":0}`, 501)
}

func TestPGPostReplyPinParentAuthorityPrivacyAndPersistence(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	parent := pinPost(t, f)
	reply, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Pinned reply"), RepliedPostID: &parent.ID})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, h, "POST", "/api/posts/"+reply.ID+"/pin", f.otherToken, `{"mode":2}`, 403)
	pinned := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts/"+reply.ID+"/pin", f.token, `{"mode":2}`, 200))
	if pinned.PinMode == nil || *pinned.PinMode != posts.ReplyPin {
		t.Fatal(pinned)
	}
	listed := backendDecode[[]posts.Post](t, backendCall(t, h, "GET", "/api/posts/"+parent.ID+"/replies/pinned", "", "", 200))
	if len(listed) != 1 || listed[0].ID != reply.ID {
		t.Fatal(listed)
	}
	reopened := postpg.NewWithAuthority(f.db, f.guard).WithVisibility(socialpg.New(f.db)).WithAssociations(f.links)
	listed, _, e = reopened.Replies(context.Background(), "", parent.ID, "pinned", 0, 100, false)
	if e != nil || len(listed) != 1 || listed[0].ID != reply.ID {
		t.Fatal(listed, e)
	}
	private := posts.Private
	if _, e = f.posts.Update(f.ctx, f.actor, reply.ID, posts.Patch{Visibility: &private}); e != nil {
		t.Fatal(e)
	}
	listed, _, e = reopened.Replies(context.Background(), "", parent.ID, "pinned", 0, 100, false)
	if e != nil || len(listed) != 0 {
		t.Fatal("private reply leaked", listed, e)
	}
}

func TestPGPostBoostConcurrentUniqueAndVisibility(t *testing.T) {
	f := newPostSchedulingFixture(t)
	public := pinPost(t, f)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.posts.BoostPost(f.ctx, f.actor, public.ID, nil); errs <- e }()
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, posts.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 7 {
		t.Fatal(success, conflict)
	}
	boosts, total, e := f.posts.PostBoosts(context.Background(), "", public.ID, 0, 20)
	if e != nil || total != 1 || len(boosts) != 1 || boosts[0].AccountID != f.actor {
		t.Fatal(boosts, total, e)
	}
	visibility := posts.Private
	private, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Private boost target"), Visibility: &visibility})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.BoostPost(f.ctx, f.actor, private.ID, nil); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.posts.PostBoosts(context.Background(), "", private.ID, 0, 20); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private boost list leaked", e)
	}
	if e = f.posts.UnboostPost(f.ctx, f.actor, public.ID); e != nil {
		t.Fatal(e)
	}
	if _, total, e = f.posts.PostBoosts(context.Background(), "", public.ID, 0, 20); e != nil || total != 0 {
		t.Fatal(total, e)
	}
}
func TestPGPostPinRealmRolesAndVisibleLists(t *testing.T) {
	f := postRealmFixture(t)
	postRealmEditor(t, f, f.other, f.otherCtx)
	p := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "Pinned private realm")
	if _, e := f.posts.Pin(f.otherCtx, f.other, p.ID, posts.RealmPin); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("normal member pinned realm", e)
	}
	if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
		t.Fatal(e)
	}
	if _, e := f.posts.Unpin(f.otherCtx, f.other, p.ID); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("normal member unpinned realm", e)
	}
	rows, n, e := f.posts.List(f.otherCtx, f.other, posts.ListOptions{Take: 20, Realm: f.private.ID, Pinned: boolean(true)})
	if e != nil || n != 1 || len(rows) != 1 || rows[0].PinMode == nil || *rows[0].PinMode != 1 {
		t.Fatal(rows, n, e)
	}
	if _, _, e = f.posts.List(context.Background(), "", posts.ListOptions{Take: 20, Realm: f.private.ID, Pinned: boolean(true)}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private realm listed", e)
	}
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.posts.List(f.otherCtx, f.other, posts.ListOptions{Take: 20, Realm: f.private.ID, Pinned: boolean(true)}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("withdrawn realm membership listed pins", e)
	}
	// A pin grants no audience: both private visibility and soft deletion suppress it.
	public := f.post(t, f.individual, "Publisher pin", posts.Public)
	if _, e = f.posts.Pin(f.ctx, f.actor, public.ID, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, public.ID, posts.Patch{Visibility: postInt(posts.Private)}); e != nil {
		t.Fatal(e)
	}
	rows, n, e = f.posts.List(context.Background(), "", posts.ListOptions{Take: 20, Publisher: f.individual.ID, Pinned: boolean(true)})
	if e != nil || n != 0 || len(rows) != 0 {
		t.Fatal(rows, n, e)
	}
	if e = f.posts.Delete(f.ctx, f.actor, public.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Pin(f.ctx, f.actor, public.ID, 0); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestPGPostPinRelationRejectionRollbackAndConcurrency(t *testing.T) {
	f := newPostSchedulingFixture(t)
	p := pinPost(t, f)
	for _, patch := range []posts.Patch{{Content: postString("Reply"), RepliedPostID: &p.ID}, {Content: postString("Chain"), ChainedPostID: &p.ID}} {
		q, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, patch)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.posts.Pin(f.ctx, f.actor, q.ID, 0); !errors.Is(e, posts.ErrPinFailed) {
			t.Fatal(e)
		}
	}
	before := pinSnapshot(t, f.db, p.ID)
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT synthetic_pin_audit_failure CHECK(post_id<>'`+p.ID+`'::uuid OR action NOT IN('pin','unpin')) NOT VALID`)
	if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, 0); e == nil {
		t.Fatal("audit failure ignored")
	}
	if pinSnapshot(t, f.db, p.ID) != before {
		t.Fatal("pin partially committed")
	}
	query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT synthetic_pin_audit_failure`)
	if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, 0); e != nil {
		t.Fatal(e)
	}
	before = pinSnapshot(t, f.db, p.ID)
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT synthetic_pin_audit_failure CHECK(post_id<>'`+p.ID+`'::uuid OR action<>'unpin') NOT VALID`)
	if _, e := f.posts.Unpin(f.ctx, f.actor, p.ID); e == nil {
		t.Fatal("unpin audit failure ignored")
	}
	if pinSnapshot(t, f.db, p.ID) != before {
		t.Fatal("unpin partially committed")
	}
	query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT synthetic_pin_audit_failure`)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var e error
			if i%2 == 0 {
				_, e = f.posts.Pin(f.ctx, f.actor, p.ID, 0)
			} else {
				_, e = f.posts.Unpin(f.ctx, f.actor, p.ID)
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
	var mode sql.NullInt64
	var action string
	if e := f.db.QueryRow(`SELECT pin_mode,(SELECT action FROM owned_text_post_audit WHERE post_id=$1 AND action IN('pin','unpin') ORDER BY id DESC LIMIT 1) FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&mode, &action); e != nil || (action == "pin") != mode.Valid {
		t.Fatal(mode, action, e)
	}
}
func TestPGPostPinWaitRechecksScopeAndOriginalProof(t *testing.T) {
	for _, change := range []string{"editor", "proof", "cancel", "pin-state"} {
		t.Run(change, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			p := pinPost(t, f)
			before := pinSnapshot(t, f.db, p.ID)
			requestCtx := f.ctx
			var expires time.Time
			if change == "proof" {
				requestCtx, expires = publishingExpiringProof(t, f)
			}
			ctx, cancel := context.WithTimeout(requestCtx, 10*time.Second)
			defer cancel()
			locks := pinWaitLocks(t, f.db)
			tx, pid := atomicBlocker(t, locks, "owned_publishers", f.pub.ID)
			defer tx.Rollback()
			done := make(chan error, 1)
			go func() { _, e := f.posts.Pin(ctx, f.actor, p.ID, 0); done <- e }()
			waitAtomicBlocked(t, locks, pid, "owned_publishers")
			switch change {
			case "editor":
				if _, e := tx.Exec(`UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1 AND account_id=$2`, f.pub.ID, f.actor); e != nil {
					t.Fatal(e)
				}
			case "proof":
				organizationWaitExpired(t, ctx, f.db, expires)
			case "cancel":
				cancel()
			case "pin-state":
				if _, e := tx.Exec(`UPDATE owned_text_posts SET deleted_at=clock_timestamp() WHERE id=$1`, p.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e := tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; e == nil {
				t.Fatal("stale write accepted")
			}
			if change != "pin-state" && pinSnapshot(t, f.db, p.ID) != before {
				t.Fatal("failed wait changed original state")
			}
		})
	}
}
func TestPGPostPinCurrentAccountSessionAndGrant(t *testing.T) {
	for _, change := range []string{"disabled", "session", "grant"} {
		t.Run(change, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			p := pinPost(t, f)
			before := pinSnapshot(t, f.db, p.ID)
			locks := pinWaitLocks(t, f.db)
			tx, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
			defer tx.Rollback()
			done := make(chan error, 1)
			go func() { _, e := f.posts.Pin(f.ctx, f.actor, p.ID, 0); done <- e }()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			var e error
			switch change {
			case "disabled":
				_, e = tx.Exec(`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "session":
				_, e = tx.Exec(`UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor)
			case "grant":
				_, e = tx.Exec(`DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale authority accepted")
			}
			if pinSnapshot(t, f.db, p.ID) != before {
				t.Fatal("authority failure changed post")
			}
		})
	}
}
func TestPGPostPinListWaitRechecksPrivacyAndPinState(t *testing.T) {
	for _, change := range []string{"private", "delete", "unpin"} {
		t.Run(change, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			p := pinPost(t, f)
			if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, 0); e != nil {
				t.Fatal(e)
			}
			locks := pinWaitLocks(t, f.db)
			tx, pid := atomicBlocker(t, locks, "owned_publishers", f.pub.ID)
			defer tx.Rollback()
			type result struct {
				rows []posts.Post
				n    int64
				e    error
			}
			done := make(chan result, 1)
			go func() {
				rows, n, e := f.posts.List(context.Background(), "", posts.ListOptions{Publisher: f.pub.ID, Take: 20, Pinned: boolean(true)})
				done <- result{rows, n, e}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_publishers")
			q := `UPDATE owned_text_posts SET pin_mode=NULL WHERE id=$1`
			if change == "private" {
				q = `UPDATE owned_text_posts SET visibility=3 WHERE id=$1`
			} else if change == "delete" {
				q = `UPDATE owned_text_posts SET deleted_at=clock_timestamp() WHERE id=$1`
			}
			if _, e := tx.Exec(q, p.ID); e != nil {
				t.Fatal(e)
			}
			if e := tx.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if got.e != nil || got.n != 0 || len(got.rows) != 0 {
				t.Fatal("stale pinned projection", got)
			}
		})
	}
}
func TestPGPostPinSavedRestartPersistence(t *testing.T) {
	db := database(t)
	phase := fixtureRestartPhase(t)
	type saved struct {
		Actor, Publisher, Post, Unpinned, Token, Snapshot, UnpinnedSnapshot string
		Authority                                                           restartAuthoritySnapshot
	}
	const marker = "post-pins.json"
	if phase == "before" {
		f := newPostSchedulingFixture(t)
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f = postSchedulingAuthority(t, f)
		p := pinPost(t, f)
		q := pinPost(t, f)
		if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, 0); e != nil {
			t.Fatal(e)
		}
		if _, e := f.posts.Pin(f.ctx, f.actor, q.ID, 0); e != nil {
			t.Fatal(e)
		}
		if _, e := f.posts.Unpin(f.ctx, f.actor, q.ID); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, saved{f.actor, f.pub.ID, p.ID, q.ID, f.token, pinSnapshot(t, db, p.ID), pinSnapshot(t, db, q.ID), saveAuthoritySnapshot(t, f.config)})
		return
	}
	var s saved
	loadRestartFixture(t, marker, &s)
	if pinSnapshot(t, db, s.Post) != s.Snapshot || pinSnapshot(t, db, s.Unpinned) != s.UnpinnedSnapshot {
		t.Fatal("original pin/unpin/audit state changed across restart")
	}
	cfg := restoreAuthoritySnapshot(t, s.Authority)
	verifier, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), s.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	ctx := guard.Bind(context.Background(), proof)
	repo := postpg.NewWithAuthority(db, guard).WithVisibility(socialpg.New(db))
	rows, n, e := repo.List(ctx, s.Actor, posts.ListOptions{Take: 20, Publisher: s.Publisher, Pinned: boolean(true)})
	if e != nil || n != 1 || len(rows) != 1 || rows[0].ID != s.Post || rows[0].PinMode == nil || *rows[0].PinMode != 0 {
		t.Fatal(rows, n, e)
	}
	rows, n, e = repo.List(ctx, s.Actor, posts.ListOptions{Take: 20, Publisher: s.Publisher, Pinned: boolean(false)})
	if e != nil || n != 1 || len(rows) != 1 || rows[0].ID != s.Unpinned || rows[0].PinMode != nil {
		t.Fatal(rows, n, e)
	}
	if pinSnapshot(t, db, s.Post) != s.Snapshot || pinSnapshot(t, db, s.Unpinned) != s.UnpinnedSnapshot {
		t.Fatal("read-only restart verification changed saved state")
	}
	// Captured original proof only: no seed, issuance, repair, policy or target write.
}

func pinWaitLocks(t *testing.T, db *sql.DB) atomicAuthorityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	return atomicAuthorityFixture{db: db, ctx: ctx}
}

func TestPGPostPinRealmWaitDemotionAndWithdrawal(t *testing.T) {
	for _, change := range []string{"demote", "leave", "read-leave", "current-mode"} {
		t.Run(change, func(t *testing.T) {
			f := postRealmFixture(t)
			postRealmEditor(t, f, f.other, f.otherCtx)
			p := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "Realm wait pin")
			if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
				t.Fatal(e)
			}
			query(t, f.db, `UPDATE owned_realm_members SET role=50 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other)
			if change == "current-mode" {
				query(t, f.db, `UPDATE owned_text_posts SET pin_mode=0 WHERE id=$1`, p.ID)
				query(t, f.db, `UPDATE owned_realm_members SET role=0 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other)
			}
			locks := pinWaitLocks(t, f.db)
			tx, pid := atomicBlocker(t, locks, "owned_realms", f.private.ID)
			defer tx.Rollback()
			type result struct {
				rows []posts.Post
				e    error
			}
			done := make(chan result, 1)
			go func() {
				if change == "read-leave" {
					rows, _, e := f.posts.List(f.otherCtx, f.other, posts.ListOptions{Realm: f.private.ID, Take: 20, Pinned: boolean(true)})
					done <- result{rows, e}
				} else {
					_, e := f.posts.Unpin(f.otherCtx, f.other, p.ID)
					done <- result{e: e}
				}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_realms")
			var e error
			switch change {
			case "demote":
				_, e = tx.Exec(`UPDATE owned_realm_members SET role=0 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other)
			case "leave", "read-leave":
				_, e = tx.Exec(`UPDATE owned_realm_members SET leave_at=clock_timestamp() WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other)
			case "current-mode":
				_, e = tx.Exec(`UPDATE owned_text_posts SET pin_mode=1 WHERE id=$1`, p.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if change == "read-leave" {
				if len(got.rows) != 0 {
					t.Fatal("withdrawn member received pinned body", got)
				}
			} else if got.e == nil {
				t.Fatal("unpin used stale membership or mode")
			}
			var mode int
			if e = f.db.QueryRow(`SELECT pin_mode FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&mode); e != nil || mode != 1 {
				t.Fatal("denied unpin changed state", mode, e)
			}
		})
	}
}
func TestPGPostPinRealmMoveInvalidatesScope(t *testing.T) {
	f := postRealmFixture(t)
	p := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "Scoped pin")
	if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
		t.Fatal(e)
	}
	for _, realm := range []string{f.private.ID, f.public.ID} {
		rows, n, e := f.posts.List(f.ctx, f.actor, posts.ListOptions{Realm: realm, Take: 20, Pinned: boolean(true)})
		want := int64(0)
		if realm == f.private.ID {
			want = 1
		}
		if e != nil || n != want || int64(len(rows)) != want {
			t.Fatal("before move pinned scope", rows, n, e)
		}
	}
	p, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.public.ID})
	if e != nil || p.PinMode != nil {
		t.Fatal("realm move carried moderator grant", p, e)
	}
	for _, realm := range []string{f.private.ID, f.public.ID} {
		rows, n, e := f.posts.List(f.ctx, f.actor, posts.ListOptions{Realm: realm, Take: 20, Pinned: boolean(true)})
		if e != nil || n != 0 || len(rows) != 0 {
			t.Fatal("realm move retained scoped pin", rows, n, e)
		}
	}
	if _, e = f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
		t.Fatal(e)
	}
	p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{ClearRealm: true})
	if e != nil || p.PinMode != nil {
		t.Fatal("realm detach retained stale pin", p, e)
	}
	if _, e = f.posts.Pin(f.ctx, f.actor, p.ID, posts.PublisherPin); e != nil {
		t.Fatal(e)
	}
	p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.public.ID})
	if e != nil || p.PinMode == nil || *p.PinMode != 0 {
		t.Fatal("realm change erased independent publisher pin", p, e)
	}
	if _, e = f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
		t.Fatal(e)
	}
	mod, e := moderationpg.New(f.db, f.guard, moderationpg.Config{})
	if e != nil {
		t.Fatal(e)
	}
	removed, e := mod.ModerateRealmPost(f.ctx, f.actor, p.ID, postString("Synthetic pin removal"))
	if e != nil || removed.PinMode != nil {
		t.Fatal("moderation retained pin", removed, e)
	}
	var mode sql.NullInt64
	if e = f.db.QueryRow(`SELECT pin_mode FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&mode); e != nil || mode.Valid {
		t.Fatal("moderation did not atomically invalidate pin", mode, e)
	}
}

func TestPGPostPinPublicRealmWithdrawalDuringRead(t *testing.T) {
	f := postRealmFixture(t)
	p := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.public.ID, "Public realm pin")
	if _, e := f.posts.Pin(f.ctx, f.actor, p.ID, posts.RealmPin); e != nil {
		t.Fatal(e)
	}
	options := posts.ListOptions{Realm: f.public.ID, Take: 20, Pinned: boolean(true)}
	rows, n, e := f.posts.List(context.Background(), "", options)
	if e != nil || n != 1 || len(rows) != 1 {
		t.Fatal(rows, n, e)
	}
	locks := pinWaitLocks(t, f.db)
	tx, pid := atomicBlocker(t, locks, "owned_realms", f.public.ID)
	defer tx.Rollback()
	type result struct {
		rows []posts.Post
		n    int64
		e    error
	}
	done := make(chan result, 1)
	go func() { rows, n, e := f.posts.List(context.Background(), "", options); done <- result{rows, n, e} }()
	waitAtomicBlocked(t, locks, pid, "owned_realms")
	if _, e = tx.Exec(`UPDATE owned_realms SET is_public=false WHERE id=$1`, f.public.ID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.e != nil || got.n != 0 || len(got.rows) != 0 {
		t.Fatal("realm withdrawal leaked previously public pin", got)
	}
	if _, _, e = f.posts.List(context.Background(), "", options); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private realm still discoverable", e)
	}
}

// Pause only after the real canonical hydration has completed. The owner change
// can therefore commit after the first visibility read and before the final one.
type pinHydrationWait struct {
	postpg.AssociationStore
	entered chan struct{}
	resume  <-chan struct{}
}

func (p pinHydrationWait) Load(ctx context.Context, tx *sql.Tx, actor string, out *posts.Post) error {
	if e := p.AssociationStore.Load(ctx, tx, actor, out); e != nil {
		return e
	}
	close(p.entered)
	select {
	case <-p.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p pinHydrationWait) RecheckProjection(ctx context.Context, tx *sql.Tx, actor string, out *posts.Post) error {
	return postpg.RecheckAssociations(ctx, tx, actor, out, p.AssociationStore)
}

func TestPGPostPinHydrationRechecksRealmOwner(t *testing.T) {
	for _, action := range []string{"pin", "unpin"} {
		t.Run(action, func(t *testing.T) {
			f := postRealmFixture(t)
			name := "synthetic-pin-editor-" + f.other
			publisher, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
			if e != nil {
				t.Fatal(e)
			}
			query(t, f.db, `UPDATE owned_realm_members SET role=50 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other)
			p := postRealmCreate(t, f, f.otherCtx, f.other, publisher.ID, f.private.ID, "Distinct realm owner")
			if action == "unpin" {
				if _, e = f.posts.Pin(f.otherCtx, f.other, p.ID, posts.RealmPin); e != nil {
					t.Fatal(e)
				}
			}
			before := pinSnapshot(t, f.db, p.ID)
			ctx, cancel := context.WithTimeout(f.otherCtx, 10*time.Second)
			defer cancel()
			entered, resume := make(chan struct{}), make(chan struct{}, 1)
			defer close(resume)
			f.posts.WithAssociations(pinHydrationWait{f.timeline, entered, resume})
			done := make(chan error, 1)
			go func() {
				var e error
				if action == "pin" {
					_, e = f.posts.Pin(ctx, f.other, p.ID, posts.RealmPin)
				} else {
					_, e = f.posts.Unpin(ctx, f.other, p.ID)
				}
				done <- e
			}()
			select {
			case <-entered:
			case e := <-done:
				t.Fatal("operation ended before hydration barrier", e)
			case <-ctx.Done():
				t.Fatal("hydration barrier not reached", ctx.Err())
			}
			query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			resume <- struct{}{}
			if e = <-done; !errors.Is(e, posts.ErrNotFound) {
				t.Fatal("inactive realm owner accepted after hydration", e)
			}
			if pinSnapshot(t, f.db, p.ID) != before {
				t.Fatal("late scope denial partially committed pin or audit")
			}
		})
	}
}

func pinScheduleSnapshot(t *testing.T, db *sql.DB, id int64, full bool) string {
	t.Helper()
	q := `SELECT (to_jsonb(s)-'revision'-'post_audit_id'-'updated_at')::text FROM owned_post_schedules s WHERE id=$1`
	if full {
		q = `SELECT jsonb_build_object('schedule',to_jsonb(s),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.id) FROM owned_post_schedule_events e WHERE e.schedule_id=s.id))::text FROM owned_post_schedules s WHERE id=$1`
	}
	var out string
	if e := db.QueryRow(q, id).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPGPostPinPreservesOriginalScheduledIntent(t *testing.T) {
	f := newPostSchedulingFixture(t)
	p := scheduledPost(t, f, posts.Patch{})
	original := pinScheduleSnapshot(t, f.db, p.Schedule.ID, false)
	for _, action := range []string{"pin", "pin", "unpin", "unpin"} {
		var e error
		if action == "pin" {
			_, e = f.posts.Pin(f.ctx, f.actor, p.ID, posts.PublisherPin)
		} else {
			_, e = f.posts.Unpin(f.ctx, f.actor, p.ID)
		}
		if e != nil {
			t.Fatal(action, e)
		}
		if pinScheduleSnapshot(t, f.db, p.Schedule.ID, false) != original {
			t.Fatal("pin changed original publication authority, state, policy or deadline")
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_schedule_events WHERE schedule_id=$1 AND kind='edited'`, p.Schedule.ID); n != 4 {
		t.Fatal("pin replay did not preserve atomic audit correspondence", n)
	}
	scheduleDue(t, f.db, p)
	processSchedule(t, f)
	scheduleState(t, f.db, p.Schedule.ID, "published")
	got, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil || got.PinMode != nil || got.DraftedAt != nil {
		t.Fatal("pin invalidated otherwise unchanged publication", got, e)
	}
}

func TestPGPostPinScheduleRollbackAndStaleIntent(t *testing.T) {
	for _, kind := range []string{"rollback", "stale-audit", "blocked", "revoked-publication"} {
		t.Run(kind, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			p := scheduledPost(t, f, posts.Patch{})
			switch kind {
			case "rollback":
				query(t, f.db, `ALTER TABLE owned_post_schedule_events ADD CONSTRAINT synthetic_pin_schedule_audit_failure CHECK(post_id<>'`+p.ID+`'::uuid OR kind<>'edited') NOT VALID`)
			case "stale-audit":
				query(t, f.db, `WITH changed AS (UPDATE owned_text_posts SET content='Unrelated audited change' WHERE id=$1 RETURNING id) INSERT INTO owned_text_post_audit(actor_id,post_id,action,occurred_at) SELECT $2,id,'update',clock_timestamp() FROM changed`, p.ID, f.actor)
			case "blocked":
				query(t, f.db, `UPDATE owned_post_schedules SET state='blocked',failure='post_changed',finished_at=clock_timestamp() WHERE id=$1`, p.Schedule.ID)
			case "revoked-publication":
				if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "posts.pin"}}, nil); e != nil {
					t.Fatal(e)
				}
			}
			before := pinSnapshot(t, f.db, p.ID)
			schedule := pinScheduleSnapshot(t, f.db, p.Schedule.ID, true)
			_, e := f.posts.Pin(f.ctx, f.actor, p.ID, posts.PublisherPin)
			if kind == "rollback" {
				if e == nil || pinSnapshot(t, f.db, p.ID) != before || pinScheduleSnapshot(t, f.db, p.Schedule.ID, true) != schedule {
					t.Fatal("schedule audit failure partially committed pin or intent", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if kind == "stale-audit" || kind == "blocked" {
				if pinScheduleSnapshot(t, f.db, p.Schedule.ID, true) != schedule {
					t.Fatal("pin repaired stale or blocked intent")
				}
			}
			if kind != "blocked" {
				scheduleDue(t, f.db, p)
				processSchedule(t, f)
			}
			scheduleState(t, f.db, p.Schedule.ID, "blocked")
			scheduleHidden(t, f, p.ID)
		})
	}
}

func TestPGPostPinConcurrentScheduledContentEdit(t *testing.T) {
	f := newPostSchedulingFixture(t)
	editors := accountpg.NewWithAuthority(f.db, f.guard).WithRealms(socialpg.New(f.db))
	if _, e := editors.ChangeMember(f.ctx, f.actor, f.pub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e := editors.ChangeMember(f.otherCtx, f.other, f.pub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	p := scheduledPost(t, f, posts.Patch{})
	original := pinScheduleSnapshot(t, f.db, p.Schedule.ID, false)
	actorCtx, actorCancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer actorCancel()
	editorCtx, editorCancel := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer editorCancel()
	done := make(chan error, 2)
	go func() { _, e := f.posts.Pin(editorCtx, f.other, p.ID, posts.PublisherPin); done <- e }()
	go func() {
		_, e := f.posts.Update(actorCtx, f.actor, p.ID, posts.Patch{Content: postString("Concurrent scheduled edit")})
		done <- e
	}()
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if pinScheduleSnapshot(t, f.db, p.Schedule.ID, false) != original {
		t.Fatal("concurrent metadata edit replaced original publication intent")
	}
	scheduleDue(t, f.db, p)
	processSchedule(t, f)
	scheduleState(t, f.db, p.Schedule.ID, "published")
	got, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil || got.Content != "Concurrent scheduled edit" || got.PinMode == nil || *got.PinMode != posts.PublisherPin {
		t.Fatal("lost pin or content edit", got, e)
	}
}
