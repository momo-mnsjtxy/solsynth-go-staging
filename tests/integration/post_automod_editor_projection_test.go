// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/moderationpg"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPGPostAutomodEditorSummaryAndExplicitEvidenceAuthority(t *testing.T) {
	f := automodFixture(t, true)
	body := "editor-summary-match-" + authorityUUID(t)
	rule := automodRule(t, f, a.Hide, body)
	t.Cleanup(func() {
		query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.outside)
		if e := authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Error(e)
		}
	})
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, PostAutomodReview: true})
	if e != nil {
		t.Fatal(e)
	}
	check := func(raw []byte) {
		t.Helper()
		requireEditorAutomodSummary(t, raw, rule.ID, rule.Name, "private-review-reason", f.outside)
	}
	created := backendJSONCall(t, app, "POST", "/api/posts?pub="+f.individual.Name, f.token, map[string]any{"content": body}, 200)
	check(created.Body.Bytes())
	p := backendDecode[posts.Post](t, created)
	if p.Automod == nil || !p.Automod.Hidden {
		t.Fatal("outcome missing")
	}
	review := c.AutomodReview{ExpectedRevision: p.Automod.Revision, Action: "dismiss", Reason: "private-review-reason"}
	full := backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/automod-review", f.outsideToken, review, 200)
	if !strings.Contains(full.Body.String(), rule.ID) || !strings.Contains(full.Body.String(), review.Reason) {
		t.Fatal("authorized global evidence missing")
	}
	var canonical, event string
	if e = f.db.QueryRow(`SELECT automod_decision::text,(SELECT decision::text FROM owned_post_automod_events WHERE post_id=$1 ORDER BY revision DESC LIMIT 1) FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&canonical, &event); e != nil {
		t.Fatal(e)
	}
	for _, stored := range []string{canonical, event} {
		for _, secret := range []string{rule.ID, rule.Name, review.Reason, f.outside} {
			if !strings.Contains(stored, secret) {
				t.Fatal("canonical decision or durable event lost private evidence")
			}
		}
	}
	check(backendCall(t, app, "GET", "/api/posts/"+p.ID, f.token, "", 200).Body.Bytes())
	listed, total, e := f.posts.List(f.ctx, f.actor, posts.ListOptions{Publisher: f.individual.Name, Take: 20})
	if e != nil || total != 1 || len(listed) != 1 {
		t.Fatal("editor list fixture", total, e)
	}
	raw, e := json.Marshal(listed)
	if e != nil {
		t.Fatal(e)
	}
	check(raw)
	var unchanged string
	if e = f.db.QueryRow(`SELECT automod_decision::text FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&unchanged); e != nil || unchanged != canonical {
		t.Fatal("editor read overwrote canonical evidence", e)
	}

	page, e := f.timeline.Feed(f.ctx, f.actor, timeline.Options{Publisher: f.individual.Name, Take: 20, Mode: sphere.Latest})
	if e != nil || len(page.Items) != 1 || page.Items[0].Data == nil || page.Items[0].Data.ID != p.ID {
		t.Fatal("timeline event fixture", e)
	}
	raw, e = json.Marshal(page)
	if e != nil {
		t.Fatal(e)
	}
	requireNoAutomodEvidence(t, raw, rule.ID, rule.Name, review.Reason)
	var notices int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_content_moderation_notifications WHERE post_id=$1`, p.ID).Scan(&notices); e != nil || notices != 0 {
		t.Fatal("automod review created an ordinary moderation notification", notices, e)
	}
	check(backendCall(t, app, "GET", "/api/posts?pub="+f.individual.Name, f.token, "", 200).Body.Bytes())
	forward := backendJSONCall(t, app, "POST", "/api/posts?pub="+f.individual.Name, f.token, map[string]any{"content": "Synthetic nested editor view", "forwarded_post_id": p.ID}, 200)
	check(forward.Body.Bytes())
	forwarded := backendDecode[posts.Post](t, forward)
	if forwarded.ForwardedPost == nil || forwarded.ForwardedPost.ID != p.ID {
		t.Fatal("nested projection not exercised")
	}

	check(backendJSONCall(t, app, "PATCH", "/api/posts/"+p.ID, f.token, map[string]any{"content": body + " edit"}, 200).Body.Bytes())
	check(backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/automod-review", f.outsideToken, review, 409).Body.Bytes())
	if e = f.db.QueryRow(`SELECT automod_decision::text FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&canonical); e != nil {
		t.Fatal(e)
	}
	disabled := false
	if _, e = f.ext.UpdateRule(f.outsideCtx, f.outside, rule.ID, a.Patch{IsEnabled: &disabled}); e != nil {
		t.Fatal(e)
	}
	stale := backendCall(t, app, "GET", "/api/posts/"+p.ID, f.token, "", 200)
	check(stale.Body.Bytes())
	if summary := backendDecode[posts.Post](t, stale).Automod; summary == nil || !summary.Stale || !summary.Hidden {
		t.Fatal("editor freshness summary lost historical outcome")
	}
	if e = f.db.QueryRow(`SELECT automod_decision::text FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&unchanged); e != nil || unchanged != canonical {
		t.Fatal("freshness projection overwrote canonical evidence", e)
	}

	if e = authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "*"}, {Pattern: "automod.rules.manage", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	restricted := backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.outsideToken, "", 200)
	check(restricted.Body.Bytes())
	check(backendCall(t, app, "GET", "/api/admin/posts?query="+body, f.outsideToken, "", 200).Body.Bytes())

	denied := backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/automod-review", f.outsideToken, review, 403)
	check(denied.Body.Bytes())
	if e = authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "admin.*"}}, nil); e != nil {
		t.Fatal(e)
	}
	check(backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.outsideToken, "", 200).Body.Bytes())
	check(backendJSONCall(t, app, "POST", "/api/admin/posts/"+p.ID+"/automod-review", f.outsideToken, review, 403).Body.Bytes())
	if e = authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "posts.read"}, {Pattern: "posts.moderate"}, {Pattern: "automod.rules.manage"}}, nil); e != nil {
		t.Fatal(e)
	}
	restored := backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.outsideToken, "", 200)
	if !strings.Contains(restored.Body.String(), rule.ID) {
		t.Fatal("explicit current evidence grant did not restore access")
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1`, f.outside)
	check(backendCall(t, app, "GET", "/api/admin/posts/"+p.ID, f.outsideToken, "", 403).Body.Bytes())
}

// Inspect every embedded decision, including arrays and response envelopes.
func requireEditorAutomodSummary(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("ordinary audience received global rule or reviewer evidence")
		}
	}
	var value any
	if e := json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if key == "automod" && child != nil {
					b, e := json.Marshal(child)
					if e != nil {
						t.Fatal(e)
					}
					var d posts.AutomodDecision
					if e = json.Unmarshal(b, &d); e != nil {
						t.Fatal(e)
					}
					if len(d.Matches) != 0 || d.Review != nil || d.Generation != 0 || d.ContentHash != "" || d.RulesFingerprint != "" {
						t.Fatal("unsanitized nested editor decision")
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
}

func TestPGPostAutomodEvidenceGrantExpiryAtResponse(t *testing.T) {
	for _, operation := range []string{"get", "list", "inspection"} {
		t.Run(operation, func(t *testing.T) {
			f := automodFixture(t, true)
			body := "expiry-evidence-" + authorityUUID(t)
			automodRule(t, f, a.Hide, body)
			p := f.post(t, f.individual, body, posts.Public)
			f.post(t, f.individual, body+" second", posts.Public)
			t.Cleanup(func() {
				if e := authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Error(e)
				}
			})
			now := f.config.Now()
			expires := now.Add(time.Second)
			if e := authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "posts.read"}, {Pattern: "automod.rules.manage", ExpiresAt: &expires}}, nil); e != nil {
				t.Fatal(e)
			}
			cfg := f.config
			var checks atomic.Int32
			at := int32(5) // Final Get check; List has already loaded one full decision.
			if operation == "inspection" {
				at = 4
			}
			cfg.Now = func() time.Time {
				if checks.Add(1) >= at {
					return expires.Add(time.Second)
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
			ctx := guard.Bind(context.Background(), proof)
			checks.Store(0)
			var value any
			switch operation {
			case "get":
				value, e = admin.Get(ctx, f.outside, c.PostKind, p.ID)
			case "list":
				value, _, e = admin.List(ctx, f.outside, c.PostKind, c.Query{PublisherID: f.individual.ID, Take: 20})
			case "inspection":
				tx, err := f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				value, e = admin.InspectionInTransaction(ctx, tx, f.outside, c.PostKind, p.ID)
			}
			if !errors.Is(e, authority.ErrForbidden) || value != nil {
				t.Fatal("expired rule-management grant returned administrative evidence", e)
			}
			if checks.Load() < at {
				t.Fatal("response boundary was not exercised")
			}
		})
	}
}

func TestPGPostAutomodEvidenceRevocationAfterAccountWait(t *testing.T) {
	for _, operation := range []string{"get", "list", "review"} {
		t.Run(operation, func(t *testing.T) {
			f := automodFixture(t, true)
			body := "revoked-evidence-" + authorityUUID(t)
			rule := automodRule(t, f, a.Hide, body)
			p := f.post(t, f.individual, body, posts.Public)
			t.Cleanup(func() {
				if e := authoritypg.New(f.db).ReplacePolicy(f.outsideCtx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
					t.Error(e)
				}
			})
			ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
			defer cancel()
			tx, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			var blocker int
			if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.outside); e != nil {
				t.Fatal(e)
			}
			type result struct {
				value any
				err   error
			}
			done := make(chan result, 1)
			go func() {
				var v any
				var err error
				switch operation {
				case "get":
					v, err = f.admin.Get(ctx, f.outside, c.PostKind, p.ID)
				case "list":
					v, _, err = f.admin.List(ctx, f.outside, c.PostKind, c.Query{PublisherID: f.individual.ID, Take: 20})
				case "review":
					v, err = f.admin.Mutate(ctx, f.outside, c.PostKind, p.ID, c.Command{Action: "automod_review", Automod: &c.AutomodReview{ExpectedRevision: p.Automod.Revision, Action: "dismiss", Reason: "Private review must never be committed"}})
				}
				done <- result{v, err}
			}()
			moderationWait(t, ctx, f.db, blocker, "owned_authority_accounts")
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern,deny) VALUES($1,'automod.rules.manage',true)`, f.outside); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if operation == "review" {
				if !errors.Is(got.err, authority.ErrForbidden) || got.value != nil {
					t.Fatal("revoked reviewer returned evidence or committed review", got.err)
				}
			} else if got.err != nil || got.value == nil {
				t.Fatal("post reader lost safe inspection", got.err)
			}
			raw, e := json.Marshal(got.value)
			if e != nil {
				t.Fatal(e)
			}
			requireNoAutomodEvidence(t, raw, rule.ID, rule.Name)
			var revision int64
			var events int
			if e = f.db.QueryRow(`SELECT automod_revision,(SELECT count(*) FROM owned_post_automod_events WHERE post_id=$1) FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&revision, &events); e != nil || revision != 1 || events != 1 {
				t.Fatal("denied evidence operation changed canonical decision", revision, events, e)
			}
		})
	}
}
