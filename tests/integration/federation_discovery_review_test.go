// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

func TestPGFederationDiscoveryPolicyAndMembershipABA(t *testing.T) {
	_ = database(t)
	for _, during := range []bool{false, true} {
		phase := "before-fetch"
		if during {
			phase = "during-fetch"
		}
		for _, change := range []string{"grants", "owner", "owner-disabled", "member", "enabled", "block", "session-scope", "group-grant", "group-member"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				x, target := discoveryFixture(t)
				group := authorityUUID(t)
				if strings.HasPrefix(change, "group-") {
					query(t, x.db, `INSERT INTO owned_authority_groups(id,name)VALUES($1::uuid,$2::text)`, group, "synthetic_discovery_group_"+group)
					query(t, x.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, x.actor, group)
					query(t, x.db, `INSERT INTO owned_authority_group_grants(group_id,pattern)VALUES($1::uuid,'publishers.*')`, group)
				}
				job := discoveryRequest(t, x, target)
				mutate := func() {
					t.Helper()
					switch change {
					case "grants":
						store := authoritypg.New(x.db)
						if e := store.ReplacePolicy(x.ctx, x.actor, nil, nil); e != nil {
							t.Fatal(e)
						}
						if e := store.ReplacePolicy(x.ctx, x.actor, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "publishers.*"}}, nil); e != nil {
							t.Fatal(e)
						}
					case "owner":
						other := authorityUUID(t)
						query(t, x.db, `INSERT INTO owned_authority_accounts(id,name,nick,created_at)VALUES($1::uuid,$2::text,$2::text,clock_timestamp())`, other, "synthetic_discovery_other_"+other)
						query(t, x.db, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, x.publisher, other)
						query(t, x.db, `UPDATE owned_publishers SET account_id=$2::uuid WHERE id=$1::uuid`, x.publisher, x.actor)
					case "owner-disabled":
						query(t, x.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1::uuid`, x.actor)
						query(t, x.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1::uuid`, x.actor)
					case "member":
						query(t, x.db, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor)
						query(t, x.db, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor)
					case "enabled":
						query(t, x.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1::uuid`, x.publisher)
						query(t, x.db, `UPDATE owned_federation_publishers SET enabled=true WHERE publisher_id=$1::uuid`, x.publisher)
					case "block":
						if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "block", RemoteActor: target.Actor}); e != nil {
							t.Fatal(e)
						}
						if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "unblock", RemoteActor: target.Actor}); e != nil {
							t.Fatal(e)
						}
					case "session-scope":
						var scopes string
						if e := x.db.QueryRow(`SELECT scopes::text FROM owned_authority_sessions WHERE account_id=$1::uuid`, x.actor).Scan(&scopes); e != nil {
							t.Fatal(e)
						}
						query(t, x.db, `UPDATE owned_authority_sessions SET scopes='["posts.view"]'::jsonb WHERE account_id=$1::uuid`, x.actor)
						query(t, x.db, `UPDATE owned_authority_sessions SET scopes=$2::jsonb WHERE account_id=$1::uuid`, x.actor, scopes)
					case "group-grant":
						query(t, x.db, `UPDATE owned_authority_group_grants SET deny=true WHERE group_id=$1::uuid`, group)
						query(t, x.db, `UPDATE owned_authority_group_grants SET deny=false WHERE group_id=$1::uuid`, group)
					case "group-member":
						query(t, x.db, `DELETE FROM owned_authority_group_members WHERE account_id=$1::uuid AND group_id=$2::uuid`, x.actor, group)
						query(t, x.db, `INSERT INTO owned_authority_group_members(account_id,group_id)VALUES($1::uuid,$2::uuid)`, x.actor, group)
					}
				}
				calls := 0
				response := discoveryResponse(t, x, target)
				fetch := discoveryFetchFunc(func(ctx context.Context, u, a string) (f.FetchResponse, error) {
					calls++
					if during && calls == 1 {
						mutate()
					}
					return response(ctx, u, a)
				})
				if !during {
					mutate()
				}
				if n, e := x.r.ProcessDiscoveries(context.Background(), fetch, 1); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				if state := discoveryState(t, x.db, job.ID); state != "cancelled" {
					t.Fatal("old intent revived", change, state)
				}
				if !during && calls != 0 {
					t.Fatal("revoked intent fetched", calls)
				}
				if replay := discoveryRequest(t, x, target); replay.ID != job.ID || replay.State != "cancelled" {
					t.Fatal("replay replaced cancelled admission", replay)
				}
				if _, e := x.r.ProcessDiscoveries(context.Background(), fetch, 1); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

func TestPGFederationDiscoveryAdmissionWaitABA(t *testing.T) {
	_ = database(t)
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "read"}[read], func(t *testing.T) {
			x, target := discoveryFixture(t)
			if read {
				discoveryRequest(t, x, target)
			}
			ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
			defer cancel()
			hold, e := x.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer hold.Rollback()
			var pid int
			if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, x.actor); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if read {
					_, e := x.r.Discovery(ctx, x.actor, x.publisher, target.Resource)
					done <- e
				} else {
					_, e := x.r.RequestDiscovery(ctx, x.actor, x.publisher, target.Resource)
					done <- e
				}
			}()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_authority_accounts")
			if _, e = hold.ExecContext(ctx, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor); e != nil {
				t.Fatal(e)
			}
			if _, e = hold.ExecContext(ctx, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor); e != nil {
				t.Fatal(e)
			}
			if e = hold.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, f.ErrForbidden) {
				t.Fatal("pre-wait admission restored", e)
			}
			if !read {
				var n int
				if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_discoveries WHERE publisher_id=$1::uuid`, x.publisher).Scan(&n); e != nil || n != 0 {
					t.Fatal("waiter left work", n, e)
				}
			}
		})
	}
}

func TestPGFederationDiscoveryOriginalDeadlineAndConfiguration(t *testing.T) {
	_ = database(t)
	t.Run("original-deadline", func(t *testing.T) {
		x, target := discoveryFixture(t)
		// The saved ancestor deadline uses actual PG time. Extension models ordinary
		// refresh without waiting for an hour or rewriting the immutable job record.
		parent := authorityUUID(t)
		query(t, x.db, `INSERT INTO owned_authority_sessions(id,account_id,parent_id,label,type,client_id,created_at,last_granted_at,category,trusted,epoch,authorization_epoch,account_version,expires_at,scopes)SELECT $2::uuid,account_id,NULL,'synthetic original ancestor',0,'',created_at,last_granted_at,'device',false,1,1,account_version,clock_timestamp()+interval '3 seconds',scopes FROM owned_authority_sessions WHERE account_id=$1::uuid`, x.actor, parent)
		query(t, x.db, `UPDATE owned_authority_sessions SET parent_id=$2::uuid WHERE account_id=$1::uuid AND id<>$2::uuid`, x.actor, parent)
		job := discoveryRequest(t, x, target)
		var original time.Time
		if e := x.db.QueryRow(`SELECT expires_at FROM owned_federation_discoveries WHERE id=$1::uuid`, job.ID).Scan(&original); e != nil {
			t.Fatal(e)
		}
		query(t, x.db, `UPDATE owned_authority_sessions SET epoch=epoch+1,expires_at=clock_timestamp()+interval '1 hour' WHERE account_id=$1::uuid`, x.actor)
		select {
		case <-time.After(time.Until(original) + 25*time.Millisecond):
		case <-x.ctx.Done():
			t.Fatal(x.ctx.Err())
		}
		called := false
		if n, e := x.r.ProcessDiscoveries(context.Background(), discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) {
			called = true
			return f.FetchResponse{}, nil
		}), 1); e != nil || n != 1 || called || discoveryState(t, x.db, job.ID) != "cancelled" {
			t.Fatal("refresh extended original admission", n, e, called)
		}
		var retained time.Time
		if e := x.db.QueryRow(`SELECT expires_at FROM owned_federation_discoveries WHERE id=$1::uuid`, job.ID).Scan(&retained); e != nil || !retained.Equal(original) {
			t.Fatal(retained, original, e)
		}
	})
	t.Run("configuration", func(t *testing.T) {
		x, target := discoveryFixture(t)
		job := discoveryRequest(t, x, target)
		policy := x.config
		policy.MaxAttempts++
		replacement, e := federationpg.NewWithDiscovery(x.db, policy, x.keys, x.guard, f.DiscoveryFeatures{Targets: []f.DiscoveryTarget{target}})
		if e != nil {
			t.Fatal(e)
		}
		calls := 0
		fetch := discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) {
			calls++
			return f.FetchResponse{Status: 503}, nil
		})
		if _, e = replacement.ProcessDiscoveries(x.ctx, fetch, 1); e != nil || calls != 0 || discoveryState(t, x.db, job.ID) != "cancelled" {
			t.Fatal("configuration extended old work", e, calls)
		}
		if _, e = x.r.ProcessDiscoveries(x.ctx, fetch, 1); e != nil || calls != 0 {
			t.Fatal("restored config revived work", e, calls)
		}
		if _, e = x.db.Exec(`UPDATE owned_federation_discoveries SET expires_at=expires_at+interval '1 day',max_attempts=max_attempts+1 WHERE id=$1::uuid`, job.ID); e == nil {
			t.Fatal("original bounds rewritten")
		}
	})
}

func TestPGFederationDiscoveryCurrentPolicyAfterJobLockWait(t *testing.T) {
	x, target := discoveryFixture(t)
	job := discoveryRequest(t, x, target)
	ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
	defer cancel()
	hold, e := x.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	fetched, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	response := discoveryResponse(t, x, target)
	fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
		v, e := response(c, u, a)
		if u == target.Actor {
			close(fetched)
			select {
			case <-release:
			case <-c.Done():
				return f.FetchResponse{}, c.Err()
			}
		}
		return v, e
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDiscoveries(ctx, fetch, 1); done <- e }()
	select {
	case <-fetched:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var pid int
	if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_federation_discoveries WHERE id=$1::uuid FOR UPDATE`, job.ID); e != nil {
		t.Fatal(e)
	}
	unblock()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_discoveries")
	// Direct maintained-table fixture writes deliberately bypass canonical locks
	// to prove the completion's final post-job-wait statement reads fresh policy.
	if _, e = hold.ExecContext(ctx, `UPDATE owned_authority_grants SET deny=true WHERE account_id=$1::uuid AND pattern='publishers.*'`, x.actor); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `UPDATE owned_authority_grants SET deny=false WHERE account_id=$1::uuid AND pattern='publishers.*'`, x.actor); e != nil {
		t.Fatal(e)
	}
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil || discoveryState(t, x.db, job.ID) != "cancelled" {
		t.Fatal("job-wait policy ABA survived", e)
	}
}
