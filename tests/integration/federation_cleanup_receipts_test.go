// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func configureCleanup(t *testing.T, x *federationFixture, activate bool) {
	t.Helper()
	query(t, x.db, `INSERT INTO owned_authority_grants(account_id,pattern) VALUES($1,$2)`, x.actor, f.CleanupPermission)
	x.config.CleanupPolicyID = authorityUUID(t)
	var e error
	x.r, e = federationpg.New(x.db, x.config, x.keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	if activate {
		if e = x.r.ActivateCleanupPolicy(x.ctx, x.actor, x.publisher); e != nil {
			t.Fatal(e)
		}
	}
	// A grant mutation must not revive the preceding normal publication intent.
	if e = x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: true, Manual: false}); e != nil {
		t.Fatal(e)
	}
}
func queueCleanupSource(t *testing.T, x *federationFixture) (string, int64) {
	t.Helper()
	if e := x.receive(t, x.activity(t, "Follow", x.actorURI)); e != nil {
		t.Fatal(e)
	}
	if _, e := x.r.ProcessDeliveries(x.ctx, &federationCapture{}, 20); e != nil {
		t.Fatal(e)
	}
	post := x.makePost(t, "Synthetic formerly public receipt content", posts.Public, false)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	var id int64
	if e := x.db.QueryRow(`SELECT id FROM owned_federation_deliveries WHERE post_id=$1 AND activity_type='Create'`, post.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return post.ID, id
}
func hideCleanupPost(t *testing.T, x *federationFixture, post string) {
	t.Helper()
	query(t, x.db, `UPDATE owned_text_posts SET visibility=3,content='Synthetic private bytes must never be sent',updated_at=clock_timestamp() WHERE id=$1`, post)
	if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestPGFederationCleanupReceiptAmbiguousDispatchAndNeverDispatched(t *testing.T) {
	for _, mode := range []string{"ambiguous", "never", "disabled", "unactivated"} {
		t.Run(mode, func(t *testing.T) {
			x := newFederationFixture(t, false)
			if mode != "disabled" {
				configureCleanup(t, x, mode != "unactivated")
			}
			post, source := queueCleanupSource(t, x)
			sent := 0
			if mode != "never" {
				transport := deliverySendFunc(func(ctx context.Context, req *http.Request) (int, error) {
					sent++
					body, e := io.ReadAll(req.Body)
					if e != nil {
						return 0, e
					}
					if !bytes.Contains(body, []byte("formerly public")) {
						return 0, errors.New("missing public source")
					}
					// A peer accepted the bytes and local visibility changed before the worker
					// received a reply. No SQL lock may prevent the independent transaction.
					tx, e := x.db.BeginTx(ctx, nil)
					if e != nil {
						return 0, e
					}
					defer tx.Rollback()
					if _, e = tx.ExecContext(ctx, `SET LOCAL lock_timeout='300ms'`); e != nil {
						return 0, e
					}
					if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_federation_deliveries WHERE id=$1 FOR UPDATE`, source); e != nil {
						return 0, e
					}
					if _, e = tx.ExecContext(ctx, `UPDATE owned_text_posts SET visibility=3,content='Synthetic private replacement',updated_at=clock_timestamp() WHERE id=$1`, post); e != nil {
						return 0, e
					}
					if e = tx.Commit(); e != nil {
						return 0, e
					}
					return 0, errors.New("synthetic response lost after acceptance")
				})
				if n, e := x.r.ProcessDeliveries(x.ctx, transport, 1); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				if state, _, _ := deliveryRow(t, x.db, source); state != "cancelled" {
					t.Fatal(state)
				}
				if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 1 {
					t.Fatal(n, e)
				}
			} else {
				if _, e := x.db.Exec(`INSERT INTO owned_federation_dispatch_receipts(source_delivery_id,policy_id,dispatch_token)VALUES($1,$2,$3)`, source, x.config.CleanupPolicyID, authorityUUID(t)); e == nil {
					t.Fatal("fabricated receipt for undispatched source")
				}
				hideCleanupPost(t, x, post)
			}
			capture := &federationCapture{}
			if _, e := x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil {
				t.Fatal(e)
			}
			var receipts, cleanups int
			if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_dispatch_receipts WHERE source_delivery_id=$1`, source).Scan(&receipts); e != nil {
				t.Fatal(e)
			}
			if e := x.db.QueryRow(`SELECT count(*) FROM owned_federation_deliveries WHERE cleanup_receipt_id=$1`, source).Scan(&cleanups); e != nil {
				t.Fatal(e)
			}
			if mode == "ambiguous" {
				if receipts != 1 || cleanups != 1 || len(capture.requests) != 1 || sent != 1 {
					t.Fatal(receipts, cleanups, len(capture.requests), sent)
				}
				var a f.Activity
				if e := json.Unmarshal(capture.requests[0], &a); e != nil || a.Type != "Delete" || bytes.Contains(capture.requests[0], []byte("Synthetic")) {
					t.Fatal(a, e)
				}
				var object map[string]string
				if e := json.Unmarshal(a.Object, &object); e != nil || len(object) != 2 || object["type"] != "Tombstone" || object["id"] != x.config.Origin+"/activitypub/objects/"+post {
					t.Fatal(object, e)
				}
			} else if receipts != 0 || cleanups != 0 || len(capture.requests) != 0 {
				t.Fatal("no historical authority", mode, receipts, cleanups, len(capture.requests))
			}
			if n, e := x.r.ReconcilePosts(x.ctx, 20); e != nil || n != 0 {
				t.Fatal("replayed reconciliation", n, e)
			}
		})
	}
	t.Run("insert-wait-rollback", assertCleanupReceiptCommitFenceAfterInsertWait)
}

func TestPGFederationCleanupReceiptReplayBoundsAndTampering(t *testing.T) {
	x := newFederationFixture(t, false)
	configureCleanup(t, x, true)
	post, source := queueCleanupSource(t, x)
	if _, e := x.r.ProcessDeliveries(x.ctx, &federationCapture{}, 20); e != nil {
		t.Fatal(e)
	}
	var receiptBefore string
	if e := x.db.QueryRow(`SELECT row_to_json(c)::text FROM owned_federation_dispatch_receipts c WHERE source_delivery_id=$1`, source).Scan(&receiptBefore); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{
		`UPDATE owned_federation_dispatch_receipts SET expires_at=expires_at+interval '1 second' WHERE source_delivery_id=$1`,
		`UPDATE owned_federation_dispatch_receipts SET object_uri='https://local.example.test/activitypub/objects/forged' WHERE source_delivery_id=$1`,
		`DELETE FROM owned_federation_dispatch_receipts WHERE source_delivery_id=$1`,
	} {
		if _, e := x.db.Exec(statement, source); e == nil {
			t.Fatal("receipt tampering accepted", statement)
		}
	}
	hideCleanupPost(t, x, post)
	var cleanup int64
	var bounded bool
	if e := x.db.QueryRow(`SELECT d.id,d.expires_at=c.expires_at AND c.expires_at<=s.expires_at AND d.max_attempts=s.max_attempts FROM owned_federation_deliveries d JOIN owned_federation_dispatch_receipts c ON c.source_delivery_id=d.cleanup_receipt_id JOIN owned_federation_deliveries s ON s.id=c.source_delivery_id WHERE d.cleanup_receipt_id=$1`, source).Scan(&cleanup, &bounded); e != nil || !bounded {
		t.Fatal(bounded, e)
	}
	for _, statement := range []string{
		`UPDATE owned_federation_deliveries SET expires_at=expires_at+interval '1 second' WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET cleanup_receipt_id=NULL WHERE id=$1`,
		`UPDATE owned_federation_deliveries SET payload=convert_to('{"private":"Synthetic hidden"}','UTF8') WHERE id=$1`,
		`DELETE FROM owned_federation_deliveries WHERE id=$1`,
	} {
		if _, e := x.db.Exec(statement, cleanup); e == nil {
			t.Fatal("cleanup tampering accepted", statement)
		}
	}
	capture := &federationCapture{codes: []int{503, 202}}
	if n, e := x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	x.clock.Add(61)
	reopened, e := federationpg.New(x.db, x.config, x.keys, x.guard)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := reopened.ProcessDeliveries(x.ctx, capture, 20); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if len(capture.requests) != 2 || !bytes.Equal(capture.requests[0], capture.requests[1]) || bytes.Contains(capture.requests[0], []byte("Synthetic")) {
		t.Fatal("unstable or contentful cleanup")
	}
	if state, attempts, _ := deliveryRow(t, x.db, cleanup); state != "sent" || attempts != 2 {
		t.Fatal(state, attempts)
	}
	// Retries and hidden edits cannot renew the historical deadline or receipt.
	query(t, x.db, `UPDATE owned_text_posts SET content='Synthetic later hidden edit',updated_at=clock_timestamp() WHERE id=$1`, post)
	if _, e = reopened.ReconcilePosts(x.ctx, 20); e != nil {
		t.Fatal(e)
	}
	if n, e := reopened.ProcessDeliveries(x.ctx, capture, 20); e != nil || n != 0 {
		t.Fatal("cleanup replay", n, e)
	}
	var receiptAfter string
	if e = x.db.QueryRow(`SELECT row_to_json(c)::text FROM owned_federation_dispatch_receipts c WHERE source_delivery_id=$1`, source).Scan(&receiptAfter); e != nil || receiptBefore != receiptAfter {
		t.Fatal("renewed receipt", e)
	}
	if _, e = x.db.Exec(`UPDATE owned_federation_deliveries SET status='pending' WHERE id=$1`, cleanup); e == nil {
		t.Fatal("terminal cleanup revived")
	}
	if _, e = x.db.Exec(`DELETE FROM owned_federation_deliveries WHERE id=$1`, cleanup); e == nil {
		t.Fatal("terminal cleanup reservation released")
	}
}

func assertCleanupReceiptCommitFenceAfterInsertWait(t *testing.T) {
	t.Helper()
	x := newFederationFixture(t, false)
	configureCleanup(t, x, true)
	post, source := queueCleanupSource(t, x)
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
	if _, e = hold.ExecContext(ctx, `LOCK TABLE owned_federation_dispatch_receipts IN SHARE MODE`); e != nil {
		t.Fatal(e)
	}
	capture := &federationCapture{}
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessDeliveries(ctx, capture, 1); done <- e }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_dispatch_receipts")
	// This generation-only probe isolates the new receipt-insert wait. Ordinary
	// grant/session writers still use the canonical account-first lock protocol.
	syntheticFederationHistoryTransition(t, ctx, hold, "delivery", x.actor)
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	var receipts int
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_dispatch_receipts WHERE source_delivery_id=$1`, source).Scan(&receipts); e != nil {
		t.Fatal(e)
	}
	if state, _, _ := deliveryRow(t, x.db, source); state != "cancelled" || receipts != 0 || len(capture.requests) != 0 {
		t.Fatal("stale receipt observation committed", state, receipts, len(capture.requests))
	}
	hideCleanupPost(t, x, post)
	if _, e = x.r.ProcessDeliveries(ctx, capture, 20); e != nil || len(capture.requests) != 0 {
		t.Fatal("cancelled observation created cleanup authority", e, len(capture.requests))
	}
}

func TestPGFederationCleanupCurrentAuthorityAndBindingFences(t *testing.T) {
	for _, change := range []string{"session", "grant-aba", "member-aba", "owner-aba", "publisher-name-aba", "disabled", "enabled-aba", "block-aba", "local-key", "remote-key", "remote-inbox", "remote-incarnation", "reservation", "policy-change", "policy-retired", "policy-off"} {
		t.Run(change, func(t *testing.T) {
			x := newFederationFixture(t, false)
			configureCleanup(t, x, true)
			post, _ := queueCleanupSource(t, x)
			if _, e := x.r.ProcessDeliveries(x.ctx, &federationCapture{}, 20); e != nil {
				t.Fatal(e)
			}
			hideCleanupPost(t, x, post)
			switch change {
			case "session":
				if e := x.issuer.Revoke(x.ctx, x.token, "current"); e != nil {
					t.Fatal(e)
				}
			case "grant-aba":
				query(t, x.db, `UPDATE owned_authority_grants SET deny=true WHERE account_id=$1 AND pattern='publishers.*'`, x.actor)
				query(t, x.db, `UPDATE owned_authority_grants SET deny=false WHERE account_id=$1 AND pattern='publishers.*'`, x.actor)
			case "member-aba":
				query(t, x.db, `UPDATE owned_publisher_members SET revoked_at=clock_timestamp() WHERE publisher_id=$1`, x.publisher)
				query(t, x.db, `UPDATE owned_publisher_members SET revoked_at=NULL WHERE publisher_id=$1`, x.publisher)
			case "owner-aba":
				other := authorityUUID(t)
				if e := authoritypg.SeedSynthetic(x.ctx, x.db, other, "synthetic_cleanup_owner_"+other, x.config.Clock()); e != nil {
					t.Fatal(e)
				}
				query(t, x.db, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, x.publisher, other)
				query(t, x.db, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, x.publisher, x.actor)
			case "publisher-name-aba":
				query(t, x.db, `UPDATE owned_publishers SET name='renamed_'||name WHERE id=$1`, x.publisher)
				query(t, x.db, `UPDATE owned_publishers SET name=$2 WHERE id=$1`, x.publisher, x.name)
			case "disabled":
				query(t, x.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, x.publisher)
			case "enabled-aba":
				query(t, x.db, `UPDATE owned_federation_publishers SET enabled=false WHERE publisher_id=$1`, x.publisher)
				query(t, x.db, `UPDATE owned_federation_publishers SET enabled=true WHERE publisher_id=$1`, x.publisher)
			case "block-aba":
				query(t, x.db, `INSERT INTO owned_federation_blocks(publisher_id,actor_uri,created_at) VALUES($1,$2,clock_timestamp())`, x.publisher, x.keys.remote.Actor)
				query(t, x.db, `DELETE FROM owned_federation_blocks WHERE publisher_id=$1`, x.publisher)
			case "local-key":
				key, e := rsa.GenerateKey(rand.Reader, 2048)
				if e != nil {
					t.Fatal(e)
				}
				x.keys.locals[x.publisher] = key
			case "remote-key":
				key, e := rsa.GenerateKey(rand.Reader, 2048)
				if e != nil {
					t.Fatal(e)
				}
				x.keys.remote.PublicKey = &key.PublicKey
			case "remote-inbox":
				x.keys.remote.Inbox += "/replacement"
			case "remote-incarnation":
				x.keys.remote.Incarnation = authorityUUID(t)
			case "reservation":
				query(t, x.db, federationpg.DiscoverySchema)
				query(t, x.db, federationpg.PublicAdmissionSchema)
				query(t, x.db, `INSERT INTO owned_federation_public_policies(origin,incarnation,created_at)VALUES('https://remote.example.test',$1,clock_timestamp()) ON CONFLICT DO NOTHING`, authorityUUID(t))
				pem, err := f.PEM(x.remoteKey)
				if err != nil {
					t.Fatal(err)
				}
				fingerprint, err := f.KeyFingerprint(&x.remoteKey.PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				query(t, x.db, `INSERT INTO owned_federation_public_keys(actor_uri,resource,inbox_uri,key_id,fingerprint,public_pem,incarnation,origin,policy_incarnation,admitted_at,revoked_at) SELECT $1,$2,$3,$4,$5,$6,$7,origin,incarnation,clock_timestamp(),clock_timestamp() FROM owned_federation_public_policies WHERE origin='https://remote.example.test'`, x.keys.remote.Actor, "acct:"+x.name+"@remote.example.test", x.keys.remote.Inbox, x.keys.remote.KeyID, fingerprint, pem, authorityUUID(t))
			case "policy-change":
				x.config.AllowedOrigins = append(x.config.AllowedOrigins, "https://other.example.test")
			case "policy-off":
				x.config.CleanupPolicyID = ""
			case "policy-retired":
				old := x.r
				x.config.CleanupPolicyID = authorityUUID(t)
				var e error
				x.r, e = federationpg.New(x.db, x.config, x.keys, x.guard)
				if e != nil {
					t.Fatal(e)
				}
				if e = x.r.ActivateCleanupPolicy(x.ctx, x.actor, x.publisher); e != nil {
					t.Fatal(e)
				}
				if e = old.ActivateCleanupPolicy(x.ctx, x.actor, x.publisher); !errors.Is(e, f.ErrForbidden) {
					t.Fatal("retired incarnation reused", e)
				}
			}
			reopened, e := federationpg.New(x.db, x.config, x.keys, x.guard)
			if e != nil {
				t.Fatal(e)
			}
			capture := &federationCapture{}
			if _, e = reopened.ProcessDeliveries(x.ctx, capture, 20); e != nil {
				t.Fatal(e)
			}
			if len(capture.requests) != 0 {
				t.Fatal("old receipt crossed current fence", change)
			}
		})
	}
}

func TestPGFederationCleanupActivationRequiresExplicitRootGrant(t *testing.T) {
	for _, mode := range []string{"unbound", "grantless-superuser", "scoped", "delegated", "forged", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			x := newFederationFixture(t, false)
			x.config.CleanupPolicyID = authorityUUID(t)
			if mode == "grantless-superuser" {
				query(t, x.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, x.actor)
			} else {
				query(t, x.db, `INSERT INTO owned_authority_grants(account_id,pattern)VALUES($1,$2)`, x.actor, f.CleanupPermission)
			}
			r, e := federationpg.New(x.db, x.config, x.keys, x.guard)
			if e != nil {
				t.Fatal(e)
			}
			ctx := x.ctx
			switch mode {
			case "unbound":
				ctx = context.Background()
			case "forged":
				ctx = x.guard.Bind(context.Background(), authority.Principal{Account: authority.Account{ID: x.actor}})
			case "scoped", "delegated":
				scopes := []string{"publishers.*"}
				if mode == "delegated" {
					scopes = []string{"*"}
				}
				pair, e := x.issuer.IssueSession(x.ctx, x.actor, authority.SessionOptions{Label: "Synthetic cleanup proof", Scopes: scopes})
				if e != nil {
					t.Fatal(e)
				}
				principal, e := x.verifier.Authenticate(x.ctx, pair.Token)
				if e != nil {
					t.Fatal(e)
				}
				if mode == "delegated" {
					parent, e := x.verifier.Authenticate(x.ctx, x.token)
					if e != nil {
						t.Fatal(e)
					}
					query(t, x.db, `UPDATE owned_authority_sessions SET parent_id=$2 WHERE id=$1`, principal.Session.ID, parent.Session.ID)
					principal, e = x.verifier.Authenticate(x.ctx, pair.Token)
					if e != nil {
						t.Fatal(e)
					}
				}
				ctx = x.guard.Bind(context.Background(), principal)
			}
			e = r.ActivateCleanupPolicy(ctx, x.actor, x.publisher)
			var n int
			if err := x.db.QueryRow(`SELECT count(*) FROM owned_federation_cleanup_policies WHERE publisher_id=$1`, x.publisher).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if mode == "allowed" {
				if e != nil || n != 1 {
					t.Fatal(e, n)
				}
				if e = r.ActivateCleanupPolicy(ctx, x.actor, x.publisher); e != nil {
					t.Fatal("activation replay", e)
				}
			} else if e == nil || n != 0 {
				t.Fatal("activation authority bypass", mode, e, n)
			}
		})
	}
}

func TestPGFederationCleanupActivationRechecksAfterWait(t *testing.T) {
	for _, change := range []string{"revoked", "expired"} {
		t.Run(change, func(t *testing.T) {
			x := newFederationFixture(t, false)
			configureCleanup(t, x, true)
			if change == "expired" {
				query(t, x.db, `UPDATE owned_authority_grants SET expires_at=$2 WHERE account_id=$1 AND pattern=$3`, x.actor, x.config.Clock().Add(30*time.Second), f.CleanupPermission)
			}
			x.config.CleanupPolicyID = authorityUUID(t)
			r, e := federationpg.New(x.db, x.config, x.keys, x.guard)
			if e != nil {
				t.Fatal(e)
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
			table := "owned_authority_accounts"
			statement := `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`
			if change == "expired" {
				table = "owned_federation_cleanup_policies"
				statement = `SELECT id FROM owned_federation_cleanup_policies WHERE publisher_id=$1 FOR UPDATE`
			}
			target := x.actor
			if change == "expired" {
				target = x.publisher
			}
			if _, e = hold.ExecContext(ctx, statement, target); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- r.ActivateCleanupPolicy(ctx, x.actor, x.publisher) }()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, table)
			if change == "revoked" {
				if _, e = hold.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1 AND pattern=$2`, x.actor, f.CleanupPermission); e != nil {
					t.Fatal(e)
				}
			} else {
				x.clock.Add(31)
			}
			if e = hold.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("activation survived authority wait", change)
			}
			var n int
			if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_cleanup_policies WHERE publisher_id=$1 AND id=$2`, x.publisher, x.config.CleanupPolicyID).Scan(&n); e != nil || n != 0 {
				t.Fatal("failed activation committed", n, e)
			}
		})
	}
}

func TestPGFederationCleanupLeaseDeadlineAndRevocation(t *testing.T) {
	for _, mode := range []string{"deadline", "lease", "session-during-send"} {
		t.Run(mode, func(t *testing.T) {
			x := newFederationFixture(t, false)
			configureCleanup(t, x, true)
			if mode == "deadline" {
				// A scheduled policy boundary caps the original normal intent as well as
				// its receipt. Expiry is observed using the database clock, not fake Now.
				query(t, x.db, `INSERT INTO owned_authority_grants(account_id,pattern,expires_at)VALUES($1,'unrelated.synthetic',clock_timestamp()+interval '2 seconds')`, x.actor)
				if e := x.r.Settings(x.ctx, x.actor, x.publisher, f.Settings{Enabled: true, Manual: false}); e != nil {
					t.Fatal(e)
				}
			}
			post, source := queueCleanupSource(t, x)
			if _, e := x.r.ProcessDeliveries(x.ctx, &federationCapture{}, 20); e != nil {
				t.Fatal(e)
			}
			hideCleanupPost(t, x, post)
			var id int64
			var expires time.Time
			if e := x.db.QueryRow(`SELECT id,expires_at FROM owned_federation_deliveries WHERE cleanup_receipt_id=$1`, source).Scan(&id, &expires); e != nil {
				t.Fatal(e)
			}
			capture := &federationCapture{}
			if mode == "deadline" {
				time.Sleep(time.Until(expires) + 25*time.Millisecond)
				if n, e := x.r.ProcessDeliveries(x.ctx, capture, 20); e != nil || n != 0 || len(capture.requests) != 0 {
					t.Fatal("expired cleanup dispatched", n, e)
				}
				if state, _, _ := deliveryRow(t, x.db, id); state != "cancelled" {
					t.Fatal(state)
				}
				return
			}
			var first []byte
			transport := deliverySendFunc(func(ctx context.Context, req *http.Request) (int, error) {
				var e error
				first, e = io.ReadAll(req.Body)
				if e != nil {
					return 0, e
				}
				if mode == "lease" {
					_, e = x.db.ExecContext(ctx, `UPDATE owned_federation_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id)
				} else {
					e = x.issuer.Revoke(ctx, x.token, "current")
				}
				return 202, e
			})
			if n, e := x.r.ProcessDeliveries(x.ctx, transport, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			state, attempts, _ := deliveryRow(t, x.db, id)
			if mode == "session-during-send" {
				if state != "cancelled" || attempts != 1 {
					t.Fatal("late success crossed session revoke", state, attempts)
				}
				return
			}
			if state != "inflight" || attempts != 1 {
				t.Fatal("expired lease acknowledged", state, attempts)
			}
			if n, e := x.r.ProcessDeliveries(x.ctx, capture, 1); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			if len(capture.requests) != 1 || !bytes.Equal(first, capture.requests[0]) {
				t.Fatal("reclaim changed cleanup action")
			}
			if state, attempts, _ = deliveryRow(t, x.db, id); state != "sent" || attempts != 2 {
				t.Fatal(state, attempts)
			}
		})
	}
}
