// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func federationPoolLimit(t *testing.T, db *sql.DB, limit int) {
	t.Helper()
	previous := db.Stats().MaxOpenConnections
	db.SetMaxOpenConns(limit)
	// Restore before fixture cleanup, including assertion failures.
	t.Cleanup(func() { db.SetMaxOpenConns(previous) })
}

func TestPGFederationDeliverySingleConnection(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		for _, quote := range []bool{false, true} {
			for _, revocation := range []string{"current", "key-before", "policy-before", "key-during", "policy-during"} {
				if !admitted && revocation != "current" {
					continue
				}
				name := "static/follow/" + revocation
				if admitted {
					name = "admitted/follow/" + revocation
				}
				if quote {
					name += "/quote"
				}
				t.Run(name, func(t *testing.T) {
					var x *federationFixture
					var policy f.PublicAdmissionPolicy
					if admitted {
						var target f.DiscoveryTarget
						x, target, policy = publicAdmissionFixture(t)
						finishPublicAdmission(t, x, target)
						var e error
						x.keys.remote, e = x.r.Keys().RemoteActor(x.ctx, target.Actor)
						if e != nil {
							t.Fatal(e)
						}
					} else {
						x = newFederationFixture(t, true)
					}
					var id int64
					if quote {
						note := quoteRemoteNote(t, x)
						post := x.makePost(t, "Synthetic one-connection quote target", posts.Public, false)
						request := quoteRequest(t, x, note, post)
						if e := x.receive(t, request); e != nil {
							t.Fatal(e)
						}
						grant := quoteGrantID(t, x, request)
						if e := x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Accept'`, grant).Scan(&id); e != nil {
							t.Fatal(e)
						}
						if n, e := x.r.ProcessDeliveries(context.Background(), &federationCapture{}, 1); e != nil || n != 1 {
							t.Fatal("superseded Follow", n, e)
						}
					} else {
						id = queueDeliveryFollow(t, x)
					}
					federationPoolLimit(t, x.db, 1)
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					defer cancel()
					revoke := func(ctx context.Context) error {
						actor := x.keys.remote.Actor
						if revocation == "policy-before" || revocation == "policy-during" {
							actor = ""
						}
						return x.r.RevokePublicAdmission(ctx, policy.Origin, actor)
					}
					before := revocation == "key-before" || revocation == "policy-before"
					if before {
						if e := revoke(ctx); e != nil {
							t.Fatal(e)
						}
					}
					var sends int
					transport := deliverySendFunc(func(ctx context.Context, _ *http.Request) (int, error) {
						sends++
						// With only one connection this also proves the dispatch
						// transaction committed and released it before Send.
						var observed bool
						if e := x.db.QueryRowContext(ctx, `SELECT status='inflight' AND attempts=1 AND dispatch_started_at IS NOT NULL AND lease_token IS NOT NULL FROM owned_federation_deliveries WHERE id=$1`, id).Scan(&observed); e != nil {
							return 0, e
						}
						if !observed {
							return 0, errors.New("missing committed dispatch observation")
						}
						if revocation == "key-during" || revocation == "policy-during" {
							return 202, revoke(ctx)
						}
						return 202, nil
					})
					waits := x.db.Stats().WaitCount
					if n, e := x.r.ProcessDeliveries(ctx, transport, 1); e != nil || n != 1 {
						t.Fatal("single-connection dispatch", n, e)
					}
					state, attempts, _ := deliveryRow(t, x.db, id)
					wantState, wantSends := "sent", 1
					if revocation != "current" {
						wantState = "dead"
						if quote {
							wantState = "cancelled"
						}
					}
					if before {
						wantSends = 0
					}
					if state != wantState || attempts != 1 || sends != wantSends || x.db.Stats().WaitCount != waits {
						t.Fatal("pool wait or lost delivery fence", state, attempts, sends, x.db.Stats())
					}
				})
			}
		}
	}
}

type federationPoolKeys struct {
	federationKeys
	second f.RemoteKey
}

func (k federationPoolKeys) RemoteActor(ctx context.Context, actor string) (f.RemoteKey, error) {
	if actor == k.second.Actor {
		return k.second, nil
	}
	return k.federationKeys.RemoteActor(ctx, actor)
}

type federationPoolGuard struct {
	*authoritypg.TransactionGuard
	armed   atomic.Bool
	entered chan int
	release <-chan struct{}
}

func (g *federationPoolGuard) StoredSessionEligible(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, session string, version, epoch int64, permissions ...string) (bool, error) {
	allowed, e := g.TransactionGuard.StoredSessionEligible(ctx, db, tx, actor, session, version, epoch, permissions...)
	if e != nil || !allowed || !g.armed.CompareAndSwap(true, false) {
		return allowed, e
	}
	var pid int
	if e := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		return false, e
	}
	g.entered <- pid
	select {
	case <-g.release:
		return allowed, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func TestPGFederationDeliverySaturatedPool(t *testing.T) {
	x := newFederationFixture(t, true)
	second := x.keys.remote.Snapshot()
	second.Actor += "-second"
	second.KeyID = second.Actor + "#main-key"
	release := make(chan struct{})
	guard := &federationPoolGuard{TransactionGuard: x.guard, entered: make(chan int, 1), release: release}
	var e error
	x.r, e = federationpg.New(x.db, x.config, federationPoolKeys{x.keys, second}, guard)
	if e != nil {
		t.Fatal(e)
	}
	firstID := queueDeliveryFollow(t, x)
	if e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: second.Actor}); e != nil {
		t.Fatal(e)
	}
	var secondID int64
	if e = x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE publisher_id=$1 AND actor_uri=$2 AND activity_type='Follow'`, x.publisher, second.Actor).Scan(&secondID); e != nil {
		t.Fatal(e)
	}
	// This independent pool observes PostgreSQL lock state only; it never
	// satisfies any repository query or raises the tested pool's capacity.
	observer := database(t)
	federationPoolLimit(t, x.db, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	guard.armed.Store(true)
	capture := &federationCapture{}
	done := make(chan error, 2)
	worker := func() {
		n, e := x.r.ProcessDeliveries(ctx, capture, 1)
		if e == nil && n != 1 {
			e = errors.New("worker did not own one delivery")
		}
		done <- e
	}
	go worker()
	var pid int
	select {
	case pid = <-guard.entered:
	case e = <-done:
		t.Fatal("first observation missed barrier", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go worker()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: observer, ctx: ctx}, pid, "owned_authority_accounts")
	if stats := x.db.Stats(); stats.InUse != 2 || stats.MaxOpenConnections != 2 {
		t.Fatal("repository pool was not fully occupied by observations", stats)
	}
	// Release the first worker only after the other transaction is known to
	// occupy the last connection while waiting on its canonical account lock.
	// Auxiliary r.db reads would now deadlock both until a context expires.
	unblock()
	for i := 0; i < 2; i++ {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	if len(capture.requests) != 2 {
		t.Fatal("lost delivery under saturation", len(capture.requests))
	}
	for _, id := range []int64{firstID, secondID} {
		state, attempts, _ := deliveryRow(t, x.db, id)
		if state != "sent" || attempts != 1 {
			t.Fatal("saturation consumed attempt or changed lease outcome", id, state, attempts)
		}
	}
}

func TestPGFederationTransactionalLookupSingleConnection(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		name := "static-inbox-quote-fanout"
		if admitted {
			name = "admitted-inbox-quote-fanout"
		}
		t.Run(name, func(t *testing.T) {
			var x *federationFixture
			if admitted {
				var target f.DiscoveryTarget
				x, target, _ = publicAdmissionFixture(t)
				finishPublicAdmission(t, x, target)
				var e error
				x.keys.remote, e = x.r.Keys().RemoteActor(x.ctx, target.Actor)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				x = newFederationFixture(t, false)
			}
			if e := x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: true, Manual: false}); e != nil {
				t.Fatal(e)
			}
			federationPoolLimit(t, x.db, 1)
			ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
			defer cancel()
			x.ctx = ctx
			note := quoteRemoteNote(t, x)
			post := x.makePost(t, "Synthetic single-connection transaction lifecycle", posts.Public, false)
			request := quoteRequest(t, x, note, post)
			if e := x.receive(t, request); e != nil {
				t.Fatal("quote inbox", e)
			}
			id := quoteGrantID(t, x, request)
			if grant, e := x.r.QuoteAuthorization(ctx, id); e != nil || grant.ID != x.config.Origin+"/quote-authorizations/"+id {
				t.Fatal("quote current lookup", grant, e)
			}
			if e := x.r.RevokeQuoteAuthorization(ctx, x.actor, id); e != nil {
				t.Fatal("quote withdrawal", e)
			}
			if _, e := x.r.QuoteAuthorization(ctx, id); !errors.Is(e, f.ErrNotFound) {
				t.Fatal("revoked quote became current", e)
			}
			var revoked bool
			if e := x.db.QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Delete') FROM owned_federation_quote_authorizations WHERE id=$1`, id).Scan(&revoked); e != nil || !revoked {
				t.Fatal("quote withdrawal lost its bounded notification", revoked, e)
			}
			if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
				t.Fatal("inbox freshness", e)
			}
			if n, e := x.r.ReconcilePosts(ctx, 1); e != nil || n != 1 {
				t.Fatal("post fanout lookup", n, e)
			}
			var deliveries int
			if e := x.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_deliveries WHERE post_id=$1 AND actor_uri=$2 AND activity_type='Create'`, post.ID, x.keys.remote.Actor).Scan(&deliveries); e != nil || deliveries != 1 {
				t.Fatal("missing post fanout", deliveries, e)
			}
		})
	}
	t.Run("discovery-request-read-completion", func(t *testing.T) {
		x, target := discoveryFixture(t)
		federationPoolLimit(t, x.db, 1)
		ctx, cancel := context.WithTimeout(x.ctx, 8*time.Second)
		defer cancel()
		job, e := x.r.RequestDiscovery(ctx, x.actor, x.publisher, target.Resource)
		if e != nil {
			t.Fatal("discovery request", e)
		}
		if got, e := x.r.Discovery(ctx, x.actor, x.publisher, target.Resource); e != nil || got.ID != job.ID {
			t.Fatal("discovery read", got, e)
		}
		if n, e := x.r.ProcessDiscoveries(ctx, discoveryResponse(t, x, target), 1); e != nil || n != 1 {
			t.Fatal("discovery completion", n, e)
		}
		if state := discoveryState(t, x.db, job.ID); state != "succeeded" {
			t.Fatal(state)
		}
	})
}
