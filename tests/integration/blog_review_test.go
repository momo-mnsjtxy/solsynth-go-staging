// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/sphere/blogs"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/reader"
)

func pendingBlogDomain(t *testing.T, f blogFixture) (blogFixture, blogs.Domain) {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { cancel(); return false, context.Canceled })
	f = blogPool(t, f)
	if _, e := f.domains.Add(ctx, f.actor, f.pub.ID, "blog.example"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	rows, e := f.domains.List(f.ctx, f.actor, f.pub.ID)
	if e != nil || len(rows) != 1 || rows[0].Status != blogs.Pending {
		t.Fatal(rows, e)
	}
	return f, rows[0]
}

func TestPGBlogPreviewFencesDomainAndPolicyABA(t *testing.T) {
	for _, kind := range []string{"recheck", "recreate", "policy", "member", "owner_policy"} {
		t.Run(kind, func(t *testing.T) {
			f := newBlogFixture(t)
			if kind == "owner_policy" {
				query(t, f.db, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, f.pub.ID, f.other)
			}
			domain := verifiedBlogDomain(t, f)
			f.configBlog.Previewer = blogPreviewFunc(func(ctx context.Context, u string) (reader.Link, error) {
				ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				switch kind {
				case "recheck":
					if _, e := f.domains.Recheck(ctx, f.actor, f.pub.ID, domain.ID); e != nil {
						return reader.Link{}, e
					}
				case "recreate":
					if e := f.domains.Remove(ctx, f.actor, f.pub.ID, domain.ID); e != nil {
						return reader.Link{}, e
					}
					if _, e := f.domains.Add(ctx, f.actor, f.pub.ID, "blog.example"); e != nil {
						return reader.Link{}, e
					}
				case "policy":
					if _, e := f.db.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern,deny) VALUES($1::uuid,'posts.create.blog',true)`, f.actor); e != nil {
						return reader.Link{}, e
					}
					if _, e := f.db.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid AND pattern='posts.create.blog' AND deny`, f.actor); e != nil {
						return reader.Link{}, e
					}
				case "owner_policy":
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, f.other); e != nil {
						return reader.Link{}, e
					}
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, f.other); e != nil {
						return reader.Link{}, e
					}
				case "member":
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.pub.ID, f.actor); e != nil {
						return reader.Link{}, e
					}
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.pub.ID, f.actor); e != nil {
						return reader.Link{}, e
					}
				}
				return reader.Link{URL: u, Title: "Stale synthetic preview"}, nil
			})
			f = blogPool(t, f)
			_, e := f.posts.Create(f.ctx, f.actor, f.pub.ID, posts.Patch{Type: postInt(posts.Blog), Content: postString("https://blog.example/article")})
			if !errors.Is(e, posts.ErrConflict) {
				t.Fatal("stale preview accepted", kind, e)
			}
			var n int
			if e = f.db.QueryRow(`SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1::uuid`, f.pub.ID).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}

func TestPGBlogVerificationFencesPublisherIdentityABA(t *testing.T) {
	for _, kind := range []string{"owner", "name"} {
		t.Run(kind, func(t *testing.T) {
			f := newBlogFixture(t)
			f.configBlog.Verifier = blogVerifyFunc(func(ctx context.Context, _, _ string) (bool, error) {
				ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				if kind == "owner" {
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, f.pub.ID, f.other); e != nil {
						return false, e
					}
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, f.pub.ID, f.actor); e != nil {
						return false, e
					}
				} else {
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publishers SET name=$2::text WHERE id=$1::uuid`, f.pub.ID, f.pub.Name+"-changed"); e != nil {
						return false, e
					}
					if _, e := f.db.ExecContext(ctx, `UPDATE owned_publishers SET name=$2::text WHERE id=$1::uuid`, f.pub.ID, f.pub.Name); e != nil {
						return false, e
					}
				}
				return true, nil
			})
			f = blogPool(t, f)
			if _, e := f.domains.Add(f.ctx, f.actor, f.pub.ID, "blog.example"); !errors.Is(e, blogs.ErrConflict) {
				t.Fatal("old identity proof accepted", e)
			}
			rows, e := f.domains.List(f.ctx, f.actor, f.pub.ID)
			if e != nil || len(rows) != 1 || rows[0].Status != blogs.Revoked || rows[0].Revision != 3 {
				t.Fatal(rows, e)
			}
			if permission, e := f.posts.CheckBlogPermission(f.ctx, f.actor, f.pub.ID, "https://blog.example/a"); e != nil || permission.CanCreate {
				t.Fatal(permission, e)
			}
		})
	}
}

func TestPGBlogPendingRecoveryFencesRevokedThenRestoredIntent(t *testing.T) {
	for _, kind := range []string{"policy", "member", "session", "group_policy"} {
		t.Run(kind, func(t *testing.T) {
			initial := newBlogFixture(t)
			group := authorityUUID(t)
			if kind == "group_policy" {
				query(t, initial.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic-blog-"+group)
				query(t, initial.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, initial.actor, group)
			}
			f, d := pendingBlogDomain(t, initial)
			switch kind {
			case "group_policy":
				query(t, f.db, `INSERT INTO owned_authority_group_grants(group_id,pattern,deny)VALUES($1::uuid,'publishers.domains.manage',true)`, group)
				query(t, f.db, `DELETE FROM owned_authority_group_grants WHERE group_id=$1::uuid`, group)
			case "policy":
				query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1::uuid,'publishers.domains.manage',true)`, f.actor)
				query(t, f.db, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid AND pattern='publishers.domains.manage'`, f.actor)
			case "member":
				query(t, f.db, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.pub.ID, f.actor)
				query(t, f.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, f.pub.ID, f.actor)
			case "session":
				query(t, f.db, `UPDATE owned_authority_sessions SET epoch=epoch+1,authorization_epoch=authorization_epoch+1 WHERE id=(SELECT session_id FROM owned_publisher_domain_intents WHERE domain_id=$1::uuid)`, d.ID)
			}
			calls := 0
			f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { calls++; return true, nil })
			f = blogPool(t, f)
			if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 0 {
				t.Fatal(n, e)
			}
			var status int
			if e := f.db.QueryRow(`SELECT status FROM owned_publisher_domains WHERE id=$1::uuid`, d.ID).Scan(&status); e != nil || status != blogs.Revoked || calls != 0 {
				t.Fatal(status, calls, e)
			}
		})
	}
}

func TestPGBlogPendingRecoveryRetainsOriginalSessionDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "refresh_before_deadline"
		if expired {
			name = "expired_original_deadline"
		}
		t.Run(name, func(t *testing.T) {
			f, d := pendingBlogDomain(t, newBlogFixture(t))
			var original, current time.Time
			if e := f.db.QueryRow(`SELECT i.expires_at,s.expires_at FROM owned_publisher_domain_intents i JOIN owned_authority_sessions s ON s.id=i.session_id WHERE i.domain_id=$1::uuid`, d.ID).Scan(&original, &current); e != nil || !original.Equal(current) {
				t.Fatal(original, current, e)
			}
			if expired {
				// Advance the saved deadline to model elapsed time without a long sleep.
				query(t, f.db, `UPDATE owned_publisher_domain_intents SET expires_at=clock_timestamp()-interval '1 second' WHERE domain_id=$1::uuid`, d.ID)
			}
			// Model a routine token rotation and session extension, preserving the
			// authorization epoch. This issues no replacement bearer credentials.
			query(t, f.db, `UPDATE owned_authority_sessions SET epoch=epoch+1,expires_at=expires_at+interval '1 day' WHERE id=(SELECT session_id FROM owned_publisher_domain_intents WHERE domain_id=$1::uuid)`, d.ID)
			calls := 0
			f.configBlog.Verifier = blogVerifyFunc(func(context.Context, string, string) (bool, error) { calls++; return true, nil })
			f = blogPool(t, f)
			if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			status, wantCalls := blogs.Verified, 1
			if expired {
				status, wantCalls = blogs.Revoked, 0
			}
			var actual int
			if e := f.db.QueryRow(`SELECT status FROM owned_publisher_domains WHERE id=$1::uuid`, d.ID).Scan(&actual); e != nil || actual != status || calls != wantCalls {
				t.Fatal(actual, calls, e)
			}
			if !expired {
				var retained time.Time
				if e := f.db.QueryRow(`SELECT expires_at FROM owned_publisher_domain_intents WHERE domain_id=$1::uuid`, d.ID).Scan(&retained); e != nil || !retained.Equal(original) {
					t.Fatal("rotation rewrote original intent deadline", retained, original, e)
				}
			}
		})
	}
}

func TestPGBlogPendingRecoveryRechecksAfterProviderPolicyABA(t *testing.T) {
	f, d := pendingBlogDomain(t, newBlogFixture(t))
	f.configBlog.Verifier = blogVerifyFunc(func(ctx context.Context, _, _ string) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if _, e := f.db.ExecContext(ctx, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1::uuid,'publishers.domains.manage',true)`, f.actor); e != nil {
			return false, e
		}
		if _, e := f.db.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid AND pattern='publishers.domains.manage'`, f.actor); e != nil {
			return false, e
		}
		return true, nil
	})
	f = blogPool(t, f)
	if n, e := f.domains.ProcessPending(context.Background(), 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var status int
	if e := f.db.QueryRow(`SELECT status FROM owned_publisher_domains WHERE id=$1::uuid`, d.ID).Scan(&status); e != nil || status != blogs.Revoked {
		t.Fatal(status, e)
	}
}

func TestPGBlogScheduledPublicationFencesTransientPolicyRevocation(t *testing.T) {
	f := newBlogFixture(t)
	verifiedBlogDomain(t, f)
	due := scheduleFuture(t, f.db)
	p := createBlog(t, f, posts.Patch{PublishedAt: &due})
	query(t, f.db, `INSERT INTO owned_authority_grants(account_id,pattern,deny)VALUES($1::uuid,'posts.create.blog',true)`, f.actor)
	query(t, f.db, `DELETE FROM owned_authority_grants WHERE account_id=$1::uuid AND pattern='posts.create.blog'`, f.actor)
	scheduleDue(t, f.db, p)
	processSchedule(t, f.postMediaFixture)
	scheduleState(t, f.db, p.Schedule.ID, "blocked")
	scheduleHidden(t, f.postMediaFixture, p.ID)
}
