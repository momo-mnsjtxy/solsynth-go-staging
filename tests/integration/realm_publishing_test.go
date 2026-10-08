// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/stickerpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

type realmPublishingFixture struct {
	mediaFixture
	outside, outsideToken             string
	outsideCtx                        context.Context
	accounts                          *accountpg.Repository
	social                            *socialpg.Repository
	posts                             *postpg.Repository
	timeline                          *timelinepg.Repository
	interactions                      *interactionpg.Repository
	surveys                           *surveypg.Repository
	stickers                          *stickerpg.Repository
	private, public                   social.Realm
	privatePub, publicPub, individual accounts.Publisher
}

func newRealmPublishingFixture(t *testing.T) *realmPublishingFixture {
	t.Helper()
	f := &realmPublishingFixture{mediaFixture: newMediaFixture(t), outside: authorityUUID(t)}
	ctx := context.Background()
	if e := authoritypg.SeedSynthetic(ctx, f.db, f.outside, "synthetic_realm_outside_"+strings.ReplaceAll(f.outside, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	for _, actor := range []string{f.actor, f.other, f.outside} {
		if e := authoritypg.New(f.db).ReplacePolicy(ctx, actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,3,$2)`, actor, f.config.Now())
		query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,3,$2)`, actor, f.config.Now())
	}
	f.outsideToken = pgIssue(t, f.issuer, f.outside).Token
	proof, e := f.verifier.Authenticate(ctx, f.outsideToken)
	if e != nil {
		t.Fatal(e)
	}
	f.outsideCtx = f.guard.Bind(ctx, proof)
	f.social = socialpg.NewWithAuthority(f.db, f.guard)
	f.accounts = accountpg.NewWithAuthority(f.db, f.guard).WithRealms(f.social)
	for i, out := range []*social.Realm{&f.private, &f.public} {
		slug := fmt.Sprintf("synthetic-org-%s-%d", f.actor, i)
		*out, e = f.social.CreateRealm(f.ctx, f.actor, social.RealmPatch{Slug: &slug, Name: postString("Synthetic organization realm"), IsPublic: boolean(i == 1)})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.social.ChangeMember(f.ctx, f.actor, out.ID, social.MemberChange{Action: social.Invite, AccountID: f.other}); e != nil {
			t.Fatal(e)
		}
		if _, e = f.social.ChangeMember(f.otherCtx, f.other, out.ID, social.MemberChange{Action: social.Accept}); e != nil {
			t.Fatal(e)
		}
	}
	f.privatePub = f.createOrganization(t, f.ctx, f.actor, f.private, "private")
	f.publicPub = f.createOrganization(t, f.ctx, f.actor, f.public, "public")
	name := "synthetic-org-personal-" + f.actor
	f.individual, e = f.accounts.CreatePublisher(f.ctx, f.actor, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	f.interactions = interactionpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social)
	links := postmediapg.New(f.db, f.interactions).WithVisibility(f.social)
	f.timeline = timelinepg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social).WithAssociations(links)
	f.posts = postpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social).WithAssociations(f.timeline)
	f.repo.WithReadVisibility(links)
	f.surveys = surveypg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social).WithMedia(f.repo)
	f.stickers = stickerpg.NewWithAuthority(f.db, f.guard).WithVisibility(f.social).WithMedia(f.repo)
	return f
}
func (f *realmPublishingFixture) createOrganization(t *testing.T, ctx context.Context, actor string, realm social.Realm, suffix string) accounts.Publisher {
	t.Helper()
	name := "synthetic-org-" + suffix + "-" + actor
	p, e := f.accounts.CreateOrganizationPublisher(ctx, actor, realm.Slug, accounts.PublisherPatch{Name: &name, Nick: postString("Synthetic organization")})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *realmPublishingFixture) post(t *testing.T, publisher accounts.Publisher, body string, visibility int) posts.Post {
	t.Helper()
	p, e := f.posts.Create(f.ctx, f.actor, publisher.ID, posts.Patch{Content: &body, Visibility: &visibility})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *realmPublishingFixture) publisherEditor(t *testing.T) {
	t.Helper()
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.privatePub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.accounts.ChangeMember(f.otherCtx, f.other, f.privatePub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
}
func (f *realmPublishingFixture) accountHTTP(t *testing.T, ctx context.Context, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	(&accounts.Service{Repository: f.accounts, Authority: f.verifier}).Handler().ServeHTTP(w, req)
	return w
}

func TestPGRealmPublisherCreationRolesQuotaAndMetadata(t *testing.T) {
	f := newRealmPublishingFixture(t)
	if f.privatePub.Type != 1 || f.privatePub.RealmID == nil || *f.privatePub.RealmID != f.private.ID || f.privatePub.AccountID != nil {
		t.Fatal("canonical organization projection", f.privatePub)
	}
	for _, actor := range []struct {
		id    string
		ctx   context.Context
		token string
	}{{f.other, f.otherCtx, f.otherToken}, {f.outside, f.outsideCtx, f.outsideToken}} {
		w := f.accountHTTP(t, actor.ctx, actor.token, "POST", "/api/publishers/organization/"+f.private.Slug, `{"name":"synthetic-disallowed-org","nick":"Disallowed"}`)
		if w.Code != 403 {
			t.Fatal("nonmoderator creation", w.Code, w.Body.String())
		}
	}
	if _, e := f.accounts.CreateOrganizationPublisher(f.ctx, f.actor, f.private.Slug, accounts.PublisherPatch{Name: postString("synthetic-quota-org"), Nick: postString("Quota")}); !errors.Is(e, accounts.ErrQuota) {
		t.Fatal("organization ignored creator quota", e)
	}
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.private.ID)
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: social.Moderator}); e != nil {
		t.Fatal(e)
	}
	w := f.accountHTTP(t, f.otherCtx, f.otherToken, "POST", "/api/publishers/organization/"+f.private.Slug, `{"name":"synthetic-mod-`+f.other+`","nick":"Moderator organization"}`)
	if w.Code != 200 {
		t.Fatal("moderator organization route", w.Code, w.Body.String())
	}
	var modPub accounts.Publisher
	if e := json.Unmarshal(w.Body.Bytes(), &modPub); e != nil {
		t.Fatal(e)
	}
	if m, e := f.accounts.Membership(f.otherCtx, modPub.ID, f.other); e != nil || m.Role != accounts.Owner {
		t.Fatal("creator is not separate publisher owner", m, e)
	}
	if _, e := f.posts.Create(f.ctx, f.actor, modPub.ID, posts.Patch{Content: postString("realm owner has no publisher grant")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("implicit realm editor grant", e)
	}
	if _, e := f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Invite, AccountID: f.outside}); e != nil {
		t.Fatal(e)
	}
	for _, entry := range []struct {
		ctx   context.Context
		token string
		want  int
	}{{context.Background(), "", 404}, {f.outsideCtx, f.outsideToken, 404}, {f.otherCtx, f.otherToken, 200}} {
		for _, path := range []string{"/api/publishers/" + f.privatePub.Name, "/api/publishers/" + f.privatePub.Name + "/members"} {
			w = f.accountHTTP(t, entry.ctx, entry.token, "GET", path, "")
			if w.Code != entry.want {
				t.Fatal("realm metadata scope", path, w.Code, w.Body.String())
			}
		}
	}
	w = f.accountHTTP(t, context.Background(), "", "GET", "/api/publishers/of/"+f.actor, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), f.privatePub.ID) || !strings.Contains(w.Body.String(), f.publicPub.ID) {
		t.Fatal("public account publisher list", w.Code, w.Body.String())
	}
	w = f.accountHTTP(t, f.ctx, f.token, "GET", "/api/publishers", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), f.privatePub.ID) {
		t.Fatal("managed private publisher missing", w.Code, w.Body.String())
	}
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.privatePub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.outside, Role: accounts.Editor}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("outside realm publisher invitation", e)
	}
	if _, e := f.accounts.LookupPublisher(context.Background(), f.privatePub.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("unscoped private publisher metadata", e)
	}
	req := httptest.NewRequest("GET", "/api/publishers/"+f.privatePub.Name, nil)
	req.Header.Set("X-Realm-ID", f.private.ID)
	req.Header.Set("X-Realm-Role", "owner")
	w = httptest.NewRecorder()
	(&accounts.Service{Repository: f.accounts, Authority: f.verifier}).Handler().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("realm headers granted membership", w.Code, w.Body.String())
	}
	query(t, f.db, `ALTER TABLE owned_accounts_audit ADD CONSTRAINT realm_organization_failure CHECK(actor_id<>'`+f.other+`'::uuid OR action<>'publishers.create') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_accounts_audit DROP CONSTRAINT IF EXISTS realm_organization_failure`)
	})
	rollbackName := "synthetic-org-rollback-" + f.other
	_, e := f.accounts.CreateOrganizationPublisher(f.otherCtx, f.other, f.private.Slug, accounts.PublisherPatch{Name: &rollbackName, Nick: postString("Synthetic rollback")})
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "realm_organization_failure" {
		t.Fatal("organization audit rollback", e)
	}
	var remains int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_publishers WHERE name=$1`, rollbackName).Scan(&remains); e != nil || remains != 0 {
		t.Fatal("partial organization/owner creation", remains, e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	for _, pub := range []accounts.Publisher{f.privatePub, f.publicPub} {
		if _, e = f.accounts.LookupPublisherForViewer(f.otherCtx, f.other, pub.ID); !errors.Is(e, accounts.ErrNotFound) {
			t.Fatal("disabled realm owner metadata", e)
		}
	}
}

func TestPGRealmPublisherPostTimelineNestedPrivacy(t *testing.T) {
	f := newRealmPublishingFixture(t)
	pub := f.post(t, f.privatePub, "private realm public-labelled post", posts.Public)
	private := f.post(t, f.privatePub, "private organization post", posts.Private)
	unlisted := f.post(t, f.privatePub, "unlisted organization post", posts.Unlisted)
	draftAt := f.config.Now()
	draft, e := f.posts.Create(f.ctx, f.actor, f.privatePub.Name, posts.Patch{Content: postString("organization draft"), DraftedAt: &draftAt})
	if e != nil {
		t.Fatal(e)
	}
	public := f.post(t, f.publicPub, "public realm post", posts.Public)
	for _, actor := range []string{"", f.outside} {
		for _, p := range []posts.Post{pub, private, unlisted, draft} {
			if _, e := f.posts.Get(context.Background(), actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
				t.Fatal("private realm post exposed", actor, p.ID, e)
			}
		}
		if _, e := f.posts.Get(context.Background(), actor, public.ID); e != nil {
			t.Fatal("public realm post hidden", e)
		}
	}
	if _, e = f.posts.Get(f.otherCtx, f.other, pub.ID); e != nil {
		t.Fatal("realm member public-labelled post", e)
	}
	if _, e = f.posts.Get(f.otherCtx, f.other, private.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("realm membership revealed publisher-private post", e)
	}
	if _, e = f.posts.Create(f.otherCtx, f.other, f.privatePub.ID, posts.Patch{Content: postString("no publisher grant")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("realm member became editor", e)
	}
	f.publisherEditor(t)
	if _, e = f.posts.Update(f.otherCtx, f.other, pub.ID, posts.Patch{Title: postString("editor title")}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Publish(f.otherCtx, f.other, draft.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.privatePub.ID, posts.Patch{Content: postString("ambiguous friends"), Visibility: postInt(posts.Friends)}); !errors.Is(e, posts.ErrUnsupported) {
		t.Fatal("organization friend policy invented", e)
	}
	reply, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("independent public reply"), RepliedPostID: &pub.ID, ForwardedPostID: &pub.ID})
	if e != nil {
		t.Fatal(e)
	}
	got, e := f.posts.Get(context.Background(), "", reply.ID)
	if e != nil || got.RepliedPostID != nil || got.ForwardedPostID != nil || got.RepliedPost != nil || got.ForwardedPost != nil {
		t.Fatal("nested private realm exposure", got, e)
	}
	got, e = f.posts.Get(f.otherCtx, f.other, reply.ID)
	if e != nil || got.RepliedPost == nil || got.RepliedPost.ID != pub.ID {
		t.Fatal("visible nested relation missing", got, e)
	}
	for _, actor := range []string{"", f.outside} {
		page, e := f.timeline.Feed(context.Background(), actor, timeline.Options{Take: 100, Mode: sphere.Latest})
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range page.Items {
			if event.Data != nil && event.Data.PublisherID == f.privatePub.ID {
				t.Fatal("private realm feed exposure")
			}
		}
		if _, _, e = f.posts.List(context.Background(), actor, posts.ListOptions{Publisher: f.privatePub.Name, Take: 20}); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("private publisher list scope", e)
		}
	}
	if _, e = f.timeline.Subscribe(f.outsideCtx, f.outside, f.privatePub.ID); !errors.Is(e, timeline.ErrPublisherNotFound) {
		t.Fatal("outside private realm follow", e)
	}
	if _, e = f.timeline.Subscribe(f.otherCtx, f.other, f.privatePub.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.interactions.React(f.otherCtx, f.other, pub.ID, interactions.ReactionRequest{Symbol: "heart"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Get(f.otherCtx, f.other, pub.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("revoked realm member retained post access", e)
	}
	if _, e = f.posts.Update(f.otherCtx, f.other, pub.ID, posts.Patch{Content: postString("stale editor")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("stale realm editor", e)
	}
	list, total, e := f.timeline.Subscriptions(f.otherCtx, f.other, 0, 100)
	if e != nil || total != 0 || len(list) != 0 {
		t.Fatal("stale follow exposed publisher", list, total, e)
	}
	if _, e = f.timeline.Unsubscribe(f.otherCtx, f.other, f.privatePub.ID); e != nil {
		t.Fatal("private follow cleanup", e)
	}
	if e = f.social.DeleteRealm(f.ctx, f.actor, f.private.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Get(f.ctx, f.actor, pub.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted realm post survived", e)
	}
}

func TestPGRealmPublisherMediaSurveyStickerAndFederationPrivacy(t *testing.T) {
	f := newRealmPublishingFixture(t)
	body := "Synthetic private realm attachment"
	file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("realm-private-file", body, false), strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.privatePub.ID, posts.Patch{Content: postString("realm attachment"), Attachments: mediaIDs(file.ID)})
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []struct {
		id  string
		ctx context.Context
	}{{"", context.Background()}, {f.outside, f.outsideCtx}} {
		if _, stream, e := f.repo.Open(actor.ctx, actor.id, file.ID); !errors.Is(e, media.ErrNotFound) || stream != nil {
			if stream != nil {
				stream.Close()
			}
			t.Fatal("private realm attachment download", e)
		}
	}
	_, stream, e := f.repo.Open(f.otherCtx, f.other, file.ID)
	if e != nil {
		t.Fatal(e)
	}
	bytes, e := io.ReadAll(stream)
	stream.Close()
	if e != nil || string(bytes) != body {
		t.Fatal("realm member attachment bytes", e)
	}
	q := surveyQuestions()
	deadline := f.config.Now().Add(24 * time.Hour)
	s, e := f.surveys.Create(f.ctx, f.actor, f.privatePub.ID, surveys.Patch{Title: postString("Synthetic realm choices"), Questions: &q, EndedAt: &deadline})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.surveys.Transition(f.ctx, f.actor, s.ID, "publish")
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []struct {
		id  string
		ctx context.Context
	}{{"", context.Background()}, {f.outside, f.outsideCtx}} {
		if _, e = f.surveys.Get(actor.ctx, actor.id, s.ID); !errors.Is(e, surveys.ErrNotFound) {
			t.Fatal("realm survey intro leak", e)
		}
		want := surveys.ErrNotFound
		if actor.id == "" {
			want = surveys.ErrUnauthenticated
		}
		result, resultErr := f.surveys.Results(actor.ctx, actor.id, s.ID)
		if !errors.Is(resultErr, want) || result.Total != 0 || len(result.Questions) != 0 {
			t.Fatal("realm survey results leak", result, resultErr)
		}
	}
	if _, e = f.surveys.Answer(f.otherCtx, f.other, s.ID, surveyAnswers(s)); e != nil {
		t.Fatal("realm member survey response", e)
	}
	// Organization sticker creation remains unsupported. An isolated imported
	// pack verifies the same outer metadata gate, without third-party media.
	pack := authorityUUID(t)
	prefix := "syntheticrealmpack" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `INSERT INTO owned_sticker_packs(id,publisher_id,name,description,prefix,created_at,updated_at) VALUES($1,$2,'Private realm pack','Synthetic private intro',$3,$4,$4)`, pack, f.privatePub.ID, prefix, f.config.Now())
	if _, e = f.stickers.GetPack(context.Background(), "", pack); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("realm sticker intro leak", e)
	}
	if _, e = f.stickers.GetPack(f.otherCtx, f.other, pack); e != nil {
		t.Fatal("realm member sticker intro", e)
	}
	packs, total, e := f.stickers.List(context.Background(), "", stickers.Page{Publisher: f.privatePub.Name, Take: 20})
	if e != nil || total != 0 || len(packs) != 0 {
		t.Fatal("realm sticker enumeration", packs, total, e)
	}
	publicPost := f.post(t, f.publicPub, "public organization remains unfederated", posts.Public)
	// Federation shares the synthetic verifier clock; make both publication
	// times eligible so rejection proves publisher scope, not a future timestamp.
	query(t, f.db, `UPDATE owned_text_posts SET published_at=$3 WHERE id IN($1::uuid,$2::uuid)`, p.ID, publicPost.ID, f.config.Now())
	fed, e := federationpg.New(f.db, federation.Config{Origin: "https://realm-publishing.example.test", AllowedOrigins: []string{"https://remote.example.test"}, Now: f.config.Now, MaxAttempts: 3, DeliveryTimeout: time.Second}, federationKeys{}, f.guard)
	if e != nil {
		t.Fatal(e)
	}
	for _, pub := range []accounts.Publisher{f.privatePub, f.publicPub} {
		query(t, f.db, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at) VALUES($1,true,true,$2)`, pub.ID, f.config.Now())
		if _, e = fed.Actor(f.ctx, pub.Name); !errors.Is(e, federation.ErrNotFound) {
			t.Fatal("organization federation actor", e)
		}
		if _, e = fed.Outbox(f.ctx, pub.Name, 0, 20); !errors.Is(e, federation.ErrNotFound) {
			t.Fatal("organization federation outbox", e)
		}
	}
	for _, post := range []posts.Post{p, publicPost} {
		if _, e = fed.Object(f.ctx, post.ID); !errors.Is(e, federation.ErrNotFound) {
			t.Fatal("organization federation object", e)
		}
	}
	if _, e = fed.ReconcilePosts(f.ctx, 100); e != nil {
		t.Fatal(e)
	}
	var federated int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_federation_post_state WHERE post_id IN($1::uuid,$2::uuid)`, p.ID, publicPost.ID).Scan(&federated); e != nil || federated != 0 {
		t.Fatal("organization federation delivery state", federated, e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, stream, e = f.repo.Open(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) || stream != nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("revoked realm attachment access", e)
	}
	if _, e = f.surveys.Get(f.otherCtx, f.other, s.ID); !errors.Is(e, surveys.ErrNotFound) {
		t.Fatal("revoked survey intro", e)
	}
	if _, e = f.stickers.GetPack(f.otherCtx, f.other, pack); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("revoked sticker intro", e)
	}
	// The file owner's independent canonical access remains valid.
	if _, stream, e = f.repo.Open(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	} else {
		stream.Close()
	}
}

func realmPublishingWait(t *testing.T, ctx context.Context, db *sql.DB, pid int, table string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock' AND position($2 in query)>0)`, pid, table).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected realm publishing lock wait", table, ctx.Err())
		case <-ticker.C:
		}
	}
}
func realmPublishingBlock(t *testing.T, ctx context.Context, db *sql.DB, realm string) *sql.Tx {
	t.Helper()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, realm); e != nil {
		t.Fatal(e)
	}
	return tx
}

// Every concurrent transaction uses the exact pool bound to f.guard and the
// existing signed contexts. Separate BeginTx calls still reserve independent
// connections; accepting another pool would fail closed before the lock probe.
func realmPublishingProbe(f *realmPublishingFixture) (*postpg.Repository, *timelineLockProbe) {
	db := f.db
	v := socialpg.New(db)
	probe := &timelineLockProbe{Repository: timelinepg.New(db).WithVisibility(v).WithAssociations(interactionpg.New(db).WithVisibility(v)), entered: make(chan int, 1)}
	return postpg.NewWithAuthority(db, f.guard).WithVisibility(v).WithAssociations(probe), probe
}

func TestPGRealmPublisherLockWaitRechecks(t *testing.T) {
	f := newRealmPublishingFixture(t)
	f.publisherEditor(t)
	root := f.post(t, f.privatePub, "realm membership retained content", posts.Public)
	ctx, cancel := context.WithTimeout(f.otherCtx, 20*time.Second)
	defer cancel()
	w, probe := realmPublishingProbe(f)
	block := realmPublishingBlock(t, ctx, f.db, f.private.ID)
	done := make(chan error, 1)
	go func() {
		_, e := w.Update(ctx, f.other, root.ID, posts.Patch{Content: postString("revoked realm editor")})
		done <- e
	}()
	realmPublishingWait(t, ctx, f.db, timelineEntered(t, ctx, probe), "owned_realms")
	check, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = check.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE NOWAIT`, f.privatePub.ID)
	_ = check.Rollback()
	if e != nil {
		t.Fatal("publisher acquired before realm", e)
	}
	if _, e = block.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$3 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal("membership revocation not rechecked", e)
	}
	got, e := f.posts.Get(f.ctx, f.actor, root.ID)
	if e != nil || got.Content != "realm membership retained content" {
		t.Fatal("stale realm editor changed post", got, e)
	}
	// A public target realm becoming private while the reply waits is rechecked.
	target := f.post(t, f.publicPub, "target realm privacy wait", posts.Public)
	name := "synthetic-realm-outside-publisher-" + f.outside
	outsidePub, e := f.accounts.CreatePublisher(f.outsideCtx, f.outside, accounts.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	outsideCtx, stop := context.WithTimeout(f.outsideCtx, 20*time.Second)
	defer stop()
	block = realmPublishingBlock(t, outsideCtx, f.db, f.public.ID)
	go func() {
		_, e := w.Create(outsideCtx, f.outside, outsidePub.ID, posts.Patch{Content: postString("target privacy stale reply"), RepliedPostID: &target.ID})
		done <- e
	}()
	realmPublishingWait(t, outsideCtx, f.db, timelineEntered(t, outsideCtx, probe), "owned_realms")
	if _, e = block.ExecContext(outsideCtx, `UPDATE owned_realms SET is_public=false WHERE id=$1`, f.public.ID); e != nil {
		t.Fatal(e)
	}
	if e = block.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("target realm privacy not rechecked", e)
	}
	// Unexpected imported ancestry changes must not acquire a late realm lock.
	ownerCtx, stopOwner := context.WithTimeout(f.ctx, 20*time.Second)
	defer stopOwner()
	pubBlock := timelinePublisherBlock(t, ownerCtx, f.db, f.privatePub.ID)
	go func() {
		_, e := w.Update(ownerCtx, f.actor, root.ID, posts.Patch{Content: postString("changed realm ancestry")})
		done <- e
	}()
	timelinePublisherWait(t, ownerCtx, f.db, timelineEntered(t, ownerCtx, probe))
	if _, e = pubBlock.ExecContext(ownerCtx, `UPDATE owned_publishers SET realm_id=$2 WHERE id=$1`, f.privatePub.ID, f.public.ID); e != nil {
		t.Fatal(e)
	}
	if e = pubBlock.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, accounts.ErrConflict) {
		t.Fatal("realm ancestry change accepted", e)
	}
	var content string
	var count int
	e = f.db.QueryRow(`SELECT content,(SELECT count(*) FROM owned_text_posts WHERE publisher_id=$2) FROM owned_text_posts WHERE id=$1`, root.ID, outsidePub.ID).Scan(&content, &count)
	if e != nil || content != "realm membership retained content" || count != 0 {
		t.Fatal("failed scope rechecks left mutations", content, count, e)
	}
}

func TestPGRealmPublisherOppositeRepliesAndRollback(t *testing.T) {
	f := newRealmPublishingFixture(t)
	slug := "synthetic-opposite-realm-" + f.other
	second, e := f.social.CreateRealm(f.otherCtx, f.other, social.RealmPatch{Slug: &slug, Name: &slug, IsPublic: boolean(true)})
	if e != nil {
		t.Fatal(e)
	}
	secondPub := f.createOrganization(t, f.otherCtx, f.other, second, "opposite")
	a := f.post(t, f.publicPub, "realm opposite root A", posts.Public)
	b, e := f.posts.Create(f.otherCtx, f.other, secondPub.ID, posts.Patch{Content: postString("realm opposite root B")})
	if e != nil {
		t.Fatal(e)
	}
	ac, cancelA := context.WithTimeout(f.ctx, 20*time.Second)
	defer cancelA()
	bc, cancelB := context.WithTimeout(f.otherCtx, 20*time.Second)
	defer cancelB()
	aw, aprobe := realmPublishingProbe(f)
	bw, bprobe := realmPublishingProbe(f)
	low, high := f.public.ID, second.ID
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
		go func() {
			patch := posts.Patch{Content: postString("realm opposite reply A"), RepliedPostID: &b.ID}
			if failA {
				patch.Tags = strList("realm-opposite-rollback")
			}
			p, e := aw.Create(ac, f.actor, f.publicPub.ID, patch)
			adone <- result{p, e}
		}()
		go func() {
			p, e := bw.Create(bc, f.other, secondPub.Name, posts.Patch{Content: postString("realm opposite reply B"), RepliedPostID: &a.ID})
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
			_, e = check.ExecContext(ac, `SELECT id FROM owned_publishers WHERE id IN($1::uuid,$2::uuid) FOR UPDATE NOWAIT`, f.publicPub.ID, secondPub.ID)
		}
		_ = check.Rollback()
		if e != nil {
			t.Fatal("realm/publisher lock acquired before smallest realm", e)
		}
		if e = block.Commit(); e != nil {
			t.Fatal(e)
		}
		for i, done := range []chan result{adone, bdone} {
			var got result
			select {
			case got = <-done:
			case <-ac.Done():
				t.Fatal("opposite realm replies did not finish", ac.Err())
			}
			if i == 0 && failA {
				var pgerr *pgconn.PgError
				if !errors.As(got.err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "realm_opposite_failure" || got.post.ID != "" {
					t.Fatal("wrong rollback failure", got)
				}
				continue
			}
			parent := b.ID
			if i == 1 {
				parent = a.ID
			}
			if got.err != nil || got.post.RepliedPostID == nil || *got.post.RepliedPostID != parent {
				t.Fatal("opposite realm reply", got)
			}
		}
	}
	run(false)
	query(t, f.db, `ALTER TABLE owned_text_post_audit ADD CONSTRAINT realm_opposite_failure CHECK(actor_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_text_post_audit DROP CONSTRAINT IF EXISTS realm_opposite_failure`)
	})
	run(true)
	var postCount, relationCount, auditCount, relationAuditCount, tags int
	e = f.db.QueryRow(`SELECT
 (SELECT count(*) FROM owned_text_posts WHERE publisher_id IN($1::uuid,$2::uuid)),
 (SELECT count(*) FROM owned_post_relations l JOIN owned_text_posts p ON p.id=l.post_id WHERE p.publisher_id IN($1::uuid,$2::uuid)),
 (SELECT count(*) FROM owned_text_post_audit WHERE actor_id IN($3::uuid,$4::uuid)),
 (SELECT count(*) FROM owned_timeline_audit WHERE actor_id IN($3::uuid,$4::uuid)),
 (SELECT count(*) FROM owned_taxonomy.post_tags WHERE slug='realm-opposite-rollback')`, f.publicPub.ID, secondPub.ID, f.actor, f.other).Scan(&postCount, &relationCount, &auditCount, &relationAuditCount, &tags)
	if e != nil || postCount != 5 || relationCount != 3 || auditCount != 5 || relationAuditCount != 3 || tags != 0 {
		t.Fatal("realm concurrent rollback state", postCount, relationCount, auditCount, relationAuditCount, tags, e)
	}
}

func TestPGRealmPublisherRestartPersistence(t *testing.T) {
	const publisher = "synthetic-realm-publisher-restart"
	const slug = "realm-publishing-restart"
	const member = "d5100000-0000-4000-8000-000000004891"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		v := socialpg.New(db)
		ctx := context.Background()
		p := postpg.New(db).WithVisibility(v)
		if _, e := p.GetSlug(ctx, "", publisher, slug); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("restart anonymous realm exposure", e)
		}
		got, e := p.GetSlug(ctx, member, publisher, slug)
		if e != nil || got.Content != "Durable private realm post" || got.Publisher.Type != 1 || got.Publisher.RealmID == nil {
			t.Fatal("restart organization post", got, e)
		}
		if _, e = accountpg.New(db).WithRealms(v).LookupPublisherForViewer(ctx, member, publisher); e != nil {
			t.Fatal("restart publisher member visibility", e)
		}
		return
	}
	db := timelineDB(t)
	ctx := context.Background()
	owner, _ := postPublisher(t, db, 4890)
	actualMember := postAccount(t, db, 4891)
	if actualMember != member {
		t.Fatal("restart fixture selector")
	}
	for _, actor := range []string{owner, member} {
		if e := authoritypg.New(db).ReplacePolicy(ctx, actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	issuer, verifier, guard, token := interactionAuthority(t, db, owner)
	proof, e := verifier.Authenticate(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	bound := guard.Bind(ctx, proof)
	memberToken := pgIssue(t, issuer, member).Token
	memberProof, e := verifier.Authenticate(ctx, memberToken)
	if e != nil {
		t.Fatal(e)
	}
	memberCtx := guard.Bind(ctx, memberProof)
	query(t, db, `INSERT INTO owned_realm_quota(account_id,total,starts_at) VALUES($1,1,'2020-01-01')`, owner)
	v := socialpg.NewWithAuthority(db, guard)
	realm, e := v.CreateRealm(bound, owner, social.RealmPatch{Slug: postString("synthetic-realm-restart"), Name: postString("Synthetic restart realm")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.ChangeMember(bound, owner, realm.ID, social.MemberChange{Action: social.Invite, AccountID: member}); e != nil {
		t.Fatal(e)
	}
	if _, e = v.ChangeMember(memberCtx, member, realm.ID, social.MemberChange{Action: social.Accept}); e != nil {
		t.Fatal(e)
	}
	a := accountpg.NewWithAuthority(db, guard).WithRealms(v)
	pub, e := a.CreateOrganizationPublisher(bound, owner, realm.Slug, accounts.PublisherPatch{Name: postString(publisher), Nick: postString("Durable organization")})
	if e != nil {
		t.Fatal(e)
	}
	p := postpg.NewWithAuthority(db, guard).WithVisibility(v)
	if _, e = p.Create(bound, owner, pub.ID, posts.Patch{Content: postString("Durable private realm post"), Slug: postString(slug)}); e != nil {
		t.Fatal(e)
	}
}

func TestPGRealmPublisherUnsupportedPublicIntegrations(t *testing.T) {
	f := newRealmPublishingFixture(t)
	appRepo := apppg.NewWithAuthority(f.db, f.guard)
	if _, e := appRepo.Developers(f.ctx, f.actor, f.privatePub.Name, true); !errors.Is(e, apps.ErrForbidden) {
		t.Fatal("private organization developer enrollment", e)
	}
	developer, project, app := authorityUUID(t), authorityUUID(t), authorityUUID(t)
	appSlug := "synthetic-org-app-" + f.actor
	query(t, f.db, `INSERT INTO owned_developers(id,publisher_id,created_at) VALUES($1,$2,$3)`, developer, f.privatePub.ID, f.config.Now())
	query(t, f.db, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,created_at,updated_at) VALUES($1,$2,'synthetic-private','Synthetic private project',$3,$3)`, project, developer, f.config.Now())
	query(t, f.db, `INSERT INTO owned_custom_apps(id,project_id,slug,name,status,created_at,updated_at) VALUES($1,$2,$3,'Synthetic private app',1,$4,$4)`, app, project, appSlug, f.config.Now())
	if developers, e := appRepo.Developers(context.Background(), "", f.privatePub.Name, false); e != nil || len(developers) != 0 {
		t.Fatal("organization developer projection", developers, e)
	}
	if _, e := appRepo.PublicApp(context.Background(), appSlug); !errors.Is(e, apps.ErrNotFound) {
		t.Fatal("organization public app projection", e)
	}
	tags := taxonomypg.NewWithAuthority(f.db, 0, f.guard)
	tagSlug := "synthetic-org-tag-" + f.actor
	for _, admin := range []bool{false, true} {
		_, e := tags.Create(f.ctx, taxonomy.Record{ID: authorityUUID(t), Kind: taxonomy.Tag, Slug: tagSlug, OwnerPublisherID: &f.privatePub.ID, AuditActorID: f.actor, AuditAdmin: admin, CreatedAt: f.config.Now(), UpdatedAt: f.config.Now()})
		if !errors.Is(e, taxonomy.ErrForbidden) {
			t.Fatal("organization protected taxonomy ownership", admin, e)
		}
	}
	// Imported unsupported records must also be hidden from public projections.
	tag := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_taxonomy.post_tags(id,slug,owner_publisher_id,is_protected,is_event,created_at,updated_at) VALUES($1,$2,$3,false,false,$4,$4)`, tag, tagSlug, f.privatePub.ID, f.config.Now())
	if _, e := tags.Get(context.Background(), taxonomy.Tag, tagSlug, false); !errors.Is(e, taxonomy.ErrNotFound) {
		t.Fatal("organization taxonomy metadata", e)
	}
	page, e := tags.List(context.Background(), taxonomy.Query{Kind: taxonomy.Tag, Search: tagSlug, Take: 20})
	if e != nil || page.Total != 0 || len(page.Rows) != 0 {
		t.Fatal("organization taxonomy listing", page, e)
	}
	if q, e := tags.Quota(context.Background(), f.privatePub.ID); e != nil || len(q) != 0 {
		t.Fatal("organization taxonomy quota projection", q, e)
	}
	if _, e = f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("unsupported organization tag"), Tags: strList(tagSlug)}); !errors.Is(e, interactions.ErrTagUnavailable) {
		t.Fatal("organization tag indirect projection", e)
	}
}
