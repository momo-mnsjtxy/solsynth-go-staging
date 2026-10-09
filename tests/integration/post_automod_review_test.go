// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/moderationpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"sync/atomic"
	"testing"
	"time"
)

func requireNoAutomodEvidence(t *testing.T, raw []byte, privateText ...string) {
	t.Helper()
	var v any
	if e := json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	var check func(any)
	check = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "automod" || k == "rules_fingerprint" || k == "matched_text" {
					t.Fatalf("automatic-rule evidence field %q escaped its projection", k)
				}
				check(child)
			}
		case []any:
			for _, child := range x {
				check(child)
			}
		}
	}
	check(v)
	for _, secret := range privateText {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private rule or review text escaped its projection")
		}
	}
}

func TestPGPostAutomodRealmAndNestedEvidenceIsolation(t *testing.T) {
	f := automodFixture(t, true)
	body := "synthetic-private-rule-" + authorityUUID(t)
	rule := automodRule(t, f, a.Hide, body)
	p, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: &body, RealmID: &f.public.ID})
	if e != nil || p.Automod == nil || !p.Automod.Hidden || len(p.Automod.Matches) != 0 {
		t.Fatal(p, e)
	}
	reason := "Synthetic confidential global review " + authorityUUID(t)
	request := c.AutomodReview{ExpectedRevision: p.Automod.Revision, Action: "dismiss", Reason: reason}
	if _, e = f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, p.ID, c.Command{Action: "automod_review", Automod: &request}); e != nil {
		t.Fatal(e)
	}
	other, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: &body, RealmID: &f.private.ID})
	if e != nil {
		t.Fatal(e)
	}
	forward, e := f.posts.Create(f.ctx, f.actor, f.individual.ID, posts.Patch{Content: postString("Synthetic forwarding projection"), ForwardedPostID: &p.ID})
	if e != nil || forward.ForwardedPost == nil {
		t.Fatal(forward, e)
	}
	query(t, f.db, `UPDATE owned_realms SET boost_level=2 WHERE id=$1`, f.public.ID)
	if _, e = f.social.ChangeMember(f.ctx, f.actor, f.public.ID, social.MemberChange{Action: social.ChangeRole, AccountID: f.other, Role: social.Moderator}); e != nil {
		t.Fatal(e)
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, PostAutomodReview: true})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{p.ID, forward.ID} {
		w := backendCall(t, app, "GET", "/api/posts/"+id, f.otherToken, "", 200)
		requireNoAutomodEvidence(t, w.Body.Bytes(), rule.Name, reason)
		if id == forward.ID {
			v := backendDecode[posts.Post](t, w)
			if v.ForwardedPost == nil || v.ForwardedPost.ID != p.ID {
				t.Fatal("nested privacy assertion did not exercise a visible parent")
			}
		}
	}
	denied := backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.otherToken, "", 403)
	requireNoAutomodEvidence(t, denied.Body.Bytes(), rule.Name, reason)
	denied = backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/automod-review", f.otherToken, request, 403)
	requireNoAutomodEvidence(t, denied.Body.Bytes(), rule.Name, reason)
	denied = backendJSONCall(t, app, "POST", "/api/posts/"+other.ID+"/realm/moderate", f.otherToken, map[string]any{"reason": "Synthetic scope denial"}, 403)
	requireNoAutomodEvidence(t, denied.Body.Bytes(), rule.Name, reason)
	allowed := backendJSONCall(t, app, "POST", "/api/posts/"+p.ID+"/realm/moderate", f.otherToken, map[string]any{"reason": "Synthetic scoped removal"}, 200)
	requireNoAutomodEvidence(t, allowed.Body.Bytes(), rule.Name, reason)
	var result struct {
		Post c.Post `json:"post"`
	}
	if e = json.Unmarshal(allowed.Body.Bytes(), &result); e != nil || result.Post.Visibility != posts.Private || result.Post.RealmID != nil {
		t.Fatal(result, e)
	}
}

func TestPGPostAutomodManualRemovalIgnoresMalformedEvidence(t *testing.T) {
	f := automodFixture(t, false)
	p := f.post(t, f.individual, "Synthetic removable post", posts.Public)
	query(t, f.db, `UPDATE owned_text_posts SET automod_decision='{"revision":"synthetic invalid historical revision"}'::jsonb WHERE id=$1`, p.ID)
	for _, action := range []string{"lock", "unlock", "delete"} {
		v, e := f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, p.ID, c.Command{Action: action})
		if e != nil {
			t.Fatalf("automatic evidence prevented manual %s: %v", action, e)
		}
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		requireNoAutomodEvidence(t, raw)
	}
	var deleted bool
	var events int
	if e := f.db.QueryRow(`SELECT deleted_at IS NOT NULL,(SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1 AND action='delete') FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&deleted, &events); e != nil || !deleted || events != 1 {
		t.Fatal(deleted, events, e)
	}
}

func TestPGPostAutomodReviewReplayRevalidatesProof(t *testing.T) {
	f := automodFixture(t, true)
	body := "synthetic-replay-proof-" + authorityUUID(t)
	automodRule(t, f, a.Hide, body)
	p := f.post(t, f.individual, body, posts.Public)
	cmd := c.Command{Action: "automod_review", Automod: &c.AutomodReview{ExpectedRevision: p.Automod.Revision, Action: "dismiss", Reason: "Synthetic reviewed dismissal"}}
	if _, e := f.admin.Mutate(f.outsideCtx, f.outside, c.PostKind, p.ID, cmd); e != nil {
		t.Fatal(e)
	}
	// The same proof is valid through preflight, account locks and generation
	// locking, then expires before the replay can return administrative evidence.
	cfg := f.config
	now := cfg.Now()
	var checks atomic.Int32
	cfg.Now = func() time.Time {
		if checks.Add(1) >= 5 {
			return now.Add(24 * time.Hour)
		}
		return now
	}
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), f.outsideToken)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	admin, e := moderationpg.New(f.db, guard, moderationpg.Config{AutomodReview: true})
	if e != nil {
		t.Fatal(e)
	}
	checks.Store(0)
	if _, e = admin.Mutate(guard.Bind(context.Background(), proof), f.outside, c.PostKind, p.ID, cmd); !errors.Is(e, authority.ErrExpired) {
		t.Fatal("expired proof returned successful review replay", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_post_automod_events WHERE post_id=$1`, p.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("replay changed saved decisions", count, e)
	}
}
