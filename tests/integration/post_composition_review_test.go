// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func TestPGPostCompositionCanonicalLivePrivacy(t *testing.T) {
	f := newRealmPublishingFixture(t)
	_, timeline := compositionPool(f.db)
	f.posts.WithAssociations(timeline)
	if _, e := f.accounts.ChangeMember(f.ctx, f.actor, f.publicPub.ID, accounts.MemberChange{Action: accounts.InviteMember, AccountID: f.other, Role: accounts.Editor}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.accounts.ChangeMember(f.otherCtx, f.other, f.publicPub.ID, accounts.MemberChange{Action: accounts.AcceptInvite}); e != nil {
		t.Fatal(e)
	}
	stream := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_livestreams(id,publisher_id,title,type,visibility,status,room_name,created_at,updated_at)VALUES($1,$2,'Private stream title',0,2,0,'private-room',clock_timestamp(),clock_timestamp())`, stream, f.publicPub.ID)
	refs := []posts.Embed{{Type: "live_stream", ID: stream}}
	p, e := f.posts.Create(f.ctx, f.actor, f.publicPub.ID, posts.Patch{Content: postString("Readable realm post"), Embeds: &refs})
	if e != nil {
		t.Fatal(e)
	}
	forward, e := f.posts.Create(f.ctx, f.actor, f.publicPub.ID, posts.Patch{Content: postString("Readable realm forward"), ForwardedPostID: &p.ID})
	if e != nil {
		t.Fatal(e)
	}
	visible, e := f.posts.Get(f.otherCtx, f.other, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, visible, "live_stream")
	if _, e = f.social.ChangeMember(f.otherCtx, f.other, f.public.ID, social.MemberChange{Action: social.Leave}); e != nil {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND revoked_at IS NULL AND role>=50`, f.publicPub.ID, f.other); n != 1 {
		t.Fatal("retained publisher role precondition", n)
	}
	for _, actor := range []string{f.other, ""} {
		for _, id := range []string{p.ID, forward.ID} {
			got, e := f.posts.Get(context.Background(), actor, id)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(got)
			if containsJSON(raw, stream) || containsJSON(raw, "Private stream title") || containsJSON(raw, "private-room") {
				t.Fatal("private stream leaked without current realm membership", string(raw))
			}
		}
	}
	// Even a public stream requires the canonical realm owner's current eligibility
	// and block policy. Neither is implied by public post visibility.
	query(t, f.db, `UPDATE owned_livestreams SET visibility=0 WHERE id=$1`, stream)
	visible, e = f.posts.Get(f.otherCtx, f.other, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, visible, "live_stream")
	if _, e = f.social.ChangeRelationship(f.ctx, f.actor, f.other, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	hidden, e := f.posts.Get(f.otherCtx, f.other, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, hidden)
	query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.actor)
	hidden, e = f.posts.Get(context.Background(), "", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	assertCompositionKinds(t, hidden)
}

func TestPGPostCompositionFinalWaitsRecheckReferences(t *testing.T) {
	for _, kind := range []string{"automod", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			f := newCompositionFixture(t)
			var p posts.Post
			if kind == "schedule" {
				p = scheduledPost(t, f.postMediaFixture, posts.Patch{})
			} else {
				p = compositionPost(t, f, []posts.Embed{})
				f.posts.WithAutomod(false)
			}
			before := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID)
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			table := "owned_automod_state"
			if kind == "automod" {
				_, e = blocker.ExecContext(ctx, `SELECT generation FROM owned_automod_state WHERE singleton FOR UPDATE`)
			} else {
				table = "owned_post_schedules"
				_, e = blocker.ExecContext(ctx, `SELECT id FROM owned_post_schedules WHERE id=$1 FOR UPDATE`, p.Schedule.ID)
			}
			if e != nil {
				t.Fatal(e)
			}
			var expiration time.Time
			if e = f.db.QueryRow(`UPDATE owned_surveys SET ended_at=clock_timestamp()+interval '5 seconds' WHERE id=$1 RETURNING ended_at`, f.refs[0].ID).Scan(&expiration); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.posts.Update(ctx, f.actor, p.ID, posts.Patch{Content: postString("Must roll back after final wait"), Embeds: &[]posts.Embed{f.refs[0]}})
				done <- e
			}()
			moderationWait(t, ctx, f.db, pid, table)
			var current bool
			if e = f.db.QueryRow(`SELECT clock_timestamp()<$1`, expiration).Scan(&current); e != nil || !current {
				t.Fatal("target expired before final wait", current, e)
			}
			waitPostScheduleClock(t, f.db, expiration)
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, posts.ErrNotFound) {
				t.Fatal("expired target committed after final wait", e)
			}
			got, e := f.posts.Get(f.ctx, f.actor, p.ID)
			if e != nil || got.Content != p.Content {
				t.Fatal(got, e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 0 {
				t.Fatal("reference rows escaped rollback", n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID); n != before {
				t.Fatal("post audit escaped rollback", n, before)
			}
			if kind == "schedule" {
				scheduleState(t, f.db, p.Schedule.ID, "pending")
			}
		})
	}
}

func TestPGPostCompositionProofScopeAndMediaWait(t *testing.T) {
	f := newCompositionFixture(t)
	app := backendApp(t, f.db, f.config)
	readOnly, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "synthetic composer read scope", Scopes: []string{"posts.read"}})
	if e != nil {
		t.Fatal(e)
	}
	before := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID)
	backendJSONCall(t, app, "POST", "/sphere/posts?pub="+f.pub.ID, readOnly.Token, map[string]any{"content": "Denied scope", "embeds": f.refs}, 403)
	if _, e = f.posts.Create(f.ctx, f.other, f.pub.ID, posts.Patch{Content: postString("Denied actor substitution"), Embeds: &f.refs}); e == nil {
		t.Fatal("mismatched original proof accepted")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.pub.ID); n != before {
		t.Fatal("denied scope created post", n, before)
	}
	file := privatePostFile(t, f.postMediaFixture, "composer-expiry", "Synthetic bytes")
	p := compositionPost(t, f, []posts.Embed{})
	for _, kind := range []string{"survey", "fund", "meet"} {
		t.Run(kind, func(t *testing.T) {
			var ref posts.Embed
			for _, x := range f.refs {
				if x.Type == kind {
					ref = x
				}
			}
			table, column := "owned_surveys", "ended_at"
			if kind == "fund" {
				table, column = "wallet_v1.funds", "expires_at"
			}
			if kind == "meet" {
				table, column = "owned_meets", "expires_at"
			}
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
			var expiration time.Time
			if kind == "fund" {
				// Fund terms are immutable. Seed a separate bounded target with its
				// original deadline; never weaken the canonical trigger for a test.
				original := ref.ID
				ref.ID = authorityUUID(t)
				e = f.db.QueryRow(`INSERT INTO wallet_v1.funds(id,creator_account_id,beneficiary_wallet_id,currency,total_amount,amount_of_splits,split_type,message,status,is_open,is_raising,target_amount,contribution_type,contribution_amount,expires_at,created_at,updated_at,fingerprint)
 SELECT $1,creator_account_id,beneficiary_wallet_id,currency,total_amount,amount_of_splits,split_type,message,0,is_open,is_raising,target_amount,contribution_type,contribution_amount,clock_timestamp()+interval '5 seconds',clock_timestamp(),clock_timestamp(),fingerprint FROM wallet_v1.funds WHERE id=$2 RETURNING expires_at`, ref.ID, original).Scan(&expiration)
			} else {
				e = f.db.QueryRow(fmt.Sprintf("UPDATE %s SET %s=clock_timestamp()+interval '5 seconds' WHERE id=$1 RETURNING %s", table, column, column), ref.ID).Scan(&expiration)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.posts.Update(ctx, f.actor, p.ID, posts.Patch{Content: postString("Must roll back expired composition"), Embeds: &[]posts.Embed{ref}, Attachments: mediaIDs(file.ID)})
				done <- e
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			var current bool
			if e := f.db.QueryRow(`SELECT clock_timestamp()<$1`, expiration).Scan(&current); e != nil || !current {
				t.Fatal("target expired before media wait", current, e)
			}
			waitPostScheduleClock(t, f.db, expiration)
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; !errors.Is(e, posts.ErrNotFound) {
				t.Fatal(e)
			}
			got, e := f.posts.Get(f.ctx, f.actor, p.ID)
			if e != nil || got.Content != p.Content || len(got.Attachments) != 0 {
				t.Fatal("media wait escaped rollback", got, e)
			}
		})
	}
	t.Run("original_proof_expiry", func(t *testing.T) {
		cfg := f.config
		cfg.AccessTTL = 5 * time.Second
		issuer, e := authority.New(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
		pair := pgIssue(t, issuer, f.actor)
		proof, e := verifier.Authenticate(context.Background(), pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 15*time.Second)
		defer cancel()
		raw, e := base64.RawURLEncoding.DecodeString(strings.Split(pair.Token, ".")[1])
		if e != nil {
			t.Fatal(e)
		}
		var claims struct {
			Exp int64 `json:"exp"`
		}
		if e = json.Unmarshal(raw, &claims); e != nil {
			t.Fatal(e)
		}
		expiration := time.Unix(claims.Exp, 0)
		repo := postpg.NewWithAuthority(f.db, guard).WithVisibility(socialpg.New(f.db)).WithAssociations(f.timeline)
		locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
		blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
		done := make(chan error, 1)
		go func() {
			_, e := repo.Update(ctx, f.actor, p.ID, posts.Patch{Content: postString("Must reject original expired proof"), Embeds: &[]posts.Embed{f.refs[4]}, Attachments: mediaIDs(file.ID)})
			done <- e
		}()
		waitAtomicBlocked(t, locks, pid, "owned_media_files")
		var current bool
		if e = f.db.QueryRow(`SELECT clock_timestamp()<$1`, expiration).Scan(&current); e != nil || !current {
			t.Fatal("proof expired before media wait", e)
		}
		waitPostScheduleClock(t, f.db, expiration)
		if e = blocker.Commit(); e != nil {
			t.Fatal(e)
		}
		if e = <-done; !errors.Is(e, authority.ErrExpired) {
			t.Fatal("expired proof committed", e)
		}
		got, e := f.posts.Get(f.ctx, f.actor, p.ID)
		if e != nil || got.Content != p.Content || len(got.Attachments) != 0 {
			t.Fatal(got, e)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_post_embeds WHERE post_id=$1`, p.ID); n != 0 {
			t.Fatal("proof rollback left references", n)
		}
	})

}
