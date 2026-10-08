// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

func postRealmFixture(t *testing.T) *realmPublishingFixture {
	t.Helper()
	f := newRealmPublishingFixture(t)
	var ready bool
	if e := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_text_posts'::regclass AND conname='owned_text_posts_realm_fk' AND convalidated)`).Scan(&ready); e != nil || !ready {
		t.Fatal("explicit guarded post realm migration required", e)
	}
	return f
}
func postRealmCreate(t *testing.T, f *realmPublishingFixture, ctx context.Context, actor, publisher, realm, body string) posts.Post {
	t.Helper()
	p, e := f.posts.Create(ctx, actor, publisher, posts.Patch{Content: &body, RealmID: &realm})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func postRealmEditor(t *testing.T, f *realmPublishingFixture, actor string, ctx context.Context) {
	t.Helper()
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.individual.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: actor, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.accounts.ChangeMember(ctx, actor, f.individual.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
}
func postRealmHTTP(t *testing.T, f *realmPublishingFixture, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(f.ctx)
	r.Header.Set("Authorization", "Bearer "+f.token)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	posts.NewVerifiedHandler(f.posts, f.verifier).ServeHTTP(w, r)
	return w
}

func TestPGPostRealmAssociationLifecycleAndFeeds(t *testing.T) {
	f := postRealmFixture(t)
	w := postRealmHTTP(t, f, "POST", "/api/posts?pub="+f.individual.Name, `{"content":"Explicit private realm post","realm_id":"`+f.private.ID+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p posts.Post
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil || p.RealmID == nil || *p.RealmID != f.private.ID {
		t.Fatal("realm post wire association", p, e)
	}
	if _, e := f.posts.Get(context.Background(), "", p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private realm post anonymous", e)
	}
	if _, e := f.posts.Get(f.otherCtx, f.other, p.ID); e != nil {
		t.Fatal("joined member post read", e)
	}
	if _, e := f.posts.Update(f.otherCtx, f.other, p.ID, posts.Patch{Content: postString("realm membership is not editing")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("implicit realm editor", e)
	}
	postRealmEditor(t, f, f.outside, f.outsideCtx)
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Invite, AccountID: f.outside}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.posts.Create(f.outsideCtx, f.outside, f.individual.ID, posts.Patch{Content: postString("pending is not membership"), RealmID: &f.private.ID}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("pending realm attached", e)
	}
	if _, e := f.posts.Get(f.outsideCtx, f.outside, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("publisher editor bypassed realm", e)
	}
	w = postRealmHTTP(t, f, "PATCH", "/api/posts/"+p.ID, `{"title":"Omitted realm is preserved"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil || p.RealmID == nil || *p.RealmID != f.private.ID {
		t.Fatal("omitted realm detached", p, e)
	}
	w = postRealmHTTP(t, f, "PATCH", "/api/posts/"+p.ID, `{"realm_id":"`+f.public.ID+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := f.posts.Get(context.Background(), "", p.ID); e != nil {
		t.Fatal("explicit public realm move", e)
	}
	w = postRealmHTTP(t, f, "PATCH", "/api/posts/"+p.ID, `{"realm_id":null}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	p = posts.Post{}
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil || p.RealmID != nil {
		t.Fatal("explicit null did not detach", p, e)
	}
	privatePost := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "Private realm filtered post")
	publicPost := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.public.ID, "Public realm filtered post")
	_ = f.post(t, f.privatePub, "Organization implicit realm is separate", posts.Public)
	if _, e := f.posts.Create(f.ctx, f.actor, f.privatePub.ID, posts.Patch{Content: postString("ambiguous dual realm"), RealmID: &f.public.ID}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("organization gained independent realm association", e)
	}
	items, total, e := f.timeline.Search(f.ctx, f.actor, timeline.SearchOptions{Realm: f.private.Slug, Take: 20})
	if e != nil || total != 1 || len(items) != 1 || items[0].ID != privatePost.ID {
		t.Fatal("realm search scope", items, total, e)
	}
	items, total, e = f.posts.List(f.otherCtx, f.other, posts.ListOptions{Realm: f.private.ID, Take: 20})
	if e != nil || total != 1 || len(items) != 1 || items[0].ID != privatePost.ID {
		t.Fatal("realm list scope", items, total, e)
	}
	page, e := f.timeline.Feed(f.ctx, f.actor, timeline.Options{Realm: f.private.Slug, Take: 20, Mode: sphere.Latest})
	if e != nil || len(page.Items) != 1 || page.Items[0].Data == nil || page.Items[0].Data.ID != privatePost.ID {
		t.Fatal("realm timeline scope", page, e)
	}
	items, total, e = f.timeline.Search(context.Background(), "", timeline.SearchOptions{Realm: f.public.Slug, Take: 20})
	if e != nil || total != 1 || items[0].ID != publicPost.ID {
		t.Fatal("public realm filter", items, total, e)
	}
	if _, _, e = f.timeline.Search(f.outsideCtx, f.outside, timeline.SearchOptions{Realm: f.private.Slug, Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("private realm filter exposed", e)
	}
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT post_realm_attach_failure CHECK(post_id<>'`+p.ID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS post_realm_attach_failure`)
	})
	_, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.private.ID, Content: postString("rollback attachment")})
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "post_realm_attach_failure" {
		t.Fatal("wrong attachment rollback error", e)
	}
	got, e := f.posts.Get(context.Background(), "", p.ID)
	if e != nil || got.RealmID != nil || got.Content != "Explicit private realm post" {
		t.Fatal("partial realm association after rollback", got, e)
	}
}

func TestPGPostRealmMediaSurveyInteractionPrivacy(t *testing.T) {
	f := postRealmFixture(t)
	body := "Synthetic linked-realm attachment bytes"
	file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("post-realm-private", body, false), strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Linked realm with attachment"), RealmID: &f.private.ID, Attachments: mediaIDs(file.ID)})
	if e != nil {
		t.Fatal(e)
	}
	if _, stream, e := f.repo.Open(context.Background(), "", file.ID); !errors.Is(e, media.ErrNotFound) || stream != nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("private post realm media", e)
	}
	_, stream, e := f.repo.Open(f.otherCtx, f.other, file.ID)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(stream)
	stream.Close()
	if e != nil || string(data) != body {
		t.Fatal("member attachment bytes", e)
	}
	q := surveyQuestions()
	survey, e := f.surveys.Create(f.ctx, f.actor, f.individual.ID, surveys.Patch{Title: postString("Synthetic linked realm choices"), Questions: &q, PostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	survey, e = f.surveys.Transition(f.ctx, f.actor, survey.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Get(context.Background(), "", survey.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("linked private realm survey intro", e)
	}
	if _, e = f.surveys.Answer(f.otherCtx, f.other, survey.ID, surveyAnswers(survey)); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.interactions.React(f.otherCtx, f.other, p.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.public.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Get(context.Background(), "", survey.ID); e != nil {
		t.Fatal("explicit public realm move did not update survey visibility", e)
	}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.private.ID}); e != nil {
		t.Fatal(e)
	}
	reply, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Visible reply with hidden target"), RepliedPostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	got, e := f.posts.Get(context.Background(), "", reply.ID)
	if e != nil || got.RepliedPost != nil || got.RepliedPostID != nil {
		t.Fatal("linked realm nested projection", got, e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, stream, e = f.repo.Open(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) || stream != nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("removed realm media access", e)
	}
	if _, e = f.surveys.Get(f.otherCtx, f.other, survey.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("removed realm survey intro", e)
	}
	if _, e = f.surveys.Answer(f.otherCtx, f.other, survey.ID, surveyAnswers(survey)); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("removed realm survey mutation", e)
	}
	if _, _, e = f.interactions.React(f.otherCtx, f.other, p.ID, interactions.ReactionRequest{Symbol: "heart"}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("removed realm reaction", e)
	}
}

func postRealmOtherPublisher(t *testing.T, f *realmPublishingFixture) accounts.Publisher {
	t.Helper()
	name := "synthetic-post-realm-other-" + f.other
	p, e := f.accounts.CreatePublisher(f.otherCtx, f.other, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func postRealmPostBlock(t *testing.T, ctx context.Context, db *sql.DB, id string) *sql.Tx {
	t.Helper()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_text_posts WHERE id=$1 FOR UPDATE`, id); e != nil {
		t.Fatal(e)
	}
	return tx
}

func TestPGPostRealmRevokeAndDeleteOrdering(t *testing.T) {
	f := postRealmFixture(t)
	pub := postRealmOtherPublisher(t, f)
	p := postRealmCreate(t, f, f.otherCtx, f.other, pub.ID, f.private.ID, "Retained private realm content")
	ctx, cancel := context.WithTimeout(f.otherCtx, 25*time.Second)
	defer cancel()
	w, probe := realmPublishingProbe(f)
	block := realmPublishingBlock(t, ctx, f.db, f.private.ID)
	done := make(chan error, 1)
	go func() { _, e := w.Update(ctx, f.other, p.ID, posts.Patch{RealmID: &f.public.ID}); done <- e }()
	realmPublishingWait(t, ctx, f.db, timelineEntered(t, ctx, probe), "owned_realms")
	check, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = check.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, pub.ID)
	_ = check.Rollback()
	if e != nil {
		t.Fatal("move locked publisher before realms", e)
	}
	if _, e = block.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$3 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("move ignored old realm revocation", e)
	}
	if _, e = w.Update(ctx, f.other, p.ID, posts.Patch{ClearRealm: true}); !errors.Is(e, posts.ErrForbidden) {
		t.Fatal("revoked member detached realm", e)
	}
	_ = timelineEntered(t, ctx, probe)
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Invite, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal(e)
	}
	// Hold an organization publisher only to stop the actual realm deletion
	// after it has acquired the realm. The individual post writer must wait.
	ownerCtx, stopOwner := context.WithTimeout(f.ctx, 25*time.Second)
	defer stopOwner()
	pubBlock := timelinePublisherBlock(t, ownerCtx, f.db, f.privatePub.ID)
	var blocker int
	if e = pubBlock.QueryRowContext(ownerCtx, `SELECT pg_backend_pid()`).Scan(&blocker); e != nil {
		t.Fatal(e)
	}
	deleted := make(chan error, 1)
	go func() { deleted <- f.social.DeleteRealm(ownerCtx, f.actor, f.private.ID) }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ownerCtx}, blocker, "owned_publishers")
	go func() {
		_, e := w.Update(ctx, f.other, p.ID, posts.Patch{Content: postString("must not survive realm deletion")})
		done <- e
	}()
	realmPublishingWait(t, ctx, f.db, timelineEntered(t, ctx, probe), "owned_realms")
	if e = pubBlock.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-deleted; e != nil {
		t.Fatal("canonical realm deletion", e)
	}
	if e = <-done; !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted realm write accepted", e)
	}
	var linked *string
	var content string
	var visibility int
	var tombstone bool
	e = f.db.QueryRow(`SELECT realm_id::text,content,visibility,deleted_at IS NOT NULL FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&linked, &content, &visibility, &tombstone)
	if e != nil || linked == nil || *linked != f.private.ID || content != "Retained private realm content" || visibility != posts.Public || tombstone {
		t.Fatal("realm deletion detached or rewrote post", linked, content, visibility, tombstone, e)
	}
	for _, actor := range []string{"", f.other} {
		if _, e = f.posts.Get(context.Background(), actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("deleted realm post exposed", actor, e)
		}
	}
	if e = f.posts.Delete(f.otherCtx, f.other, p.ID); e != nil {
		t.Fatal("publisher-authorized tombstone cleanup", e)
	}
	e = f.db.QueryRow(`SELECT realm_id::text,deleted_at IS NOT NULL FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&linked, &tombstone)
	if e != nil || linked == nil || *linked != f.private.ID || !tombstone {
		t.Fatal("soft deletion lost realm anchor", linked, tombstone, e)
	}
}

func TestPGPostRealmMoveReferenceRace(t *testing.T) {
	f := postRealmFixture(t)
	pub := postRealmOtherPublisher(t, f)
	target := postRealmCreate(t, f, f.otherCtx, f.other, pub.ID, f.private.ID, "Target before private move")
	slug := "synthetic-target-realm-" + f.other
	destination, e := f.social.CreateRealm(f.otherCtx, f.other, social.RealmPatch{Slug: &slug, Name: &slug})
	if e != nil {
		t.Fatal(e)
	}
	moveCtx, stopMove := context.WithTimeout(f.otherCtx, 20*time.Second)
	defer stopMove()
	replyCtx, stopReply := context.WithTimeout(f.ctx, 20*time.Second)
	defer stopReply()
	mover, mprobe := realmPublishingProbe(f)
	writer, wprobe := realmPublishingProbe(f)
	block := postRealmPostBlock(t, moveCtx, f.db, target.ID)
	moved, reply := make(chan error, 1), make(chan error, 1)
	go func() {
		_, e := mover.Update(moveCtx, f.other, target.ID, posts.Patch{RealmID: &destination.ID, Content: postString("New private target payload")})
		moved <- e
	}()
	realmPublishingWait(t, moveCtx, f.db, timelineEntered(t, moveCtx, mprobe), "owned_text_posts")
	go func() {
		_, e := writer.Create(replyCtx, f.actor, f.individual.ID, posts.Patch{Content: postString("Stale referenced realm reply"), RepliedPostID: &target.ID})
		reply <- e
	}()
	timelinePublisherWait(t, replyCtx, f.db, timelineEntered(t, replyCtx, wprobe))
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-moved; e != nil {
		t.Fatal("canonical post realm move", e)
	}
	if e = <-reply; !errors.Is(e, posts.ErrConflict) {
		t.Fatal("referenced post realm ancestry not rechecked", e)
	}
	if _, e = f.posts.Get(f.ctx, f.actor, target.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("moved target private payload exposed", e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Retry cannot read moved realm"), RepliedPostID: &target.ID}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("moved target visibility bypass", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.individual.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("failed reference race left post", count, e)
	}
}

func TestPGPostRealmOppositeMovesAndRollback(t *testing.T) {
	f := postRealmFixture(t)
	pub := postRealmOtherPublisher(t, f)
	a := postRealmCreate(t, f, f.ctx, f.actor, f.individual.ID, f.private.ID, "Opposite source A")
	b := postRealmCreate(t, f, f.otherCtx, f.other, pub.ID, f.public.ID, "Opposite source B")
	ac, stopA := context.WithTimeout(f.ctx, 20*time.Second)
	defer stopA()
	bc, stopB := context.WithTimeout(f.otherCtx, 20*time.Second)
	defer stopB()
	aw, aprobe := realmPublishingProbe(f)
	bw, bprobe := realmPublishingProbe(f)
	low, high := f.private.ID, f.public.ID
	if low > high {
		low, high = high, low
	}
	type result struct {
		post posts.Post
		err  error
	}
	run := func(failA bool) {
		block := realmPublishingBlock(t, ac, f.db, low)
		adone, bdone := make(chan result, 1), make(chan result, 1)
		ar, br := f.public.ID, f.private.ID
		if failA {
			ar, br = f.private.ID, f.public.ID
		}
		go func() {
			patch := posts.Patch{RealmID: &ar, Content: postString("Opposite moved A")}
			if failA {
				patch.Content = postString("Must roll back A")
				patch.Tags = strList("post-realm-move-rollback")
			}
			p, e := aw.Update(ac, f.actor, a.ID, patch)
			adone <- result{p, e}
		}()
		go func() {
			p, e := bw.Update(bc, f.other, b.ID, posts.Patch{RealmID: &br, Content: postString("Opposite moved B")})
			bdone <- result{p, e}
		}()
		for _, probe := range []*timelineLockProbe{aprobe, bprobe} {
			realmPublishingWait(t, ac, f.db, timelineEntered(t, ac, probe), "owned_realms")
		}
		check, e := f.db.BeginTx(ac, nil)
		if e != nil {
			t.Fatal(e)
		}
		_, e = check.ExecContext(ac, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE NOWAIT`, high)
		if e == nil {
			_, e = check.ExecContext(ac, `SELECT id FROM owned_publishers WHERE id IN($1::uuid,$2::uuid) FOR UPDATE NOWAIT`, f.individual.ID, pub.ID)
		}
		_ = check.Rollback()
		if e != nil {
			t.Fatal("opposite move took higher realm or publisher early", e)
		}
		if e = block.Commit(); e != nil {
			t.Fatal(e)
		}
		for i, done := range []chan result{adone, bdone} {
			var got result
			select {
			case got = <-done:
			case <-ac.Done():
				t.Fatal("opposite moves did not finish", ac.Err())
			}
			if failA && i == 0 {
				var pgerr *pgconn.PgError
				if !errors.As(got.err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "post_realm_move_failure" || got.post.ID != "" {
					t.Fatal("wrong move rollback error", got)
				}
				continue
			}
			want := ar
			if i == 1 {
				want = br
			}
			if got.err != nil || got.post.RealmID == nil || *got.post.RealmID != want {
				t.Fatal("opposite realm move", got)
			}
		}
	}
	run(false)
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT post_realm_move_failure CHECK(post_id<>'`+a.ID+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS post_realm_move_failure`)
	})
	run(true)
	var ar, br, content string
	var audits, tags int
	e := f.db.QueryRow(`SELECT (SELECT realm_id::text FROM owned_text_posts WHERE id=$1::uuid),(SELECT realm_id::text FROM owned_text_posts WHERE id=$2::uuid),(SELECT content FROM owned_text_posts WHERE id=$1::uuid),(SELECT count(*) FROM owned_text_post_audit WHERE post_id IN($1::uuid,$2::uuid)),(SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='post-realm-move-rollback')`, a.ID, b.ID).Scan(&ar, &br, &content, &audits, &tags)
	if e != nil || ar != f.public.ID || br != f.public.ID || content != "Opposite moved A" || audits != 5 || tags != 0 {
		t.Fatal("partial opposite realm move rollback", ar, br, content, audits, tags, e)
	}
}

func TestPGPostRealmFederationWithdrawal(t *testing.T) {
	x := newFederationFixture(t, false)
	if e := authoritypg.New(x.db).ReplacePolicy(x.ctx, x.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, x.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,1,$2)`, x.actor, x.config.Clock())
	v := socialpg.NewWithAuthority(x.db, x.guard)
	slug := "synthetic-federation-realm-" + x.actor
	realm, e := v.CreateRealm(x.ctx, x.actor, social.RealmPatch{Slug: &slug, Name: &slug})
	if e != nil {
		t.Fatal(e)
	}
	x.post.WithVisibility(v).WithAssociations(timelinepg.New(x.db).WithVisibility(v).WithAssociations(interactionpg.New(x.db).WithVisibility(v)))
	if e = x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	transport := &federationCapture{}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 10); e != nil || n != 1 {
		t.Fatal("synthetic acceptance", n, e)
	}
	for _, alreadySent := range []bool{false, true} {
		post := x.makePost(t, "Previously public synthetic content", posts.Public, false)
		if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
			t.Fatal("public reconciliation", n, e)
		}
		if alreadySent {
			if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
				t.Fatal("synthetic public delivery", n, e)
			}
		}
		before := len(transport.requests)
		privateBody := "Private realm payload must never be delivered"
		if _, e = x.post.Update(x.ctx, x.actor, post.ID, posts.Patch{RealmID: &realm.ID, Content: &privateBody}); e != nil {
			t.Fatal(e)
		}
		if _, e = x.r.Object(x.ctx, post.ID); !errors.Is(e, federation.ErrNotFound) {
			t.Fatal("realm-linked federation object", e)
		}
		out, e := x.r.Outbox(x.ctx, x.name, 0, 20)
		if e != nil || out.Total != 0 {
			t.Fatal("realm-linked federation outbox", out, e)
		}
		if !alreadySent {
			if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
				t.Fatal("pending delivery cancellation", n, e)
			}
			if len(transport.requests) != before {
				t.Fatal("pending realm payload reached transport")
			}
		}
		if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
			t.Fatal("realm attachment withdrawal", n, e)
		}
		if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
			t.Fatal("synthetic tombstone delivery", n, e)
		}
		if len(transport.requests) != before+1 {
			t.Fatal("expected exactly one withdrawal")
		}
		var activity federation.Activity
		if e = json.Unmarshal(transport.requests[before], &activity); e != nil || activity.Type != "Delete" || bytes.Contains(transport.requests[before], []byte(privateBody)) || bytes.Contains(activity.Object, []byte("Previously public")) {
			t.Fatal("withdrawal contains content", activity, e)
		}
		var public bool
		if e = x.db.QueryRow(`SELECT public FROM owned_federation_post_state WHERE post_id=$1`, post.ID).Scan(&public); e != nil || public {
			t.Fatal("realm post remains federated", public, e)
		}
	}
}

func TestPGPostRealmRestartPersistence(t *testing.T) {
	const owner = "d5100000-0000-4000-8000-000000004980"
	const member = "d5100000-0000-4000-8000-000000004981"
	const pubName = "post-publisher-4980"
	const realmSlug = "synthetic-post-realm-restart"
	const postSlug = "post-realm-restart"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		v := socialpg.New(db)
		p := postpg.New(db).WithVisibility(v)
		ctx := context.Background()
		if _, e := p.GetSlug(ctx, "", pubName, postSlug); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("restart realm post anonymous exposure", e)
		}
		got, e := p.GetSlug(ctx, member, pubName, postSlug)
		if e != nil || got.RealmID == nil || got.Content != "Durable individual realm association" {
			t.Fatal("restart post realm state", got, e)
		}
		r := timelinepg.New(db).WithVisibility(v)
		items, total, e := r.Search(ctx, member, timeline.SearchOptions{Realm: realmSlug, Take: 20})
		if e != nil || total != 1 || len(items) != 1 || items[0].ID != got.ID {
			t.Fatal("restart realm feed", items, total, e)
		}
		return
	}
	db := timelineDB(t)
	ctx := context.Background()
	actual, pub := postPublisher(t, db, 4980)
	if actual != owner || postAccount(t, db, 4981) != member {
		t.Fatal("restart fixture IDs")
	}
	for _, id := range []string{owner, member} {
		if e := authoritypg.New(db).ReplacePolicy(ctx, id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	issuer, verifier, guard, token := interactionAuthority(t, db, owner)
	proof, e := verifier.Authenticate(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	bound := guard.Bind(ctx, proof)
	memberProof, e := verifier.Authenticate(ctx, pgIssue(t, issuer, member).Token)
	if e != nil {
		t.Fatal(e)
	}
	memberCtx := guard.Bind(ctx, memberProof)
	query(t, db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,1,'2020-01-01')`, owner)
	v := socialpg.NewWithAuthority(db, guard)
	realm, e := v.CreateRealm(bound, owner, social.RealmPatch{Slug: postString(realmSlug), Name: postString("Synthetic durable post realm")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.ChangeMember(bound, owner, realm.ID, social.MemberChange{Action: social.Invite, AccountID: member}); e != nil {
		t.Fatal(e)
	}
	if _, e = v.ChangeMember(memberCtx, member, realm.ID, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal(e)
	}
	p := postpg.NewWithAuthority(db, guard).WithVisibility(v)
	if _, e = p.Create(bound, owner, pub.ID, posts.Patch{Content: postString("Durable individual realm association"), RealmID: &realm.ID, Slug: postString(postSlug)}); e != nil {
		t.Fatal(e)
	}
}
