// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/taxonomypg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	federation "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strings"
	"testing"
	"time"
)

type moderationFixture struct {
	*realmPublishingFixture
	admin *moderationpg.Repository
}

func newModerationFixture(t *testing.T) *moderationFixture {
	t.Helper()
	f := newRealmPublishingFixture(t)
	query(t, f.db, moderationpg.Schema)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.outside)
	r, e := moderationpg.New(f.db, f.guard, moderationpg.Config{Now: f.config.Now, PostAssociations: f.timeline})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return &moderationFixture{f, r}
}
func (f *moderationFixture) mutate(t *testing.T, k c.Kind, id string, cmd c.Command) any {
	t.Helper()
	v, e := f.admin.Mutate(f.outsideCtx, f.outside, k, id, cmd)
	if e != nil {
		t.Fatal(cmd.Action, e)
	}
	return v
}
func TestPGContentModerationGlobalIsolationAndLifecycle(t *testing.T) {
	f := newModerationFixture(t)
	p := f.post(t, f.individual, "Synthetic administrative lifecycle", posts.Public)
	if _, e := f.admin.Get(f.ctx, f.actor, c.PostKind, p.ID); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("local publisher owner gained global inspection", e)
	}
	if _, e := f.admin.Mutate(f.ctx, f.actor, c.PostKind, p.ID, c.Command{Action: "lock"}); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("wildcard grant without global flag", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}, {Pattern: "posts.lock", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, p.ID, c.Command{Action: "lock"}); e == nil {
		t.Fatal("global flag bypassed explicit denial")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "lock"})
	if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Content: postString("should remain locked")}); !errors.Is(e, posts.ErrLocked) {
		t.Fatal(e)
	}
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, p.ID, c.Command{Action: "lock"}); !errors.Is(e, c.ErrAlreadyLocked) {
		t.Fatal(e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "unlock"})
	v := posts.Private
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "visibility", Visibility: &v})
	if _, e := f.posts.Get(f.outsideCtx, f.outside, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("global flag bypassed ordinary private read", e)
	}
	if _, e := f.admin.Get(f.outsideCtx, f.outside, c.PostKind, p.ID); e != nil {
		t.Fatal(e)
	}
	if _, total, e := f.admin.List(f.outsideCtx, f.outside, c.PostKind, c.Query{PublisherID: f.individual.ID, Visibility: &v, Take: 20}); e != nil || total != 1 {
		t.Fatal(total, e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "delete"})
	if _, e := f.posts.Get(f.ctx, f.actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestPGContentModerationShadowbanPropagation(t *testing.T) {
	f := newModerationFixture(t)
	body := "Synthetic private bytes behind a public post"
	file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("moderation-bytes", body, false), strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic moderated post"), Attachments: mediaIDs(file.ID)})
	if e != nil {
		t.Fatal(e)
	}
	q := surveyQuestions()
	s, e := f.surveys.Create(f.ctx, f.actor, f.individual.ID, surveys.Patch{Title: postString("Synthetic moderation choices"), Questions: &q, PostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.surveys.Transition(f.ctx, f.actor, s.ID, "publish"); e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "shadowban", Reason: postString("spam")})
	for _, viewer := range []struct {
		id  string
		ctx context.Context
	}{{"", context.Background()}, {f.other, f.otherCtx}, {f.outside, f.outsideCtx}} {
		actor := viewer.id
		if _, e = f.posts.Get(viewer.ctx, actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("shadowbanned post exposed", actor, e)
		}
		if _, e = f.surveys.Get(viewer.ctx, actor, s.ID); !errors.Is(e, surveys.ErrNotFound) {
			t.Fatal("survey intro exposed", actor, e)
		}
	}
	if _, stream, e := f.repo.Open(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrNotFound) || stream != nil {
		if stream != nil {
			stream.Close()
		}
		t.Fatal("post association exposed bytes", e)
	}
	if _, e = f.posts.Get(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal("publisher editor management read lost", e)
	}
	if _, _, e = f.interactions.React(f.otherCtx, f.other, p.ID, interactions.ReactionRequest{Symbol: "heart"}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("interaction reached hidden post", e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "unshadowban"})
	if _, e = f.posts.Get(f.otherCtx, f.other, p.ID); e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "shadowban", Reason: postString("other")})
	if _, e = f.accounts.LookupPublisherForViewer(f.otherCtx, f.other, f.individual.ID); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("publisher metadata exposed", e)
	}
	if _, e = f.posts.Get(f.otherCtx, f.other, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal(e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "unshadowban"})
	if _, e = f.posts.Get(f.otherCtx, f.other, p.ID); e != nil {
		t.Fatal(e)
	}
	// Realm membership is an additional gate even for a publisher editor.
	f.publisherEditor(t)
	org := f.post(t, f.privatePub, "Synthetic organization post", posts.Public)
	f.mutate(t, c.PublisherKind, f.privatePub.ID, c.Command{Action: "shadowban", Reason: postString("spam")})
	if _, e = f.posts.Get(f.otherCtx, f.other, org.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.private.ID, social.MemberChange{Action: social.Remove, AccountID: f.other}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.posts.Get(f.otherCtx, f.other, org.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("revoked realm editor read bypass", e)
	}
}
func TestPGContentModerationAtomicBatchAndAuditRollback(t *testing.T) {
	f := newModerationFixture(t)
	a := f.post(t, f.individual, "Synthetic batch one", posts.Public)
	b := f.post(t, f.individual, "Synthetic batch two", posts.Public)
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, "", c.Command{Action: "lock", IDs: []string{a.ID, authorityUUID(t)}}); !errors.Is(e, c.ErrNotFound) {
		t.Fatal(e)
	}
	current, e := f.posts.Get(f.ctx, f.actor, a.ID)
	if e != nil || current.LockedAt != nil {
		t.Fatal("partial missing-target batch", e)
	}
	query(t, f.db, `ALTER TABLE owned_content_moderation_audit ADD CONSTRAINT synthetic_moderation_rollback CHECK(action<>'lock') NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_content_moderation_audit DROP CONSTRAINT IF EXISTS synthetic_moderation_rollback`)
	})
	_, e = f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, "", c.Command{Action: "lock", IDs: []string{b.ID, a.ID}})
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "synthetic_moderation_rollback" {
		t.Fatal("wrong rollback cause", e)
	}
	for _, id := range []string{a.ID, b.ID} {
		v, e := f.posts.Get(f.ctx, f.actor, id)
		if e != nil || v.LockedAt != nil {
			t.Fatal("batch partially committed", id, e)
		}
	}
	query(t, f.db, `ALTER TABLE owned_content_moderation_audit DROP CONSTRAINT synthetic_moderation_rollback`)
	f.mutate(t, c.PostKind, "", c.Command{Action: "lock", IDs: []string{b.ID, a.ID}})
	f.mutate(t, c.PostKind, "", c.Command{Action: "unlock", IDs: []string{a.ID, b.ID}})
	var updates int
	if e = f.db.QueryRow(`SELECT count(*)FROM owned_text_post_audit WHERE post_id IN($1,$2)AND action='update'`, a.ID, b.ID).Scan(&updates); e != nil || updates != 4 {
		t.Fatal("rollback leaked canonical audit", updates, e)
	}
}
func TestPGContentModerationScopedRemovalAndNotification(t *testing.T) {
	f := newModerationFixture(t)
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic realm post"), RealmID: &f.public.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.admin.ModerateRealmPost(f.otherCtx, f.other, p.ID, postString("Synthetic reason")); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("normal realm member moderated", e)
	}
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.public.ID)
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.public.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: social.Moderator}); e != nil {
		t.Fatal(e)
	}
	v, e := f.admin.ModerateRealmPost(f.otherCtx, f.other, p.ID, postString("Synthetic reason"))
	if e != nil || v.Visibility != posts.Private || v.RealmID != nil {
		t.Fatal(v, e)
	}
	if _, e = f.posts.Update(f.otherCtx, f.other, p.ID, posts.Patch{Content: postString("moderator edit")}); e == nil {
		t.Fatal("realm moderation granted publisher editing")
	}
	if _, e = f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{RealmID: &f.public.ID}); !errors.Is(e, posts.ErrConflict) {
		t.Fatal("removed post relinked", e)
	}
	n := notificationpg.New(f.db, f.guard, "solian")
	if count, e := f.admin.ProcessNotifications(f.ctx, n, "solian", 10); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if count, e := f.admin.ProcessNotifications(f.ctx, n, "solian", 10); e != nil || count != 0 {
		t.Fatal("notification replay", count, e)
	}
	var external bool
	if e = f.db.QueryRow(`SELECT (command->>'External')::boolean FROM notification_v1.outbox WHERE account_id=$1 AND source_key LIKE 'content-moderation:%'`, f.actor).Scan(&external); e != nil || external {
		t.Fatal("not local-only", external, e)
	}
}
func TestPGContentModerationOwnerTransferAndDeletedRealm(t *testing.T) {
	f := newModerationFixture(t)
	zero := 0
	owner := 100
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.RealmKind, f.private.ID, c.Command{Action: "role", AccountID: f.actor, Role: &zero}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("last owner demoted", e)
	}
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.RealmKind, f.private.ID, c.Command{Action: "remove_member", AccountID: f.actor}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("last owner removed", e)
	}
	f.mutate(t, c.RealmKind, f.private.ID, c.Command{Action: "role", AccountID: f.other, Role: &owner})
	var actual string
	var owners int
	if e := f.db.QueryRow(`SELECT account_id::text FROM owned_realms WHERE id=$1`, f.private.ID).Scan(&actual); e != nil || actual != f.other {
		t.Fatal(actual, e)
	}
	if e := f.db.QueryRow(`SELECT count(*)FROM owned_realm_members WHERE realm_id=$1 AND role=100 AND leave_at IS NULL AND joined_at IS NOT NULL`, f.private.ID).Scan(&owners); e != nil || owners != 1 {
		t.Fatal(owners, e)
	}
	if _, e := f.posts.Create(f.otherCtx, f.other, f.privatePub.ID, posts.Patch{Content: postString("realm ownership must not grant publisher editing")}); !errors.Is(e, accounts.ErrForbidden) {
		t.Fatal(e)
	}
	if _, total, e := f.admin.Members(f.outsideCtx, f.outside, f.private.ID, c.Query{Take: 20}); e != nil || total != 2 {
		t.Fatal(total, e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic retained deleted realm association"), RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.RealmKind, f.private.ID, c.Command{Action: "delete"})
	if _, e = f.posts.Get(f.ctx, f.actor, p.ID); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal("deleted realm publicized post", e)
	}
	var realm string
	if e = f.db.QueryRow(`SELECT realm_id::text FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&realm); e != nil || realm != f.private.ID {
		t.Fatal(realm, e)
	}
	if e = f.posts.Delete(f.ctx, f.actor, p.ID); e != nil {
		t.Fatal("authorized cleanup denied", e)
	}
}
func moderationWait(t *testing.T, ctx context.Context, db *sql.DB, blocker int, table string) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if e := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND position($2 in query)>0)`, blocker, table).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("moderation did not reach expected lock", table, ctx.Err())
		case <-tick.C:
		}
	}
}
func TestPGContentModerationCurrentAuthorityAndMembershipAfterWait(t *testing.T) {
	f := newModerationFixture(t)
	p := f.post(t, f.individual, "Synthetic queued moderation", posts.Public)
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.outside); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		_, e := f.admin.Mutate(ctx, f.outside, c.PostKind, p.ID, c.Command{Action: "lock"})
		result <- e
	}()
	moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1`, f.outside); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, c.ErrForbidden) {
		t.Fatal("stale global flag accepted", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.outside)
	tx, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, f.private.ID); e != nil {
		t.Fatal(e)
	}
	go func() {
		_, e := f.admin.Mutate(ctx, f.outside, c.RealmKind, f.private.ID, c.Command{Action: "update", Realm: &c.RealmPatch{AccountID: &f.other}})
		result <- e
	}()
	moderationWait(t, ctx, f.db, pid, "owned_realms")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET leave_at=$3,updated_at=$3 WHERE realm_id=$1 AND account_id=$2`, f.private.ID, f.other, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, c.ErrConflict) {
		t.Fatal("left member became owner", e)
	}
	var actual string
	if e = f.db.QueryRow(`SELECT account_id::text FROM owned_realms WHERE id=$1`, f.private.ID).Scan(&actual); e != nil || actual != f.actor {
		t.Fatal(actual, e)
	}
}
func TestPGContentModerationFederationWithdrawalRetry(t *testing.T) {
	x := newFederationFixture(t, false)
	for _, schema := range []string{socialpg.Schema, postpg.RealmSchema, moderationpg.Schema} {
		query(t, x.db, schema)
	}
	query(t, x.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, x.actor)
	r, e := moderationpg.New(x.db, x.guard, moderationpg.Config{Now: x.config.Clock})
	if e != nil {
		t.Fatal(e)
	}
	if e = x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	transport := &federationCapture{}
	if _, e = x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil {
		t.Fatal(e)
	}
	post := x.makePost(t, "Synthetic federation moderation payload", posts.Public, false)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e = x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Mutate(x.ctx, x.actor, c.PublisherKind, x.publisher, c.Command{Action: "shadowban", Reason: postString("spam")}); e != nil {
		t.Fatal(e)
	}
	if _, e = x.r.Actor(x.ctx, x.name); !errors.Is(e, federation.ErrNotFound) {
		t.Fatal("moderated federation actor exposed", e)
	}
	if _, e = x.r.Object(x.ctx, post.ID); !errors.Is(e, federation.ErrNotFound) {
		t.Fatal("moderated object exposed", e)
	}
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal("withdrawal intent missing", n, e)
	}
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 0 {
		t.Fatal("duplicate withdrawal", n, e)
	}
	before := len(transport.requests)
	transport.codes = []int{503, 202}
	if n, e := x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	// The content changes while hidden. A content-free Delete remains necessary.
	if _, e = x.post.Update(x.ctx, x.actor, post.ID, posts.Patch{Content: postString("Synthetic hidden replacement")}); e != nil {
		t.Fatal(e)
	}
	x.clock.Add(61)
	reopened, e := federationpg.New(database(t), x.config, x.keys, nil)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := reopened.ProcessDeliveries(x.ctx, transport, 20); e != nil || n != 1 {
		t.Fatal("withdrawal retry", n, e)
	}
	if len(transport.requests) != before+2 || !bytes.Equal(transport.requests[before], transport.requests[before+1]) {
		t.Fatal("withdrawal retry not stable")
	}
	var a federation.Activity
	if e = json.Unmarshal(transport.requests[before], &a); e != nil || a.Type != "Delete" || bytes.Contains(a.Object, []byte("Synthetic")) {
		t.Fatal("withdrawal included hidden content", a, e)
	}
	if _, e = r.Mutate(x.ctx, x.actor, c.PublisherKind, x.publisher, c.Command{Action: "update", Publisher: &c.PublisherPatch{Name: postString("synthetic-renamed-federated")}}); !errors.Is(e, c.ErrConflict) {
		t.Fatal("registered actor identity renamed", e)
	}
	if _, e = r.Mutate(x.ctx, x.actor, c.PublisherKind, x.publisher, c.Command{Action: "unshadowban"}); e != nil {
		t.Fatal(e)
	}
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e = r.Mutate(x.ctx, x.actor, c.PostKind, post.ID, c.Command{Action: "shadowban", Reason: postString("other")}); e != nil {
		t.Fatal(e)
	}
	before = len(transport.requests)
	if _, e = x.r.ProcessDeliveries(x.ctx, transport, 20); e != nil {
		t.Fatal(e)
	}
	if len(transport.requests) != before {
		t.Fatal("queued restored content escaped new post moderation")
	}
}
func TestPGContentModerationRestartPersistence(t *testing.T) {
	const marker = "Synthetic durable content moderation checkpoint"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var id, owner string
		var bans int
		if e := db.QueryRow(`SELECT p.id::text,u.account_id::text FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.content=$1`, marker).Scan(&id, &owner); e != nil {
			t.Fatal(e)
		}
		r := postpg.New(db).WithVisibility(socialpg.New(db))
		if _, e := r.Get(context.Background(), "", id); !errors.Is(e, posts.ErrNotFound) {
			t.Fatal("restart moderation lost", e)
		}
		if _, e := r.Get(context.Background(), owner, id); e != nil {
			t.Fatal(e)
		}
		if e := db.QueryRow(`SELECT count(*)FROM owned_content_moderation_audit WHERE target_id=$1 AND action='shadowban'`, id).Scan(&bans); e != nil || bans != 1 {
			t.Fatal(bans, e)
		}
		return
	}
	f := newModerationFixture(t)
	p := f.post(t, f.individual, marker, posts.Public)
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "shadowban", Reason: postString("other")})
}
func TestPGContentModerationConcurrentBatchesAndScopedRevocation(t *testing.T) {
	f := newModerationFixture(t)
	a := f.post(t, f.individual, "Synthetic concurrent batch A", posts.Public)
	b := f.post(t, f.publicPub, "Synthetic concurrent batch B", posts.Public)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.actor)
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	ctx2, cancel2 := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel2()
	tx, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, f.individual.ID); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 2)
	go func() {
		_, e := f.admin.Mutate(ctx, f.outside, c.PostKind, "", c.Command{Action: "lock", IDs: []string{a.ID, b.ID}})
		result <- e
	}()
	moderationWait(t, ctx, f.db, pid, "owned_publishers")
	go func() {
		_, e := f.admin.Mutate(ctx2, f.actor, c.PostKind, "", c.Command{Action: "lock", IDs: []string{b.ID, a.ID}})
		result <- e
	}()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e = <-result
		if e == nil {
			success++
		} else if errors.Is(e, c.ErrAlreadyLocked) {
			conflicts++
		} else {
			t.Fatal("opposite publisher batch lock failure", e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	for _, id := range []string{a.ID, b.ID} {
		v, e := f.posts.Get(f.ctx, f.actor, id)
		if e != nil || v.LockedAt == nil {
			t.Fatal(id, e)
		}
	}
	// Scope revocation wins after an observed realm lock wait, even though the
	// actor's named permission remains granted throughout.
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.public.ID)
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.public.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: social.Moderator}); e != nil {
		t.Fatal(e)
	}
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic scoped revoke"), RealmID: &f.public.ID})
	if e != nil {
		t.Fatal(e)
	}
	tx, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_realms WHERE id=$1 FOR UPDATE`, f.public.ID); e != nil {
		t.Fatal(e)
	}
	otherCtx, cancelOther := context.WithTimeout(f.otherCtx, 10*time.Second)
	defer cancelOther()
	go func() {
		_, e := f.admin.ModerateRealmPost(otherCtx, f.other, p.ID, postString("Synthetic revoked moderator"))
		result <- e
	}()
	moderationWait(t, ctx, f.db, pid, "owned_realms")
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realm_members SET role=0,updated_at=$3 WHERE realm_id=$1 AND account_id=$2`, f.public.ID, f.other, f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, c.ErrForbidden) {
		t.Fatal("revoked moderator detached post", e)
	}
	var realm string
	if e = f.db.QueryRow(`SELECT realm_id::text FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&realm); e != nil || realm != f.public.ID {
		t.Fatal(realm, e)
	}
}
func TestPGContentModerationPublicMetadataAndNestedReads(t *testing.T) {
	f := newModerationFixture(t)
	p := f.post(t, f.individual, "Synthetic nested moderation target", posts.Public)
	reply, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic visible reply"), RepliedPostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	f.mutate(t, c.PostKind, p.ID, c.Command{Action: "shadowban", Reason: postString("spam")})
	var projected bool
	if e = f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM owned_taxonomy.posts WHERE id=$1)`, p.ID).Scan(&projected); e != nil || projected {
		t.Fatal("public taxonomy view exposed moderated post", projected, e)
	}
	got, e := f.posts.Get(f.otherCtx, f.other, reply.ID)
	if e != nil || got.RepliedPost != nil || got.RepliedPostID != nil {
		t.Fatal("nested moderated relation exposed", got, e)
	}
	pack, e := f.stickers.CreatePack(f.ctx, f.actor, f.individual.ID, stickers.PackPatch{Name: postString("Synthetic moderation pack"), Prefix: postString("syntheticmoderation" + strings.ReplaceAll(f.actor, "-", ""))})
	if e != nil {
		t.Fatal(e)
	}
	collection, e := f.interactions.CreateCollection(f.ctx, f.actor, f.individual.Name, interactions.CollectionRequest{Slug: "synthetic-moderation", Name: postString("Synthetic moderation collection")})
	if e != nil {
		t.Fatal(e)
	}
	tags := taxonomypg.NewWithIndependentPolicy(f.db, 0)
	tagID := authorityUUID(t)
	tagSlug := "synthetic-moderation-" + f.actor
	query(t, f.db, `INSERT INTO owned_taxonomy.post_tags(id,slug,owner_publisher_id,is_protected,is_event,created_at,updated_at)VALUES($1,$2,$3,true,false,$4,$4)`, tagID, tagSlug, f.individual.ID, f.config.Now())
	appRepo := apppg.NewWithAuthority(f.db, f.guard)
	if _, e = appRepo.Developers(f.ctx, f.actor, f.individual.Name, true); e != nil {
		t.Fatal(e)
	}
	scope := apps.Scope{Actor: f.actor, Developer: f.individual.Name}
	projects, e := appRepo.Projects(f.ctx, scope, "", &apps.ProjectPatch{Slug: postString("synthetic-moderation"), Name: postString("Synthetic moderation project")}, "create")
	if e != nil || len(projects) != 1 {
		t.Fatal(projects, e)
	}
	scope.Project = projects[0].ID
	prod := 1
	appList, e := appRepo.Apps(f.ctx, scope, &apps.AppPatch{Slug: postString("synthetic-moderation-" + f.actor), Name: postString("Synthetic moderation app"), Status: &prod}, "create")
	if e != nil || len(appList) != 1 {
		t.Fatal(appList, e)
	}
	app := appList[0]
	if _, e = appRepo.PublicApp(f.otherCtx, app.Slug); e != nil {
		t.Fatal(e)
	}
	suggest, e := autocompletepg.New(f.db, f.guard, autocompletepg.Config{Stickers: f.stickers})
	if e != nil {
		t.Fatal(e)
	}
	before, e := suggest.Suggest(f.otherCtx, f.other, "", "@p/"+f.individual.Name)
	if e != nil || len(before) != 1 {
		t.Fatal(before, e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "shadowban", Reason: postString("other")})
	if _, e = f.stickers.GetPack(f.otherCtx, f.other, pack.ID); !errors.Is(e, stickers.ErrNotFound) {
		t.Fatal("pack metadata exposed", e)
	}
	if _, e = f.interactions.Collection(f.otherCtx, f.other, f.individual.Name, collection.Slug); !errors.Is(e, interactions.ErrCollectionNotFound) {
		t.Fatal("collection metadata exposed", e)
	}
	if list, total, e := f.interactions.Collections(f.otherCtx, f.other, f.individual.Name, interactions.Page{Take: 20}); e != nil || len(list) != 0 || total != 0 {
		t.Fatal(list, total, e)
	}
	if _, e = tags.Get(f.otherCtx, taxonomy.Tag, tagSlug, false); !errors.Is(e, taxonomy.ErrNotFound) {
		t.Fatal("protected tag owner exposed", e)
	}
	if _, e = appRepo.PublicApp(f.otherCtx, app.Slug); !errors.Is(e, apps.ErrNotFound) {
		t.Fatal("developer app metadata exposed", e)
	}
	if ds, e := appRepo.Developers(f.otherCtx, f.other, f.individual.Name, false); e != nil || len(ds) != 0 {
		t.Fatal("developer identity exposed", ds, e)
	}
	if list, e := suggest.Suggest(f.otherCtx, f.other, "", "@p/"+f.individual.Name); e != nil || len(list) != 0 {
		t.Fatal("publisher autocomplete exposed", list, e)
	}
	if _, _, e = f.accounts.ListMembersForViewer(f.otherCtx, f.other, f.individual.ID, 0, 20); !errors.Is(e, accounts.ErrNotFound) {
		t.Fatal("publisher member metadata exposed", e)
	}
	f.mutate(t, c.PublisherKind, f.individual.ID, c.Command{Action: "unshadowban"})
	if _, e = f.stickers.GetPack(f.otherCtx, f.other, pack.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = appRepo.PublicApp(f.otherCtx, app.Slug); e != nil {
		t.Fatal(e)
	}
}
