// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/socialpg"
	content "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func publishingApp(t *testing.T, f postMediaFixture) *application.Backend {
	t.Helper()
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app
}
func publishingRepo(f postMediaFixture) *accountpg.Repository {
	return accountpg.NewWithAuthority(f.db, f.guard).WithRealms(socialpg.New(f.db))
}
func publishingExtra(t *testing.T, f postMediaFixture, actor string) accounts.Publisher {
	t.Helper()
	name := "publishing-" + authorityUUID(t)
	p, e := accountpg.NewForCompatibilityTests(f.db).CreatePublisher(context.Background(), actor, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func publishingPatch(t *testing.T, h http.Handler, token string, p accounts.PublishingSettingsPatch) accounts.PublishingSettings {
	t.Helper()
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	return backendDecode[accounts.PublishingSettings](t, backendCall(t, h, "PATCH", "/sphere/account/publishing", token, string(b), 200))
}
func TestPGPublishingSettingsHTTPDefaultsReplyAndNulls(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	reply := publishingExtra(t, f, f.actor)
	backendCall(t, h, "GET", "/api/account/publishing", "", "", 401)
	s := backendDecode[accounts.PublishingSettings](t, backendCall(t, h, "GET", "/api/account/publishing", f.token, "", 200))
	if !authority.ValidID(s.ID) || s.AccountID != f.actor || s.DefaultPostingPublisherID != nil || s.DefaultReplyPublisherID != nil || s.DefaultFediversePublisherID != nil || s.CreatedAt.IsZero() {
		t.Fatal(s)
	}
	before := s.ID
	s = publishingPatch(t, h, f.token, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.pub.ID, DefaultReplyPublisherID: &reply.ID})
	if s.ID != before || s.UpdatedAt == nil {
		t.Fatal(s)
	}
	s = publishingPatch(t, h, f.token, accounts.PublishingSettingsPatch{})
	if s.DefaultPostingPublisherID == nil || *s.DefaultPostingPublisherID != f.pub.ID || s.DefaultReplyPublisherID == nil || *s.DefaultReplyPublisherID != reply.ID {
		t.Fatal("null erased choices", s)
	}
	parent := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/sphere/posts", f.token, `{"content":"Default original"}`, 200))
	child := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts", f.token, fmt.Sprintf(`{"content":"Default reply","replied_post_id":%q}`, parent.ID), 200))
	if parent.PublisherID != f.pub.ID || child.PublisherID != reply.ID || child.RepliedPostID == nil || *child.RepliedPostID != parent.ID {
		t.Fatal(parent, child)
	}
	explicit := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts?pub="+f.pub.ID, f.token, fmt.Sprintf(`{"content":"Explicit override","replied_post_id":%q}`, parent.ID), 200))
	if explicit.PublisherID != f.pub.ID {
		t.Fatal(explicit)
	}
	other := backendDecode[accounts.PublishingSettings](t, backendCall(t, h, "GET", "/api/account/publishing", f.otherToken, "", 200))
	if other.AccountID != f.other || other.ID == s.ID || other.DefaultPostingPublisherID != nil {
		t.Fatal(other)
	}
	for _, body := range []string{`null`, `{"account_id":"` + f.other + `"}`, `{"default_posting_publisher_id":"bad"}`, `{"default_reply_publisher_id":null,"default_reply_publisher_id":null}`} {
		backendCall(t, h, "PATCH", "/api/account/publishing", f.token, body, 400)
	}
	backendCall(t, h, "POST", "/api/account/publishing", f.token, `{}`, 405)
}
func TestPGPublishingSettingsMembershipAtomicityAndCleanup(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	other := publishingExtra(t, f, f.other)
	reply := publishingExtra(t, f, f.actor)
	s := publishingPatch(t, h, f.token, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.pub.ID, DefaultReplyPublisherID: &reply.ID})
	backendCall(t, h, "PATCH", "/api/account/publishing", f.token, fmt.Sprintf(`{"default_posting_publisher_id":%q,"default_reply_publisher_id":%q}`, reply.ID, other.ID), 400)
	got, e := publishingRepo(f).PublishingSettings(f.ctx, f.actor)
	if e != nil || *got.DefaultPostingPublisherID != f.pub.ID || *got.DefaultReplyPublisherID != reply.ID {
		t.Fatal(got, e)
	}
	backendCall(t, h, "DELETE", "/api/publishers/"+f.pub.ID, f.token, "", 204)
	got, e = publishingRepo(f).PublishingSettings(f.ctx, f.actor)
	if e != nil || got.ID != s.ID || got.DefaultPostingPublisherID != nil || got.DefaultReplyPublisherID == nil || *got.DefaultReplyPublisherID != reply.ID {
		t.Fatal("deletion erased unrelated choice", got, e)
	}
	// Joined viewer may save a choice, but may not use it to gain editor rights.
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,0,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), other.ID, f.actor)
	publishingPatch(t, h, f.token, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &other.ID})
	p := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts", f.token, `{"content":"No viewer promotion"}`, 200))
	if p.PublisherID != reply.ID {
		t.Fatal(p)
	}
	// A now-ineligible default must not make the single active fallback look
	// ambiguous merely because the caller still has publisher editor membership.
	query(t, f.db, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1 AND account_id=$2`, other.ID, f.actor)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.other)
	p = backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts", f.token, `{"content":"Active fallback only"}`, 200))
	if p.PublisherID != reply.ID {
		t.Fatal("inactive owner retained in fallback", p)
	}
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1`, f.other)
	query(t, f.db, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1 AND account_id=$2`, other.ID, f.actor)
	query(t, f.db, `UPDATE owned_publisher_members SET joined_at=NULL WHERE publisher_id=$1 AND account_id=$2`, other.ID, f.actor)
	backendCall(t, h, "PATCH", "/api/account/publishing", f.token, fmt.Sprintf(`{"default_reply_publisher_id":%q}`, other.ID), 400)
	query(t, f.db, `UPDATE owned_publisher_members SET joined_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, other.ID, f.actor)
	if _, e = accountpg.NewWithAuthority(f.db, f.guard).ChangeMember(f.ctx, f.actor, other.ID, accounts.MemberChange{Action: accounts.LeavePublisher}); e != nil {
		t.Fatal(e)
	}
	got, e = publishingRepo(f).PublishingSettings(f.ctx, f.actor)
	if e != nil || got.DefaultPostingPublisherID != nil || got.DefaultReplyPublisherID == nil || *got.DefaultReplyPublisherID != reply.ID {
		t.Fatal(got, e)
	}
}
func TestPGPublishingSettingsFederationRequiresEnabledActor(t *testing.T) {
	f := newFederationFixture(t, true)
	r := accountpg.NewWithAuthority(f.db, f.guard).WithPublishingFederation(f.r)
	s, e := r.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultFediversePublisherID: &f.publisher})
	if e != nil || s.DefaultFediversePublisherID == nil || *s.DefaultFediversePublisherID != f.publisher {
		t.Fatal(s, e)
	}
	// A previously enabled actor remains anchored to its original origin. A
	// newly configured origin may not silently accept that actor as local.
	otherConfig := f.config
	otherConfig.Origin = "https://other-local.example.test"
	otherFederation, e := federationpg.New(f.db, otherConfig, f.keys, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	otherRepo := accountpg.NewWithAuthority(f.db, f.guard).WithPublishingFederation(otherFederation)
	if _, e = otherRepo.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.publisher, DefaultFediversePublisherID: &f.publisher}); !errors.Is(e, accounts.ErrPublishingFederation) {
		t.Fatal("accepted actor from a different origin", e)
	}
	query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=clock_timestamp() WHERE id=$1`, f.publisher)
	if _, e = r.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultFediversePublisherID: &f.publisher}); !errors.Is(e, accounts.ErrPublishingFederation) {
		t.Fatal("accepted unavailable local actor", e)
	}
	query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=NULL WHERE id=$1`, f.publisher)
	if e = f.r.Settings(f.ctx, f.actor, f.publisher, federation.Settings{Enabled: false, Manual: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.publisher, DefaultFediversePublisherID: &f.publisher}); !errors.Is(e, accounts.ErrPublishingFederation) {
		t.Fatal(e)
	}
	s, e = r.PublishingSettings(f.ctx, f.actor)
	if e != nil || s.DefaultPostingPublisherID != nil {
		t.Fatal("partial failed federation update", s, e)
	}
}
func TestPGPublishingSettingsWaitProofMembershipAndAuditRollback(t *testing.T) {
	for _, mode := range []string{"member", "proof", "owner", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			r := publishingRepo(f)
			s, e := r.PublishingSettings(f.ctx, f.actor)
			if e != nil {
				t.Fatal(e)
			}
			publisher := f.pub.ID
			if mode == "owner" {
				publisher = publishingExtra(t, f, f.other).ID
				query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,0,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), publisher, f.actor)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_publishers", publisher
			if mode != "member" {
				table, id = "owned_publishing_settings", s.ID
			}
			tx, pid := atomicBlocker(t, locks, table, id)
			done := make(chan error, 1)
			go func() {
				_, e := r.UpdatePublishingSettings(ctx, f.actor, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &publisher})
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, table)
			switch mode {
			case "member":
				if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, f.pub.ID, f.actor); e != nil {
					t.Fatal(e)
				}
			case "proof":
				query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.actor)
			case "owner":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
			case "cancel":
				cancel()
			}
			if mode == "cancel" {
				_ = tx.Rollback()
			} else if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("wait accepted changed rights")
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_publishing_settings WHERE account_id=$1 AND default_publisher_id IS NOT NULL`, f.actor); n != 0 {
				t.Fatal(n)
			}
		})
	}
	f := newPostSchedulingFixture(t)
	r := publishingRepo(f)
	before, e := r.PublishingSettings(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	name := "publishing_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_accounts_audit ADD CONSTRAINT `+name+` CHECK(actor_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() { query(t, f.db, `ALTER TABLE owned_accounts_audit DROP CONSTRAINT `+name) })
	if _, e = r.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.pub.ID}); e == nil {
		t.Fatal("failed audit accepted")
	}
	after, e := r.PublishingSettings(f.ctx, f.actor)
	if e != nil || after.ID != before.ID || after.UpdatedAt != nil || after.DefaultPostingPublisherID != nil {
		t.Fatal(after, e)
	}
}
func TestPGPublishingSettingsConcurrentMergeAndUnavailableProof(t *testing.T) {
	f := newPostSchedulingFixture(t)
	r := publishingRepo(f)
	reply := publishingExtra(t, f, f.actor)
	var wg sync.WaitGroup
	done := make(chan error, 2)
	for _, patch := range []accounts.PublishingSettingsPatch{{DefaultPostingPublisherID: &f.pub.ID}, {DefaultReplyPublisherID: &reply.ID}} {
		wg.Add(1)
		go func(p accounts.PublishingSettingsPatch) {
			defer wg.Done()
			_, e := r.UpdatePublishingSettings(f.ctx, f.actor, p)
			done <- e
		}(patch)
	}
	wg.Wait()
	close(done)
	for e := range done {
		if e != nil {
			t.Fatal(e)
		}
	}
	s, e := r.PublishingSettings(f.ctx, f.actor)
	if e != nil || s.DefaultPostingPublisherID == nil || s.DefaultReplyPublisherID == nil || *s.DefaultPostingPublisherID != f.pub.ID || *s.DefaultReplyPublisherID != reply.ID {
		t.Fatal(s, e)
	}
	if _, e = r.UpdatePublishingSettings(context.Background(), f.actor, accounts.PublishingSettingsPatch{}); e == nil {
		t.Fatal("missing proof accepted")
	}
	if _, e = accountpg.New(f.db).PublishingSettings(f.ctx, f.actor); !errors.Is(e, accounts.ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestPGPublishingSettingsAndBatchRequireCurrentRealmMembership(t *testing.T) {
	f := newRealmPublishingFixture(t)
	f.publisherEditor(t)
	s, e := f.accounts.UpdatePublishingSettings(f.otherCtx, f.other, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.privatePub.ID, DefaultReplyPublisherID: &f.individual.ID})
	if !errors.Is(e, accounts.ErrPublishingMembership) || s.ID != "" {
		t.Fatal("partially accepted foreign default", s, e)
	}
	s, e = f.accounts.UpdatePublishingSettings(f.otherCtx, f.other, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.privatePub.ID})
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.otherCtx, f.other, "", posts.Patch{Content: postString("Current organization default")})
	if e != nil || p.PublisherID != f.privatePub.ID {
		t.Fatal(p, e)
	}
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.private.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.accounts.UpdatePublishingSettings(f.otherCtx, f.other, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.privatePub.ID}); !errors.Is(e, accounts.ErrPublishingMembership) {
		t.Fatal(e)
	}
	private := posts.Private
	if e = f.posts.Batch(f.otherCtx, f.other, posts.BatchRequest{PostIDs: []string{p.ID}, Visibility: &private}, false); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("realm loss bypass", e)
	}
	if _, e = f.accounts.UpdatePublishingSettings(f.outsideCtx, f.outside, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.publicPub.ID}); !errors.Is(e, accounts.ErrPublishingMembership) {
		t.Fatal("public realm became membership", e)
	}
}

func TestPGPublishingSettingsDeletionPreservesUnrelatedChoices(t *testing.T) {
	for _, mode := range []string{"realm", "admin_realm", "admin_publisher"} {
		t.Run(mode, func(t *testing.T) {
			f := newModerationFixture(t)
			s, e := f.accounts.UpdatePublishingSettings(f.ctx, f.actor, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.privatePub.ID, DefaultReplyPublisherID: &f.individual.ID})
			if e != nil {
				t.Fatal(e)
			}
			query(t, f.db, `UPDATE owned_publishing_settings SET default_fediverse_publisher_id=$2 WHERE account_id=$1`, f.actor, f.privatePub.ID)
			switch mode {
			case "realm":
				e = f.social.DeleteRealm(f.ctx, f.actor, f.private.ID)
			case "admin_realm":
				_, e = f.admin.Mutate(f.outsideCtx, f.outside, content.RealmKind, f.private.ID, content.Command{Action: "delete"})
			case "admin_publisher":
				_, e = f.admin.Mutate(f.outsideCtx, f.outside, content.PublisherKind, f.privatePub.ID, content.Command{Action: "delete"})
			}
			if e != nil {
				t.Fatal(e)
			}
			got, e := f.accounts.PublishingSettings(f.ctx, f.actor)
			if e != nil || got.ID != s.ID || got.DefaultPostingPublisherID != nil || got.DefaultFediversePublisherID != nil || got.DefaultReplyPublisherID == nil || *got.DefaultReplyPublisherID != f.individual.ID {
				t.Fatal(mode, got, e)
			}
		})
	}
}

func batchPost(t *testing.T, h http.Handler, token, publisher string, private bool) posts.Post {
	t.Helper()
	body := `{"content":"Batch source"}`
	if private {
		body = `{"content":"Private batch source","visibility":3}`
	}
	return backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts?pub="+publisher, token, body, 200))
}
func batchBody(ids []string, visibility *int) string {
	b, _ := json.Marshal(posts.BatchRequest{PostIDs: ids, Visibility: visibility})
	return string(b)
}
func TestPGPostBatchHTTPAtomicVisibilityAndDelete(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	second := publishingExtra(t, f, f.actor)
	a := batchPost(t, h, f.token, f.pub.ID, false)
	b := batchPost(t, h, f.token, second.ID, false)
	private := posts.Private
	backendCall(t, h, "POST", "/sphere/posts/batch/visibility", f.token, batchBody([]string{b.ID, a.ID}, &private), 204)
	for _, p := range []posts.Post{a, b} {
		backendCall(t, h, "GET", "/api/posts/"+p.ID, "", "", 404)
		backendCall(t, h, "GET", "/api/posts/"+p.ID, f.token, "", 200)
	}
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q,%q]}`, a.ID, b.ID), 204)
	for _, p := range []posts.Post{a, b} {
		backendCall(t, h, "GET", "/api/posts/"+p.ID, f.token, "", 404)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE actor_id=$1 AND action='delete'`, f.actor); n != 2 {
		t.Fatal(n)
	}
	// A replay does not create a second deletion or lifecycle event.
	before := publishingSnapshot(t, f.db, f.actor)
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q,%q]}`, a.ID, b.ID), 400)
	if got := publishingSnapshot(t, f.db, f.actor); got != before {
		t.Fatal("deletion replay changed committed state")
	}
	// Public source posts may refer to a target the editor can read privately;
	// anonymous projections must still remove its ID and nested representation.
	other := publishingExtra(t, f, f.other)
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), other.ID, f.actor)
	target := batchPost(t, h, f.otherToken, other.ID, true)
	reply := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts?pub="+f.pub.ID, f.token, fmt.Sprintf(`{"content":"Batch private-parent reply","visibility":3,"replied_post_id":%q}`, target.ID), 200))
	forward := backendDecode[posts.Post](t, backendCall(t, h, "POST", "/api/posts?pub="+second.ID, f.token, fmt.Sprintf(`{"content":"Batch private-parent forward","visibility":3,"forwarded_post_id":%q}`, target.ID), 200))
	public := posts.Public
	backendCall(t, h, "POST", "/api/posts/batch/visibility", f.token, batchBody([]string{forward.ID, reply.ID}, &public), 204)
	for _, source := range []posts.Post{reply, forward} {
		got := backendDecode[posts.Post](t, backendCall(t, h, "GET", "/api/posts/"+source.ID, "", "", 200))
		if got.RepliedPostID != nil || got.RepliedPost != nil || got.ForwardedPostID != nil || got.ForwardedPost != nil {
			t.Fatal("private target leaked through batch publication", got)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_relations WHERE post_id=ANY($1::uuid[]) AND(replied_post_id=$2 OR forwarded_post_id=$2)`, []string{reply.ID, forward.ID}, target.ID); n != 2 {
		t.Fatal("batch lost canonical relations", n)
	}
}
func TestPGPostBatchRejectsPartialUnauthorizedLockedAndDuplicate(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	other := publishingExtra(t, f, f.other)
	a := batchPost(t, h, f.token, f.pub.ID, false)
	b := batchPost(t, h, f.otherToken, other.ID, true)
	private := posts.Private
	for _, tc := range []struct {
		ids    []string
		status int
	}{{[]string{a.ID, b.ID}, 403}, {[]string{a.ID, authorityUUID(t)}, 400}, {[]string{a.ID, a.ID}, 400}, {[]string{}, 400}} {
		backendCall(t, h, "POST", "/api/posts/batch/visibility", f.token, batchBody(tc.ids, &private), tc.status)
		backendCall(t, h, "GET", "/api/posts/"+a.ID, "", "", 200)
	}
	query(t, f.db, `UPDATE owned_text_posts SET locked_at=clock_timestamp() WHERE id=$1`, a.ID)
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q]}`, a.ID), 423)
	backendCall(t, h, "POST", "/api/posts/batch/visibility", f.token, fmt.Sprintf(`{"post_ids":[%q],"drafted_at":%q,"published_at":%q}`, a.ID, time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339)), 400)
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, `{"post_ids":[],"actor_id":"`+f.other+`"}`, 400)
}
func TestPGPostBatchAuditRollbackAndScheduledCancellation(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	a := batchPost(t, h, f.token, f.pub.ID, false)
	b := scheduledPost(t, f, posts.Patch{})
	name := "batch_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT `+name+` CHECK(post_id<>'`+b.ID+`'::uuid OR action<>'delete') NOT VALID`)
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q,%q]}`, a.ID, b.ID), 503)
	query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT `+name)
	backendCall(t, h, "GET", "/api/posts/"+a.ID, "", "", 200)
	scheduleState(t, f.db, b.Schedule.ID, "pending")
	backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q,%q]}`, b.ID, a.ID), 204)
	scheduleState(t, f.db, b.Schedule.ID, "cancelled")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE actor_id=$1 AND action='delete'`, f.actor); n != 2 {
		t.Fatal(n)
	}
}
func TestPGPostBatchReversedConcurrencyAndProofWait(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	second := publishingExtra(t, f, f.actor)
	a := batchPost(t, h, f.token, f.pub.ID, false)
	b := batchPost(t, h, f.token, second.ID, false)
	for _, pub := range []string{f.pub.ID, second.ID} {
		query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), pub, f.other)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	otherCtx, cancelOther := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancelOther()
	done := make(chan error, 2)
	private, public := posts.Private, posts.Public
	go func() {
		done <- f.posts.Batch(ctx, f.actor, posts.BatchRequest{PostIDs: []string{a.ID, b.ID}, Visibility: &private}, false)
	}()
	go func() {
		done <- f.posts.Batch(otherCtx, f.other, posts.BatchRequest{PostIDs: []string{b.ID, a.ID}, Visibility: &public}, false)
	}()
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(DISTINCT visibility) FROM owned_text_posts WHERE id=ANY($1::uuid[])`, []string{a.ID, b.ID}); n != 1 {
		t.Fatal("split batch commit", n)
	}
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	tx, pid := atomicBlocker(t, locks, "owned_text_posts", b.ID)
	go func() { done <- f.posts.Batch(ctx, f.actor, posts.BatchRequest{PostIDs: []string{b.ID, a.ID}}, true) }()
	waitAtomicBlocked(t, locks, pid, "owned_text_posts")
	query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.actor)
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("expired batch proof accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE id=ANY($1::uuid[]) AND deleted_at IS NOT NULL`, []string{a.ID, b.ID}); n != 0 {
		t.Fatal(n)
	}
}

func TestPGPostBatchPublicationRightsMediaAndFinalWait(t *testing.T) {
	f := newPostSchedulingFixture(t)
	h := publishingApp(t, f)
	items := []posts.Post{batchPost(t, h, f.token, f.pub.ID, true), batchPost(t, h, f.token, f.pub.ID, true)}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	file, _, e := f.repo.Upload(f.otherCtx, f.other, mediaUpload("borrowed-batch", "Borrowed bytes", true), strings.NewReader("Borrowed bytes"))
	if e != nil {
		t.Fatal(e)
	}
	blocked := privatePostFile(t, f, "blocked-batch", "Private bytes")
	for i, id := range []string{file.ID, blocked.ID} {
		if _, e = f.posts.Update(f.ctx, f.actor, items[i].ID, posts.Patch{Attachments: mediaIDs(id)}); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	tx, pid := atomicBlocker(t, locks, "owned_media_files", blocked.ID)
	done := make(chan error, 1)
	public := posts.Public
	go func() {
		done <- f.posts.Batch(ctx, f.actor, posts.BatchRequest{PostIDs: []string{items[0].ID, items[1].ID}, Visibility: &public}, false)
	}()
	waitAtomicBlocked(t, locks, pid, "owned_media_files")
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.other)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("earlier batch member ignored later media-owner loss")
	}
	for _, p := range items {
		backendCall(t, h, "GET", "/api/posts/"+p.ID, "", "", 404)
	}
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1`, f.other)
	now := time.Now().UTC()
	draft, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Batch publication proof"), DraftedAt: &now})
	if e != nil {
		t.Fatal(e)
	}
	// The original signed scope also caps publication, even while canonical
	// grants still allow both permissions.
	narrow, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "Batch-only synthetic scope", Scopes: []string{posts.BatchPermission}})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, h, "POST", "/api/posts/batch/visibility", narrow.Token, fmt.Sprintf(`{"post_ids":[%q],"published_at":%q}`, draft.ID, now.Format(time.RFC3339Nano)), 403)
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: posts.BatchPermission}}, nil); e != nil {
		t.Fatal(e)
	}
	token := pgIssue(t, f.issuer, f.actor).Token
	backendCall(t, h, "POST", "/api/posts/batch/visibility", token, fmt.Sprintf(`{"post_ids":[%q],"published_at":%q}`, draft.ID, now.Format(time.RFC3339Nano)), 403)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE id=$1 AND drafted_at IS NOT NULL AND published_at IS NULL`, draft.ID); n != 1 {
		t.Fatal("batch permission promoted to publication", n)
	}
}

type publishingSaved struct {
	Actor, Settings, Posting, Reply, Public, Deleted, Snapshot string
	Token, SessionID, SessionRow                               string
	Authority                                                  restartAuthoritySnapshot
}

func publishingSnapshot(t *testing.T, db *sql.DB, actor string) string {
	t.Helper()
	var s string
	e := db.QueryRow(`SELECT jsonb_build_object('settings',(SELECT to_jsonb(s) FROM owned_publishing_settings s WHERE account_id=$1),'posts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE u.account_id=$1),'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM owned_text_post_audit a WHERE actor_id=$1))::text`, actor).Scan(&s)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPGPublishingControlsSavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	if phase == "before" {
		f := newPostSchedulingFixture(t)
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f = postSchedulingAuthority(t, f)
		h := publishingApp(t, f)
		reply := publishingExtra(t, f, f.actor)
		s := publishingPatch(t, h, f.token, accounts.PublishingSettingsPatch{DefaultPostingPublisherID: &f.pub.ID, DefaultReplyPublisherID: &reply.ID})
		p := batchPost(t, h, f.token, f.pub.ID, true)
		deleted := batchPost(t, h, f.token, reply.ID, false)
		public := posts.Public
		backendCall(t, h, "POST", "/api/posts/batch/visibility", f.token, batchBody([]string{p.ID}, &public), 204)
		backendCall(t, h, "POST", "/api/posts/batch/delete", f.token, fmt.Sprintf(`{"post_ids":[%q]}`, deleted.ID), 204)
		proof, e := f.verifier.Authenticate(context.Background(), f.token)
		if e != nil {
			t.Fatal(e)
		}
		var sessionRow string
		if e = f.db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, proof.Session.ID).Scan(&sessionRow); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, "publishing-controls.json", publishingSaved{Actor: f.actor, Settings: s.ID, Posting: f.pub.ID, Reply: reply.ID, Public: p.ID, Deleted: deleted.ID, Snapshot: publishingSnapshot(t, db, f.actor), Token: f.token, SessionID: proof.Session.ID, SessionRow: sessionRow, Authority: saveAuthoritySnapshot(t, f.config)})
		return
	}
	var saved publishingSaved
	loadRestartFixture(t, "publishing-controls.json", &saved)
	if got := publishingSnapshot(t, db, saved.Actor); got != saved.Snapshot {
		t.Fatal("settings, lifecycle or audit changed across restart")
	}
	var id, posting, reply string
	if e := db.QueryRow(`SELECT id::text,default_publisher_id::text,default_reply_publisher_id::text FROM owned_publishing_settings WHERE account_id=$1`, saved.Actor).Scan(&id, &posting, &reply); e != nil || id != saved.Settings || posting != saved.Posting || reply != saved.Reply {
		t.Fatal(id, posting, reply, e)
	}
	// Reopen against the saved rows with the original credential and public
	// verifier only. No fixture, issuer, refresh, or private signer is created.
	h := publishingApp(t, postMediaFixture{mediaFixture: mediaFixture{db: db, config: restoreAuthoritySnapshot(t, saved.Authority)}})
	backendCall(t, h, "GET", "/api/posts/"+saved.Public, "", "", 200)
	backendCall(t, h, "GET", "/api/posts/"+saved.Deleted, "", "", 404)
	s := backendDecode[accounts.PublishingSettings](t, backendCall(t, h, "GET", "/api/account/publishing", saved.Token, "", 200))
	if s.ID != saved.Settings || s.DefaultReplyPublisherID == nil || *s.DefaultReplyPublisherID != saved.Reply {
		t.Fatal(s)
	}
	var sessionRow string
	if e := db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, saved.SessionID).Scan(&sessionRow); e != nil || sessionRow != saved.SessionRow {
		t.Fatal("original session changed after read", e)
	}
	if got := publishingSnapshot(t, db, saved.Actor); got != saved.Snapshot {
		t.Fatal("restart reads changed original state")
	}
}
