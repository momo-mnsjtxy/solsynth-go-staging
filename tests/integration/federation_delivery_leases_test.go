// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/posts"
)

type deliverySendFunc func(context.Context, *http.Request) (int, error)

func (fn deliverySendFunc) Send(ctx context.Context, r *http.Request) (int, error) { return fn(ctx, r) }
func queueDeliveryFollow(t *testing.T, x *federationFixture) int64 {
	t.Helper()
	if e := x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "follow", RemoteActor: x.keys.remote.Actor}); e != nil {
		t.Fatal(e)
	}
	var id int64
	if e := x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE publisher_id=$1 AND activity_type='Follow'`, x.publisher).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func deliveryRow(t *testing.T, db *sql.DB, id int64) (string, int, string) {
	t.Helper()
	var state, row string
	var attempts int
	if e := db.QueryRow(`SELECT status,attempts,row_to_json(d)::text FROM owned_federation_deliveries d WHERE id=$1`, id).Scan(&state, &attempts, &row); e != nil {
		t.Fatal(e)
	}
	return state, attempts, row
}
func awaitDeliverySignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(4 * time.Second):
		t.Fatal("delivery barrier timeout")
	}
}

func TestPGFederationDeliveryCommittedLeaseAndRevocation(t *testing.T) {
	for _, action := range []string{"session", "disable", "block", "post-privacy"} {
		t.Run(action, func(t *testing.T) {
			x := newFederationFixture(t, action != "post-privacy")
			var id int64
			var postID string
			if action == "post-privacy" {
				if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
					t.Fatal(e)
				}
				if _, e := x.r.ProcessDeliveries(context.Background(), &federationCapture{}, 1); e != nil {
					t.Fatal(e)
				}
				post := x.makePost(t, "Synthetic pending public content", posts.Public, false)
				postID = post.ID
				if _, e := x.r.ReconcilePosts(context.Background(), 1); e != nil {
					t.Fatal(e)
				}
				if e := x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE post_id=$1`, postID).Scan(&id); e != nil {
					t.Fatal(e)
				}
			} else {
				id = queueDeliveryFollow(t, x)
			}
			var sent atomic.Int64
			transport := deliverySendFunc(func(ctx context.Context, r *http.Request) (int, error) {
				sent.Add(1)
				// Every relevant lock is available on a distinct transaction during Send.
				tx, e := x.db.BeginTx(ctx, nil)
				if e != nil {
					return 0, e
				}
				defer tx.Rollback()
				if _, e = tx.ExecContext(ctx, `SET LOCAL lock_timeout='300ms'`); e != nil {
					return 0, e
				}
				if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, x.actor); e != nil {
					return 0, e
				}
				if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_publishers WHERE id=$1 FOR UPDATE`, x.publisher); e != nil {
					return 0, e
				}
				if _, e = tx.ExecContext(ctx, `SELECT publisher_id FROM owned_publisher_members WHERE publisher_id=$1 FOR UPDATE`, x.publisher); e != nil {
					return 0, e
				}
				if postID != "" {
					if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_text_posts WHERE id=$1 FOR UPDATE`, postID); e != nil {
						return 0, e
					}
				}
				var committed bool
				if e = tx.QueryRowContext(ctx, `SELECT status='inflight' AND dispatch_started_at IS NOT NULL AND lease_until>clock_timestamp() AND attempts=1 FROM owned_federation_deliveries WHERE id=$1 FOR UPDATE`, id).Scan(&committed); e != nil {
					return 0, e
				}
				if !committed {
					return 0, errors.New("dispatch observation was not committed")
				}
				if e = tx.Commit(); e != nil {
					return 0, e
				}
				switch action {
				case "session":
					e = x.issuer.Revoke(ctx, x.token, "current")
				case "disable":
					e = x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: false, Manual: true})
				case "block":
					e = x.r.Change(x.ctx, x.actor, x.publisher, f.Change{Action: "block", RemoteActor: x.keys.remote.Actor})
				case "post-privacy":
					private := posts.Private
					_, e = x.post.Update(x.ctx, x.actor, postID, posts.Patch{Visibility: &private})
				}
				if e != nil {
					return 0, e
				}
				return 202, nil
			})
			if n, e := x.r.ProcessDeliveries(context.Background(), transport, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			state, attempts, _ := deliveryRow(t, x.db, id)
			if sent.Load() != 1 || state != "cancelled" || attempts != 1 {
				t.Fatal("late success survived revocation", sent.Load(), state, attempts)
			}
		})
	}
}

func TestPGFederationDeliveryLeaseReclaimAndStaleResults(t *testing.T) {
	x := newFederationFixture(t, true)
	id := queueDeliveryFollow(t, x)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var original []byte
	first := deliverySendFunc(func(ctx context.Context, r *http.Request) (int, error) {
		var e error
		original, e = io.ReadAll(r.Body)
		close(entered)
		<-release
		return 202, e
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDeliveries(context.Background(), first, 1); done <- e }()
	awaitDeliverySignal(t, entered)
	query(t, x.db, `UPDATE owned_federation_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
	second := &federationCapture{}
	if n, e := x.r.ProcessDeliveries(context.Background(), second, 1); e != nil || n != 1 {
		t.Fatal("reclaim", n, e)
	}
	state, attempts, saved := deliveryRow(t, x.db, id)
	if state != "sent" || attempts != 2 || len(second.requests) != 1 || !bytes.Equal(original, second.requests[0]) {
		t.Fatal("reclaim altered stable activity", state, attempts)
	}
	unblock()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	_, _, after := deliveryRow(t, x.db, id)
	if after != saved {
		t.Fatal("stale worker acknowledged replacement lease")
	}
}

func TestPGFederationDeliveryCurrentAuthorityAndTrustABA(t *testing.T) {
	cases := []string{"session", "policy", "member", "owner", "publisher", "peer", "remote-key", "local-key", "missing-guard"}
	for _, change := range cases {
		t.Run(change, func(t *testing.T) {
			x := newFederationFixture(t, true)
			id := queueDeliveryFollow(t, x)
			switch change {
			case "session":
				if e := x.issuer.Revoke(context.Background(), x.token, "current"); e != nil {
					t.Fatal(e)
				}
			case "policy":
				store := authoritypg.New(x.db)
				if e := store.ReplacePolicy(context.Background(), x.actor, []authority.Rule{{Pattern: "publishers.*", Deny: true}}, nil); e != nil {
					t.Fatal(e)
				}
				if e := store.ReplacePolicy(context.Background(), x.actor, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "publishers.*"}}, nil); e != nil {
					t.Fatal(e)
				}
			case "member":
				query(t, x.db, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp() WHERE publisher_id=$1 AND account_id=$2`, x.publisher, x.actor)
				query(t, x.db, `UPDATE owned_publisher_members SET revoked_at=NULL WHERE publisher_id=$1 AND account_id=$2`, x.publisher, x.actor)
			case "owner":
				query(t, x.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, x.actor)
				query(t, x.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, x.actor)
			case "publisher":
				query(t, x.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, x.publisher)
				query(t, x.db, `UPDATE owned_federation_publishers SET enabled=true WHERE publisher_id=$1`, x.publisher)
			case "peer":
				query(t, x.db, `UPDATE owned_federation_relationships SET status='undone' WHERE publisher_id=$1 AND direction='out'`, x.publisher)
				query(t, x.db, `UPDATE owned_federation_relationships SET status='pending' WHERE publisher_id=$1 AND direction='out'`, x.publisher)
			case "remote-key", "local-key":
				key, e := rsa.GenerateKey(rand.Reader, 2048)
				if e != nil {
					t.Fatal(e)
				}
				if change == "remote-key" {
					x.keys.remote.PublicKey = &key.PublicKey
				} else {
					x.keys.locals[x.publisher] = key
				}
				x.r, e = federationpg.New(x.db, x.config, x.keys, x.guard)
				if e != nil {
					t.Fatal(e)
				}
			case "missing-guard":
				var e error
				x.r, e = federationpg.New(x.db, x.config, x.keys, nil)
				if e != nil {
					t.Fatal(e)
				}
			}
			capture := &federationCapture{}
			_, e := x.r.ProcessDeliveries(context.Background(), capture, 1)
			if change == "missing-guard" {
				if !errors.Is(e, f.ErrUnavailable) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if len(capture.requests) != 0 {
				t.Fatal("revoked or rebound intent sent")
			}
			state, _, _ := deliveryRow(t, x.db, id)
			if change != "missing-guard" && state != "cancelled" && state != "dead" {
				t.Fatal(state)
			}
		})
	}
}

func TestPGFederationDeliveryAuthorityAfterLockWait(t *testing.T) {
	x := newFederationFixture(t, true)
	id := queueDeliveryFollow(t, x)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, x.actor); e != nil {
		t.Fatal(e)
	}
	capture := &federationCapture{}
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDeliveries(ctx, capture, 1); done <- e }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_authority_accounts")
	if _, e = hold.ExecContext(ctx, `UPDATE owned_authority_grants SET deny=true WHERE account_id=$1 AND pattern='publishers.*'`, x.actor); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `UPDATE owned_authority_grants SET deny=false WHERE account_id=$1 AND pattern='publishers.*'`, x.actor); e != nil {
		t.Fatal(e)
	}
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	state, _, _ := deliveryRow(t, x.db, id)
	if len(capture.requests) != 0 || state != "cancelled" {
		t.Fatal("lock-wait ABA sent", state)
	}
}

func TestPGFederationDeliveryOriginalBudgetAndImmutableIntent(t *testing.T) {
	x := newFederationFixture(t, true)
	id := queueDeliveryFollow(t, x)
	for _, stmt := range []string{
		`UPDATE owned_federation_deliveries SET max_attempts=12 WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET expires_at=expires_at+interval '1 hour' WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET origin_intent=origin_intent-'session' WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET payload='{}'::bytea WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET remote_binding=repeat('0',64) WHERE id=$1`,
	} {
		if _, e := x.db.Exec(stmt, id); e == nil {
			t.Fatal("intent mutation accepted", stmt)
		}
	}
	config := x.config
	config.MaxAttempts = 12
	r, e := federationpg.New(x.db, config, x.keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	capture := &federationCapture{codes: []int{503, 503, 503}}
	for attempt := 1; attempt <= 3; attempt++ {
		if n, e := r.ProcessDeliveries(context.Background(), capture, 1); e != nil || n != 1 {
			t.Fatal(attempt, n, e)
		}
		x.clock.Add(361)
	}
	if n, e := r.ProcessDeliveries(context.Background(), capture, 1); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	state, attempts, _ := deliveryRow(t, x.db, id)
	if state != "dead" || attempts != 3 || len(capture.requests) != 3 {
		t.Fatal(state, attempts)
	}
	if !bytes.Equal(capture.requests[0], capture.requests[2]) {
		t.Fatal("retry payload changed")
	}
}

func TestPGFederationDeliveryPublicReservationRevocation(t *testing.T) {
	x, target, policy := publicAdmissionFixture(t)
	finishPublicAdmission(t, x, target)
	id := queueDeliveryFollow(t, x)
	transport := deliverySendFunc(func(ctx context.Context, r *http.Request) (int, error) {
		// The remote policy/key locks are also released before entering Send.
		if e := x.r.RevokePublicAdmission(ctx, policy.Origin, target.Actor); e != nil {
			return 0, e
		}
		return 202, nil
	})
	if n, e := x.r.ProcessDeliveries(context.Background(), transport, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	state, _, _ := deliveryRow(t, x.db, id)
	if state == "sent" || state == "pending" {
		t.Fatal("revoked reservation acknowledged", state)
	}
	// Static fallback at the same actor/key cannot revive an expired incarnation.
	fallback, e := federationpg.New(x.db, x.config, x.keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	capture := &federationCapture{}
	if _, e = fallback.ProcessDeliveries(context.Background(), capture, 1); e != nil {
		t.Fatal(e)
	}
	if len(capture.requests) != 0 {
		t.Fatal("static pin revived reserved peer")
	}
	for _, before := range []bool{true, false} {
		name := "quote-revoke-after-observation"
		if before {
			name = "quote-revoke-before-observation"
		}
		t.Run(name, func(t *testing.T) {
			x, target, policy := publicAdmissionFixture(t)
			finishPublicAdmission(t, x, target)
			var e error
			// The synthetic verification fixture must retain the same admitted
			// incarnation used by the repository's durable key provider.
			x.keys.remote, e = x.r.Keys().RemoteActor(x.ctx, target.Actor)
			if e != nil {
				t.Fatal(e)
			}
			note := quoteRemoteNote(t, x)
			post := x.makePost(t, "Synthetic admitted quote target", posts.Public, false)
			request := quoteRequest(t, x, note, post)
			if e = x.receive(t, request); e != nil {
				t.Fatal(e)
			}
			grant := quoteGrantID(t, x, request)
			var id int64
			if e = x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Accept'`, grant).Scan(&id); e != nil {
				t.Fatal(e)
			}
			// The original outbound Follow was superseded by its incoming Accept.
			if n, e := x.r.ProcessDeliveries(context.Background(), &federationCapture{}, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			var sends atomic.Int64
			transport := deliverySendFunc(func(ctx context.Context, _ *http.Request) (int, error) {
				sends.Add(1)
				// When observation wins, quote/key/table locks must all be released
				// before transport. Revocation must finish without a lock retry.
				return 202, x.r.RevokePublicAdmission(ctx, policy.Origin, target.Actor)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if before {
				hold, e := x.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer hold.Rollback()
				var pid int
				if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				// UPDATE takes this table lock before attempting its target row.
				if _, e = hold.ExecContext(ctx, `LOCK TABLE owned_federation_public_keys IN ROW EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() { _, e := x.r.ProcessDeliveries(ctx, transport, 1); done <- e }()
				waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_public_keys")
				// Fail immediately if the blocked worker already owns a key row:
				// that is the former table/row upgrade deadlock, without waiting
				// for PostgreSQL to pick a deadlock victim or retrying either side.
				if _, e = hold.ExecContext(ctx, `SELECT actor_uri FROM owned_federation_public_keys WHERE actor_uri=$1 FOR UPDATE NOWAIT`, target.Actor); e != nil {
					t.Fatal("delivery took key row before reservation table", e)
				}
				if _, e = hold.ExecContext(ctx, `UPDATE owned_federation_public_keys SET revoked_at=clock_timestamp() WHERE actor_uri=$1`, target.Actor); e != nil {
					t.Fatal(e)
				}
				if e = hold.Commit(); e != nil {
					t.Fatal(e)
				}
				if e = <-done; e != nil {
					t.Fatal(e)
				}
			} else if n, e := x.r.ProcessDeliveries(ctx, transport, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			state, attempts, _ := deliveryRow(t, x.db, id)
			wantSends := int64(1)
			if before {
				wantSends = 0
			}
			if state != "cancelled" || attempts != 1 || sends.Load() != wantSends {
				t.Fatal("quote revocation was not fenced", state, attempts, sends.Load())
			}
		})
	}
}

func TestPGFederationDeliveryLocalWithdrawalWithoutSigner(t *testing.T) {
	x := newFederationFixture(t, true)
	note := quoteRemoteNote(t, x)
	post := x.makePost(t, "Synthetic withdrawal target", 0, false)
	request := quoteRequest(t, x, note, post)
	if e := x.receive(t, request); e != nil {
		t.Fatal(e)
	}
	id := quoteGrantID(t, x, request)
	delete(x.keys.locals, x.publisher)
	if e := x.r.RevokeQuoteAuthorization(x.ctx, x.actor, id); e != nil {
		t.Fatal("missing signer prevented local withdrawal", e)
	}
	var revoked bool
	var notifications int
	if e := x.db.QueryRow(`SELECT revoked_at IS NOT NULL,(SELECT count(*) FROM owned_federation_deliveries WHERE quote_id=$1 AND activity_type='Delete') FROM owned_federation_quote_authorizations WHERE id=$1`, id).Scan(&revoked, &notifications); e != nil || !revoked || notifications != 0 {
		t.Fatal(revoked, notifications, e)
	}
}

func TestPGFederationDeliveryConcurrentClaims(t *testing.T) {
	x := newFederationFixture(t, true)
	id := queueDeliveryFollow(t, x)
	capture := &federationCapture{}
	var workers sync.WaitGroup
	var attempted atomic.Int64
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			n, e := x.r.ProcessDeliveries(context.Background(), capture, 1)
			attempted.Add(int64(n))
			errs <- e
		}()
	}
	workers.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	state, attempts, _ := deliveryRow(t, x.db, id)
	if attempted.Load() != 1 || len(capture.requests) != 1 || state != "sent" || attempts != 1 {
		t.Fatal("concurrent claim duplicated", attempted.Load(), state, attempts)
	}
	var a f.Activity
	if json.Unmarshal(capture.requests[0], &a) != nil || a.ID == "" {
		t.Fatal("missing stable peer idempotency identity")
	}
}

func TestPGFederationDeliveryDeadlineAndUncertainResult(t *testing.T) {
	t.Run("original-session-deadline", func(t *testing.T) {
		x := newFederationFixture(t, true)
		principal, e := x.verifier.Authenticate(context.Background(), x.token)
		if e != nil {
			t.Fatal(e)
		}
		query(t, x.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '1500 milliseconds' WHERE id=$1`, principal.Session.ID)
		id := queueDeliveryFollow(t, x)
		var expires time.Time
		if e = x.db.QueryRow(`SELECT expires_at FROM owned_federation_deliveries WHERE id=$1`, id).Scan(&expires); e != nil {
			t.Fatal(e)
		}
		// Extend the session only; the immutable job's original boundary stays fixed.
		query(t, x.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, principal.Session.ID)
		expired, cancel := context.WithDeadline(context.Background(), expires.Add(20*time.Millisecond))
		defer cancel()
		<-expired.Done()
		capture := &federationCapture{}
		if _, e = x.r.ProcessDeliveries(context.Background(), capture, 1); e != nil {
			t.Fatal(e)
		}
		state, attempts, _ := deliveryRow(t, x.db, id)
		if len(capture.requests) != 0 || state != "cancelled" || attempts != 0 {
			t.Fatal("session extension renewed original intent", state, attempts)
		}
	})
	t.Run("expired-unacknowledged-owner", func(t *testing.T) {
		x := newFederationFixture(t, true)
		id := queueDeliveryFollow(t, x)
		transport := deliverySendFunc(func(ctx context.Context, r *http.Request) (int, error) {
			query(t, x.db, `UPDATE owned_federation_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
			return 202, nil
		})
		if _, e := x.r.ProcessDeliveries(context.Background(), transport, 1); e != nil {
			t.Fatal(e)
		}
		state, attempts, _ := deliveryRow(t, x.db, id)
		if state != "inflight" || attempts != 1 {
			t.Fatal("expired owner acknowledged", state, attempts)
		}
		capture := &federationCapture{}
		if n, e := x.r.ProcessDeliveries(context.Background(), capture, 1); e != nil || n != 1 {
			t.Fatal(n, e)
		}
		state, attempts, _ = deliveryRow(t, x.db, id)
		if state != "sent" || attempts != 2 {
			t.Fatal(state, attempts)
		}
	})
	t.Run("cancelled-network-context", func(t *testing.T) {
		x := newFederationFixture(t, true)
		id := queueDeliveryFollow(t, x)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		transport := deliverySendFunc(func(_ context.Context, r *http.Request) (int, error) { cancel(); return 202, nil })
		if _, e := x.r.ProcessDeliveries(ctx, transport, 1); e != nil {
			t.Fatal(e)
		}
		state, attempts, _ := deliveryRow(t, x.db, id)
		if state != "inflight" || attempts != 1 {
			t.Fatal("uncertain send recorded as success", state, attempts)
		}
	})
}

func TestPGFederationDeliveryFinalPolicyAfterLeaseWait(t *testing.T) {
	x := newFederationFixture(t, true)
	id := queueDeliveryFollow(t, x)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	transport := deliverySendFunc(func(ctx context.Context, r *http.Request) (int, error) {
		close(entered)
		select {
		case <-release:
			return 202, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDeliveries(ctx, transport, 1); done <- e }()
	awaitDeliverySignal(t, entered)
	hold, e := x.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	var pid int
	if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_federation_deliveries WHERE id=$1 FOR UPDATE`, id); e != nil {
		t.Fatal(e)
	}
	unblock()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_deliveries")
	// Synthetic generation-only probe; account-first policy-writer tests remain separate.
	syntheticFederationHistoryTransition(t, ctx, hold, "delivery", x.actor)
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	state, _, _ := deliveryRow(t, x.db, id)
	if state != "cancelled" {
		t.Fatal("completion retained stale policy", state)
	}
}
