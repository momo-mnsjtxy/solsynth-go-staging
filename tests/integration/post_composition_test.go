// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postembedpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
	timelineport "solsynth.local/go-migration/ports/sphere/timeline"
	"testing"
	"time"
)

type compositionFixture struct {
	postMediaFixture
	refs     []posts.Embed
	timeline *timelinepg.Repository
}

func compositionPool(db *sql.DB) (*postembedpg.Repository, *timelinepg.Repository) {
	v := socialpg.New(db)
	m := postmediapg.New(db, interactionpg.New(db).WithVisibility(v)).WithVisibility(v)
	links := postembedpg.New(db, m, v)
	return links, timelinepg.New(db).WithVisibility(v).WithAssociations(links)
}
func newCompositionFixture(t *testing.T) compositionFixture {
	t.Helper()
	return compositionFixtureFromMedia(t, newPostSchedulingFixture(t))
}

func newRestartCompositionFixture(t *testing.T, db *sql.DB) compositionFixture {
	t.Helper()
	f := newPostMediaFixtureInDatabase(t, db)
	f.config.Now = time.Now
	// Issue the original bounded credentials before creating any composer
	// target. The after phase has only public verification material.
	f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
	f = postSchedulingAuthority(t, f)
	return compositionFixtureFromMedia(t, f)
}

func compositionFixtureFromMedia(t *testing.T, media postMediaFixture) compositionFixture {
	t.Helper()
	f := compositionFixture{postMediaFixture: media}
	links, timeline := compositionPool(f.db)
	f.timeline = timeline
	if e := links.Ready(context.Background()); e != nil {
		t.Fatal("guarded runner must install composer schema", e)
	}
	f.posts.WithAssociations(timeline)
	survey, live, meet, day, event, fund := authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t), authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_surveys(id,publisher_id,title,is_anonymous,hide_results,status,questions,published_at,created_at,updated_at)VALUES($1,$2,'Synthetic survey',false,false,1,'[{"id":"q","type":0,"title":"Question","is_required":false}]',clock_timestamp(),clock_timestamp(),clock_timestamp())`, survey, f.pub.ID)
	query(t, f.db, `INSERT INTO owned_livestreams(id,publisher_id,title,type,visibility,status,room_name,created_at,updated_at)VALUES($1,$2,'Synthetic stream',0,0,0,'synthetic',clock_timestamp(),clock_timestamp())`, live, f.pub.ID)
	query(t, f.db, `INSERT INTO owned_meets(id,host_id,status,visibility,notes,expires_at,created_at,updated_at)VALUES($1,$2,0,1,'Private meeting metadata',clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp())`, meet, f.actor)
	query(t, f.db, `INSERT INTO owned_notable_days(id,definition,region,start_date,end_date,is_recurring,created_at,updated_at)VALUES($1,'{"name":"Synthetic notable day"}','US',clock_timestamp(),clock_timestamp()+interval '1 day',false,clock_timestamp(),clock_timestamp())`, day)
	query(t, f.db, `INSERT INTO owned_calendar_events(id,account_id,definition,start_time,end_time,visibility,is_recurring,created_at,updated_at)VALUES($1,$2,'{"title":"Private calendar metadata","time_zone":"UTC","tags":[]}',clock_timestamp(),clock_timestamp()+interval '1 day',0,false,clock_timestamp(),clock_timestamp())`, event, f.actor)
	wallet := walletCreate(t, walletpg.New(f.db), f.actor)
	query(t, f.db, `INSERT INTO wallet_v1.funds(id,creator_account_id,beneficiary_wallet_id,currency,total_amount,amount_of_splits,split_type,message,status,is_open,is_raising,target_amount,contribution_type,contribution_amount,expires_at,created_at,updated_at,fingerprint)VALUES($1,$2,$3,'synthetic-compose',1,1,0,'Private fund metadata',0,false,false,0,0,0,clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp(),repeat('0',64))`, fund, f.actor, wallet.ID)
	f.refs = []posts.Embed{{Type: "survey", ID: survey}, {Type: "fund", ID: fund}, {Type: "meet", ID: meet}, {Type: "live_stream", ID: live}, {Type: "notable_day", ID: day}, {Type: "calendar_event", ID: event}, {Type: "location", Name: postString("Explicit composer location"), WKT: postString("POINT(1 2)")}}
	return f
}
func compositionPost(t *testing.T, f compositionFixture, refs []posts.Embed) posts.Post {
	t.Helper()
	p, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Synthetic composed post"), Embeds: &refs})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func compositionKinds(p posts.Post) map[string]string {
	m := map[string]string{}
	if p.Meta != nil {
		for _, v := range p.Meta.Composed {
			m[v.Type] = v.ID
		}
	}
	return m
}
func assertCompositionKinds(t *testing.T, p posts.Post, want ...string) {
	t.Helper()
	got := compositionKinds(p)
	if len(got) != len(want) {
		t.Fatalf("composition kinds %v, want %v", got, want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s in %v", k, got)
		}
	}
}

func TestPGPostCompositionCanonicalReferencesAndReplay(t *testing.T) {
	f := newCompositionFixture(t)
	before := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions`)
	p := compositionPost(t, f, f.refs)
	assertCompositionKinds(t, p, "survey", "fund", "meet", "live_stream", "notable_day", "calendar_event", "location")
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var decoded posts.Post
	if e = json.Unmarshal(b, &decoded); e != nil {
		t.Fatal(e)
	}
	for _, ref := range f.refs {
		if ref.Type != "location" && compositionKinds(decoded)[ref.Type] != ref.ID {
			t.Fatal("canonical identity changed", ref, decoded.Meta)
		}
	}
	for i := 0; i < 2; i++ {
		p, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Embeds: &f.refs})
		if e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 7 {
		t.Fatal("duplicate associations", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions`); n != before {
		t.Fatal("link creation spent funds", before, n)
	}
	public, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, public, "survey", "live_stream", "notable_day", "location")
	raw, _ := json.Marshal(public)
	for _, secret := range []string{f.refs[1].ID, f.refs[2].ID, f.refs[5].ID, "Private meeting metadata", "Private calendar metadata", "Private fund metadata"} {
		if containsJSON(raw, secret) {
			t.Fatal("private reference escaped", secret, string(raw))
		}
	}
	forward, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Forward"), ForwardedPostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	nested, e := f.posts.Get(context.Background(), "", forward.ID)
	if e != nil || nested.ForwardedPost == nil {
		t.Fatal(nested, e)
	}
	assertCompositionKinds(t, *nested.ForwardedPost, "survey", "live_stream", "notable_day", "location")
	reply, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Reply"), RepliedPostID: &p.ID, Embeds: &f.refs})
	if e != nil {
		t.Fatal(e)
	}
	thread, e := f.timeline.Thread(context.Background(), "", p.ID, timelineport.ThreadOptions{Take: 10, AncestorLimit: 10, Ancestors: true})
	if e != nil || len(thread.Descendants) != 1 {
		t.Fatal(reply.ID, thread, e)
	}
	assertCompositionKinds(t, thread.Current.Post, "survey", "live_stream", "notable_day", "location")
	assertCompositionKinds(t, thread.Descendants[0].Post, "survey", "live_stream", "notable_day", "location")
	// Public meets still require canonical friendship or explicit nearby context.
	// Merely seeing a composed post supplies neither. Calendar friendship also
	// follows the current mutual graph instead of a saved post audience.
	query(t, f.db, `UPDATE owned_meets SET visibility=0 WHERE id=$1`, f.refs[2].ID)
	query(t, f.db, `UPDATE owned_calendar_events SET visibility=100 WHERE id=$1`, f.refs[5].ID)
	for _, viewer := range []string{"", f.other} {
		got, e := f.posts.Get(context.Background(), viewer, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		assertCompositionKinds(t, got, "survey", "live_stream", "notable_day", "location")
	}
	socialRepo := socialpg.NewWithAuthority(f.db, f.guard)
	if _, e = socialRepo.ChangeRelationship(f.ctx, f.actor, f.other, social.RelationshipChange{Action: social.RequestFriend}); e != nil {
		t.Fatal(e)
	}
	if _, e = socialRepo.ChangeRelationship(f.otherCtx, f.other, f.actor, social.RelationshipChange{Action: social.AcceptFriend}); e != nil {
		t.Fatal(e)
	}
	friend, e := f.posts.Get(f.otherCtx, f.other, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, friend, "survey", "meet", "calendar_event", "live_stream", "notable_day", "location")
	if _, e = socialRepo.ChangeRelationship(f.otherCtx, f.other, f.actor, social.RelationshipChange{Action: social.Forget}); e != nil {
		t.Fatal(e)
	}
	former, e := f.posts.Get(f.otherCtx, f.other, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, former, "survey", "live_stream", "notable_day", "location")
}
func containsJSON(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}

func TestPGPostCompositionOwnershipAndAtomicRollback(t *testing.T) {
	f := newCompositionFixture(t)
	p := compositionPost(t, f, f.refs)
	before := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID)
	foreign := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_meets(id,host_id,status,visibility,expires_at,created_at,updated_at)VALUES($1,$2,0,0,clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp())`, foreign, f.other)
	bad := []posts.Embed{{Type: "meet", ID: foreign}}
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Content: postString("Must roll back"), Embeds: &bad}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal(e)
	}
	got, e := f.posts.Get(f.ctx, f.actor, p.ID)
	if e != nil || got.Content != p.Content || len(compositionKinds(got)) != 7 {
		t.Fatal(got, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID); n != before {
		t.Fatal("rollback audit", n, before)
	}
	foreignMedia := f.postMediaFixture
	foreignMedia.actor = f.other
	foreignMedia.ctx = f.otherCtx
	foreignFile := privatePostFile(t, foreignMedia, "foreign-composer-media", "private bytes")
	onlySurvey := []posts.Embed{f.refs[0]}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Embeds: &onlySurvey, Attachments: mediaIDs(foreignFile.ID)}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("private media copied through composition", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 7 {
		t.Fatal("reference replacement escaped media rollback", n)
	}
	file := privatePostFile(t, f.postMediaFixture, "compose-thumbnail", "thumbnail bytes")
	article := posts.Article
	one := []posts.Embed{f.refs[0]}
	attached, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Article"), Type: &article, Attachments: mediaIDs(file.ID), Embeds: &one, ThumbnailID: &file.ID, ThumbnailSet: true})
	if e != nil || attached.Meta == nil || attached.Meta.Thumbnail == nil || *attached.Meta.Thumbnail != file.ID {
		t.Fatal(attached, e)
	}
	empty := []string{}
	if _, e = f.posts.Update(f.ctx, f.actor, attached.ID, posts.Patch{Attachments: &empty}); e == nil {
		t.Fatal("detached thumbnail accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_media WHERE post_id=$1`, attached.ID); n != 1 {
		t.Fatal("media mutation escaped rollback", n)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, attached.ID, posts.Patch{Attachments: &empty, ThumbnailSet: true}); e != nil {
		t.Fatal(e)
	}
	if e = f.posts.Delete(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 0 {
		t.Fatal("deleted links retained", n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_surveys WHERE id=$1 AND deleted_at IS NULL`, f.refs[0].ID); n != 1 {
		t.Fatal("target lifecycle changed", n)
	}
}

func TestPGPostCompositionLivePrivacyExpiryAndPublication(t *testing.T) {
	f := newCompositionFixture(t)
	p := compositionPost(t, f, f.refs)
	query(t, f.db, `UPDATE owned_livestreams SET visibility=2 WHERE id=$1`, f.refs[3].ID)
	query(t, f.db, `UPDATE owned_surveys SET ended_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.refs[0].ID)
	query(t, f.db, `UPDATE owned_notable_days SET deleted_at=clock_timestamp() WHERE id=$1`, f.refs[4].ID)
	got, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, got, "location")
	// Fresh drafts can retain a valid owned reference, but due publication must
	// refuse a target whose lifecycle becomes unavailable after scheduling.
	ref := []posts.Embed{f.refs[2]}
	draft := scheduledPost(t, f.postMediaFixture, posts.Patch{Embeds: &ref})
	query(t, f.db, `UPDATE owned_meets SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.refs[2].ID)
	scheduleDue(t, f.db, draft)
	if _, e = f.posts.ProcessScheduled(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, draft.Schedule.ID, "blocked")
	scheduleHidden(t, f.postMediaFixture, draft.ID)
	if _, e = f.posts.Publish(f.ctx, f.actor, draft.ID); e == nil {
		t.Fatal("expired target published")
	}
	blank := []posts.Embed{}
	if _, e = f.posts.Update(f.ctx, f.actor, draft.ID, posts.Patch{Embeds: &blank}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Publish(f.ctx, f.actor, draft.ID); e != nil {
		t.Fatal("explicit removal did not recover draft", e)
	}
}

func TestPGPostCompositionTargetWaitRechecksLifecycle(t *testing.T) {
	for _, target := range []struct {
		index           int
		table, mutation string
	}{
		{0, "owned_surveys", "status=2"}, {1, "wallet_v1.funds", "status=4"}, {2, "owned_meets", "status=3"},
		{3, "owned_livestreams", "status=2,operation_state='ending'"}, {4, "owned_notable_days", "deleted_at=clock_timestamp()"}, {5, "owned_calendar_events", "deleted_at=clock_timestamp()"},
	} {
		t.Run(target.table, func(t *testing.T) {
			f := newCompositionFixture(t)
			p := compositionPost(t, f, []posts.Embed{})
			ref := f.refs[target.index]
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, target.table, ref.ID)
			done := make(chan error, 1)
			go func() {
				_, e := f.posts.Update(ctx, f.actor, p.ID, posts.Patch{Content: postString("Must roll back after target wait"), Embeds: &[]posts.Embed{ref}})
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, target.table)
			if _, e := blocker.ExecContext(ctx, "UPDATE "+target.table+" SET "+target.mutation+" WHERE id=$1", ref.ID); e != nil {
				t.Fatal(e)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, posts.ErrNotFound) {
				t.Fatal(e)
			}
			got, e := f.posts.Get(f.ctx, f.actor, p.ID)
			if e != nil || got.Content != p.Content {
				t.Fatal(got, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 0 {
				t.Fatal("links escaped rollback", n)
			}
		})
	}
}

func compositionAuthorityState(t *testing.T, db *sql.DB, actor, other string) string {
	t.Helper()
	var state string
	e := db.QueryRow(`SELECT jsonb_build_object(
 'sessions',(SELECT jsonb_agg(jsonb_build_object('id',id,'account',account_id,'expires_at',expires_at,'revoked_at',revoked_at,'epoch',epoch,'account_version',account_version,'scopes',scopes) ORDER BY id) FROM owned_authority_sessions WHERE account_id IN($1,$2)),
 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_grants g WHERE g.account_id IN($1,$2)),
 'members',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM owned_publisher_members m WHERE m.account_id IN($1,$2)))::text`, actor, other).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	return state
}

// Capture all persisted composer targets and canonical rows, including original
// fund terms/status. No after-phase mutation may make an expired target reappear.
func compositionObjectState(t *testing.T, db *sql.DB, postID string) string {
	t.Helper()
	var state string
	e := db.QueryRow(`SELECT jsonb_build_object(
 'post',(SELECT to_jsonb(p) FROM owned_text_posts p WHERE p.id=$1),
 'links',(SELECT jsonb_agg(to_jsonb(e) ORDER BY e.kind,e.position) FROM owned_post_embeds e WHERE e.post_id=$1),
 'survey',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM owned_surveys s WHERE s.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='survey')),
 'fund',(SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM wallet_v1.funds f WHERE f.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='fund')),
 'meet',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM owned_meets m WHERE m.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='meet')),
 'stream',(SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM owned_livestreams l WHERE l.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='live_stream')),
 'day',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM owned_notable_days d WHERE d.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='notable_day')),
 'event',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM owned_calendar_events c WHERE c.id IN(SELECT target_id FROM owned_post_embeds WHERE post_id=$1 AND kind='calendar_event')))::text`, postID).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	return state
}

func TestPGPostCompositionSavedRestartPersistence(t *testing.T) {
	type saved struct {
		PostID, Actor, Other, Token, OtherToken, Content, AuthorityState, ObjectState string
		Database                                                                      isolatedRestartDatabase
		Refs                                                                          []posts.Embed
		Authority                                                                     restartAuthoritySnapshot
		Audit                                                                         int
	}
	const marker = "post-composition-state.json"
	if fixtureRestartPhase(t) == "before" {
		db, identity := createIsolatedRestartDatabase(t, "composer")
		f := newRestartCompositionFixture(t, db)
		p := compositionPost(t, f, f.refs)
		saveRestartFixture(t, marker, saved{Database: identity, ObjectState: compositionObjectState(t, db, p.ID), PostID: p.ID, Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Content: p.Content, Refs: f.refs, Authority: saveAuthoritySnapshot(t, f.config), AuthorityState: compositionAuthorityState(t, db, f.actor, f.other), Audit: commerceCount(t, db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID)})
		return
	}
	var state saved
	loadRestartFixture(t, marker, &state)
	db := openIsolatedRestartDatabase(t, state.Database)
	if got := compositionObjectState(t, db, state.PostID); got != state.ObjectState {
		t.Fatal("original composer objects changed across restart")
	}
	if got := compositionAuthorityState(t, db, state.Actor, state.Other); got != state.AuthorityState {
		t.Fatal("original authority changed across restart")
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), restoreAuthoritySnapshot(t, state.Authority))
	if e != nil {
		t.Fatal(e)
	}
	links, timeline := compositionPool(db)
	if e := links.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	repo := postpg.NewWithAuthority(db, guard).WithVisibility(socialpg.NewWithAuthority(db, guard)).WithAssociations(timeline)
	for _, viewer := range []struct{ actor, token string }{{state.Actor, state.Token}, {state.Other, state.OtherToken}, {"", ""}} {
		readCtx := context.Background()
		if viewer.actor != "" {
			proof, e := verifier.Authenticate(context.Background(), viewer.token)
			if e != nil || proof.Account.ID != viewer.actor {
				t.Fatal("original credential unavailable", e)
			}
			readCtx = guard.Bind(readCtx, proof)
		}
		p, e := repo.Get(readCtx, viewer.actor, state.PostID)
		if e != nil || p.Content != state.Content {
			t.Fatal(p, e)
		}
		if viewer.actor == state.Actor {
			assertCompositionKinds(t, p, "survey", "fund", "meet", "live_stream", "notable_day", "calendar_event", "location")
			for _, ref := range state.Refs {
				if compositionKinds(p)[ref.Type] != ref.ID {
					t.Fatal("saved target identity changed", ref.Type)
				}
			}
		} else {
			assertCompositionKinds(t, p, "survey", "live_stream", "notable_day", "location")
			raw, _ := json.Marshal(p)
			for _, ref := range state.Refs {
				if ref.Type == "fund" || ref.Type == "meet" || ref.Type == "calendar_event" {
					if containsJSON(raw, ref.ID) {
						t.Fatal("private saved target leaked", ref.Type)
					}
				}
			}
		}
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, state.PostID); n != len(state.Refs) {
		t.Fatal("saved reference rows changed", n)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, state.PostID); n != state.Audit {
		t.Fatal("restart read mutated saved fixture", n, state.Audit)
	}
	if got := compositionAuthorityState(t, db, state.Actor, state.Other); got != state.AuthorityState {
		t.Fatal("restart reads mutated authority")
	}
	if got := compositionObjectState(t, db, state.PostID); got != state.ObjectState {
		t.Fatal("restart reads mutated composer objects")
	}
}

func TestPGBackendPostCompositionAuthoredRoutes(t *testing.T) {
	f := newCompositionFixture(t)
	app := backendApp(t, f.db, f.config)
	p := backendDecode[posts.Post](t, backendJSONCall(t, app, "POST", "/sphere/posts?pub="+f.pub.ID, f.token, map[string]any{"content": "Composed alias request", "embeds": f.refs}, 200))
	assertCompositionKinds(t, p, "survey", "fund", "meet", "live_stream", "notable_day", "calendar_event", "location")
	public := backendDecode[posts.Post](t, backendCall(t, app, "GET", "/api/posts/"+p.ID, "", "", 200))
	assertCompositionKinds(t, public, "survey", "live_stream", "notable_day", "location")
	edited := backendDecode[posts.Post](t, backendJSONCall(t, app, "PATCH", "/sphere/posts/"+p.ID, f.token, map[string]any{"calendar_event_id": nil, "location_name": "Changed explicit location", "location_wkt": "POINT(3 4)"}, 200))
	if _, exists := compositionKinds(edited)["calendar_event"]; exists {
		t.Fatal("legacy null did not clear calendar")
	}
	backendJSONCall(t, app, "PATCH", "/sphere/posts/"+p.ID, f.otherToken, map[string]any{"embeds": []any{}}, 403)
	cleared := backendDecode[posts.Post](t, backendJSONCall(t, app, "PATCH", "/api/posts/"+p.ID, f.token, map[string]any{"embeds": []any{}}, 200))
	assertCompositionKinds(t, cleared)
}
