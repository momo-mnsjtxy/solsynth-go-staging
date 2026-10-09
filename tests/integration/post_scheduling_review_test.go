// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// Observe one real deadline. Never advance the shared clock or other due jobs.
func waitPostScheduleClock(t *testing.T, db *sql.DB, at time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var passed bool
		if e := db.QueryRowContext(ctx, `SELECT clock_timestamp()>=$1`, at).Scan(&passed); e != nil {
			t.Fatal(e)
		}
		if passed {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("schedule deadline was not observed", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestPGPostSchedulePublicationGatesAfterMediaWait(t *testing.T) {
	for _, kind := range []string{"publisher_owner_disabled", "publisher_owner_punished", "realm_owner_punished"} {
		t.Run(kind, func(t *testing.T) {
			f := newPostSchedulingFixture(t)
			// The editor and file owner differ from the publisher's owner. A
			// publisher-owner change therefore does not need the editor's lock.
			owner := f.actor
			query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,clock_timestamp(),clock_timestamp(),clock_timestamp())`, authorityUUID(t), f.pub.ID, f.other)
			patch := posts.Patch{}
			if kind == "realm_owner_punished" {
				r := socialpg.NewWithAuthority(f.db, f.guard)
				query(t, f.db, `INSERT INTO owned_realm_quota(account_id,total,starts_at)VALUES($1,1,clock_timestamp())`, owner)
				realm, e := r.CreateRealm(f.ctx, owner, social.RealmPatch{Slug: postString("scheduled-" + owner), Name: postString("Scheduled realm")})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = r.ChangeMember(f.ctx, owner, realm.ID, social.MemberChange{Action: social.Invite, AccountID: f.other}); e != nil {
					t.Fatal(e)
				}
				if _, e = r.ChangeMember(f.otherCtx, f.other, realm.ID, social.MemberChange{Action: social.Join}); e != nil {
					t.Fatal(e)
				}
				f.pub, e = accountpg.NewForCompatibilityTests(f.db).CreatePublisher(context.Background(), f.other, accounts.PublisherPatch{Name: postString("scheduled-" + f.other), Nick: postString("Scheduled editor")})
				if e != nil {
					t.Fatal(e)
				}
				patch.RealmID = &realm.ID
			}
			f.actor, f.ctx = f.other, f.otherCtx
			file := privatePostFile(t, f, "owner-wait", "publication owner wait")
			patch.Attachments = mediaIDs(file.ID)
			p := scheduledPost(t, f, patch)
			scheduleDue(t, f.db, p)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file.ID)
			done := make(chan error, 1)
			go func() { _, e := f.posts.ProcessScheduled(ctx, 100); done <- e }()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if kind == "publisher_owner_disabled" {
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, owner)
			} else {
				query(t, f.db, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, owner)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			scheduleState(t, f.db, p.Schedule.ID, "blocked")
			scheduleHidden(t, f, p.ID)
			var published bool
			if e := f.db.QueryRow(`SELECT published_at IS NOT NULL FROM owned_text_posts WHERE id=$1`, p.ID).Scan(&published); e != nil || published {
				t.Fatal("stale owner gate committed publication", published, e)
			}
		})
	}
}

func TestPGPostScheduleExplicitPublicationUsesCurrentGates(t *testing.T) {
	for _, operation := range []string{"publish", "patch"} {
		for _, kind := range []string{"media", "forward_deleted", "forward_locked", "chain_draft", "post_shadowban", "publisher_shadowban", "repair_media", "fresh_intent"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				f := newPostSchedulingFixture(t)
				patch := posts.Patch{}
				var fileID, target string
				if kind == "media" || kind == "repair_media" {
					file := privatePostFile(t, f, "explicit", "publication explicit bytes")
					fileID = file.ID
					patch.Attachments = mediaIDs(file.ID)
				}
				if kind == "forward_deleted" || kind == "forward_locked" || kind == "chain_draft" {
					parent, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Content: postString("Publication target")})
					if e != nil {
						t.Fatal(e)
					}
					target = parent.ID
					if kind == "chain_draft" {
						patch.ChainedPostID = &target
					} else {
						patch.ForwardedPostID = &target
					}
				}
				p := scheduledPost(t, f, patch)
				switch kind {
				case "media", "repair_media":
					query(t, f.db, `UPDATE owned_media_files SET state='deleting' WHERE id=$1`, fileID)
				case "forward_deleted":
					if e := f.posts.Delete(f.ctx, f.actor, target); e != nil {
						t.Fatal(e)
					}
				case "forward_locked":
					query(t, f.db, `UPDATE owned_text_posts SET locked_at=clock_timestamp() WHERE id=$1`, target)
				case "chain_draft":
					at := time.Now().UTC()
					if _, e := f.posts.Update(f.ctx, f.actor, target, posts.Patch{DraftedAt: &at}); e != nil {
						t.Fatal(e)
					}
				case "post_shadowban":
					query(t, f.db, `UPDATE owned_text_posts SET shadowbanned_at=clock_timestamp() WHERE id=$1`, p.ID)
				case "publisher_shadowban":
					query(t, f.db, `UPDATE owned_publishers SET shadowbanned_at=clock_timestamp() WHERE id=$1`, f.pub.ID)
				case "fresh_intent":
					// A fresh editor request is independent of a stale schedule's
					// policy stamp, while still requiring all current gates.
					query(t, f.db, `UPDATE owned_post_schedules SET policy_stamp='{}' WHERE id=$1`, p.Schedule.ID)
				}
				if kind == "repair_media" && operation == "publish" {
					if _, e := f.posts.Update(f.ctx, f.actor, p.ID, posts.Patch{Attachments: mediaIDs()}); e != nil {
						t.Fatal(e)
					}
				}
				var before int
				if e := f.db.QueryRow(`SELECT count(*) FROM owned_text_post_audit WHERE post_id=$1`, p.ID).Scan(&before); e != nil {
					t.Fatal(e)
				}
				var published posts.Post
				var e error
				if operation == "publish" {
					published, e = f.posts.Publish(f.ctx, f.actor, p.ID)
				} else {
					at := time.Now().UTC()
					update := posts.Patch{PublishedAt: &at}
					if kind == "repair_media" {
						update.Attachments = mediaIDs()
					}
					published, e = f.posts.Update(f.ctx, f.actor, p.ID, update)
				}
				if kind == "repair_media" || kind == "fresh_intent" {
					if e != nil || published.DraftedAt != nil || published.PublishedAt == nil {
						t.Fatal("valid fresh publication rejected", published, e)
					}
					scheduleState(t, f.db, p.Schedule.ID, "cancelled")
					if _, e := f.posts.Get(context.Background(), "", p.ID); e != nil {
						t.Fatal(e)
					}
					return
				}
				if e == nil {
					t.Fatal("explicit publication bypassed gate", kind, published)
				}
				var failure *posts.Failure
				if !errors.As(e, &failure) || failure.Status >= 500 {
					t.Fatal("publication returned an unexpected infrastructure failure", e)
				}
				scheduleState(t, f.db, p.Schedule.ID, "pending")
				var draft bool
				var after int
				if e := f.db.QueryRow(`SELECT drafted_at IS NOT NULL AND published_at IS NULL,(SELECT count(*) FROM owned_text_post_audit WHERE post_id=p.id) FROM owned_text_posts p WHERE id=$1`, p.ID).Scan(&draft, &after); e != nil || !draft || after != before {
					t.Fatal("failed explicit publication changed canonical state/audit", draft, before, after, e)
				}
			})
		}
	}
}

func TestPGPostScheduleFinalScheduleWaitRechecksSession(t *testing.T) {
	f := newPostSchedulingFixture(t)
	p := scheduledPost(t, f, posts.Patch{})
	scheduleDue(t, f.db, p)
	var expiration time.Time
	if e := f.db.QueryRow(`UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '5 seconds' WHERE id=(SELECT session_id FROM owned_post_schedules WHERE id=$1) RETURNING expires_at`, p.Schedule.ID).Scan(&expiration); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
	blocker, pid := atomicBlocker(t, locks, "owned_post_schedules", fmt.Sprint(p.Schedule.ID))
	done := make(chan error, 1)
	go func() { _, e := f.posts.ProcessScheduled(ctx, 100); done <- e }()
	waitAtomicBlocked(t, locks, pid, "owned_post_schedules")
	var stillEligible bool
	if e := f.db.QueryRow(`SELECT clock_timestamp()<$1`, expiration).Scan(&stillEligible); e != nil || !stillEligible {
		t.Fatal("session expired before the final lock wait was established", stillEligible, e)
	}
	waitPostScheduleClock(t, f.db, expiration)
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	scheduleState(t, f.db, p.Schedule.ID, "blocked")
	var failure string
	if e := f.db.QueryRow(`SELECT failure FROM owned_post_schedules WHERE id=$1`, p.Schedule.ID).Scan(&failure); e != nil || failure != "authority_revoked" {
		t.Fatal("final session recheck did not reject expiry", failure, e)
	}
	scheduleHidden(t, f, p.ID)
}
