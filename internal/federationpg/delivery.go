// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

// ReconcilePosts turns committed canonical post changes into durable deliveries.
// Selection and fanout commit together. Polling is explicit, bounded, and never
// started by New. No remote transport is called here.
func (r *Repository) ReconcilePosts(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	if r.keys == nil {
		return 0, f.ErrUnavailable
	}
	public := `pub.type=0 AND pub.deleted_at IS NULL AND pub.shadowbanned_at IS NULL AND NOT a.disabled AND p.shadowbanned_at IS NULL AND p.realm_id IS NULL AND p.visibility=0 AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND COALESCE(p.published_at<=$1,false)`
	rows, e := r.db.QueryContext(ctx, `SELECT p.id::text FROM owned_text_posts p JOIN owned_federation_publishers f ON f.publisher_id=p.publisher_id AND f.enabled JOIN owned_publishers pub ON pub.id=p.publisher_id AND pub.type=0 JOIN owned_authority_accounts a ON a.id=pub.account_id LEFT JOIN owned_federation_post_state s ON s.post_id=p.id WHERE (s.post_id IS NULL AND (`+public+`)) OR (s.post_id IS NOT NULL AND (s.source_updated_at<>p.updated_at OR s.source_audit_id<>COALESCE((SELECT max(audit.id)FROM owned_text_post_audit audit WHERE audit.post_id=p.id),0) OR s.public<>(`+public+`))) ORDER BY p.updated_at,p.id LIMIT $2`, r.config.Clock(), limit)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	n := 0
	for _, id := range ids {
		changed, e := r.reconcilePost(ctx, id)
		if e != nil {
			return n, e
		}
		if changed {
			n++
		}
	}
	return n, nil
}
func (r *Repository) reconcilePost(ctx context.Context, id string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var pid string
	if e = tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1`, id).Scan(&pid); e != nil {
		return false, mapped(e)
	}
	p, e := r.withdrawalPublisher(ctx, tx, pid)
	if e != nil {
		return false, e
	}
	var lock string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, p.owner).Scan(&lock); e != nil {
		return false, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`, pid).Scan(&lock); e != nil {
		return false, mapped(e)
	}
	if _, e = r.withdrawalPublisher(ctx, tx, pid); e != nil {
		return false, e
	}
	var updated time.Time
	var sourceAudit int64
	var visible bool
	e = tx.QueryRowContext(ctx, `SELECT updated_at,realm_id IS NULL AND visibility=0 AND deleted_at IS NULL AND shadowbanned_at IS NULL AND drafted_at IS NULL AND COALESCE(published_at<=$2,false),COALESCE((SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=owned_text_posts.id),0) FROM owned_text_posts WHERE id=$1 FOR UPDATE`, id, r.config.Clock()).Scan(&updated, &visible, &sourceAudit)
	if e != nil {
		return false, e
	}
	public, e := publisherPublic(ctx, tx, pid)
	if e != nil {
		return false, e
	}
	visible = visible && public
	var oldUpdated time.Time
	var wasPublic bool
	var revision, oldAudit int64
	e = tx.QueryRowContext(ctx, `SELECT source_updated_at,public,revision,source_audit_id FROM owned_federation_post_state WHERE post_id=$1`, id).Scan(&oldUpdated, &wasPublic, &revision, &oldAudit)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	exists := e == nil
	if exists && updated.Equal(oldUpdated) && visible == wasPublic && sourceAudit == oldAudit {
		return false, nil
	}
	revision++
	if !visible && wasPublic {
		if e = r.enqueueWithdrawals(ctx, tx, p, id); e != nil {
			return false, e
		}
	}
	if visible {
		note, _, _, e := r.note(ctx, tx, id)
		if e != nil {
			return false, e
		}
		kind := "Create"
		if wasPublic {
			kind = "Update"
		}
		// First Create shares the stable ID returned by the public outbox.
		aid := r.objectID(id) + "/activity"
		if revision > 1 {
			aid = fmt.Sprintf("%s/activities/%d", r.objectID(id), revision)
		}
		a := f.Activity{Context: f.Context, ID: aid, Type: kind, Actor: r.actorID(p), Object: marshal(note), To: []string{f.Public}, CC: []string{r.actorID(p) + "/followers"}}
		rows, e := tx.QueryContext(ctx, `SELECT x.actor_uri FROM owned_federation_relationships x WHERE x.publisher_id=$1 AND x.direction='in' AND x.status='accepted' AND NOT EXISTS(SELECT 1 FROM owned_federation_blocks b WHERE b.publisher_id=x.publisher_id AND b.actor_uri=x.actor_uri) ORDER BY x.actor_uri LIMIT 1001`, pid)
		if e != nil {
			return false, e
		}
		peers := []string{}
		for rows.Next() {
			var remote string
			if e = rows.Scan(&remote); e != nil {
				rows.Close()
				return false, e
			}
			peers = append(peers, remote)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if len(peers) > 1000 {
			return false, f.ErrUnavailable
		}
		for _, peer := range peers {
			k, e := r.remoteWithQuery(ctx, tx, peer)
			if e != nil {
				return false, e
			}
			if e = r.enqueue(ctx, tx, p, k, a, id, &updated); e != nil {
				return false, e
			}
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_post_state(post_id,source_updated_at,public,revision,source_audit_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(post_id) DO UPDATE SET source_updated_at=EXCLUDED.source_updated_at,public=EXCLUDED.public,revision=EXCLUDED.revision,source_audit_id=EXCLUDED.source_audit_id`, id, updated, visible, revision, sourceAudit)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

type delivery struct {
	cleanup                                  sql.NullInt64
	quote                                    sql.NullString
	peerSecurityEpoch                        sql.NullInt64
	audit                                    sql.NullInt64
	id                                       int64
	publisher, remote, inbox, activity, kind string
	body                                     []byte
	post                                     sql.NullString
	updated                                  sql.NullTime
	attempts                                 int
	token                                    string
	intent                                   deliveryIntent
	binding, fingerprint                     string
	maxAttempts                              int
	lease, expires                           time.Time
	peerEpoch                                int64
}

func (r *Repository) claim(ctx context.Context) (delivery, error) {
	var d delivery
	token, e := newID()
	if e != nil {
		return d, e
	}
	// No migration manufactures permission for legacy jobs. Original budgets and
	// deadlines survive configuration changes and crashes.
	_, e = r.db.ExecContext(ctx, `WITH expired AS(SELECT id FROM owned_federation_deliveries WHERE status IN('pending','inflight') AND (origin_intent IS NULL OR expires_at<=clock_timestamp() OR (attempts>=LEAST(max_attempts,$1) AND (lease_until IS NULL OR lease_until<=clock_timestamp()))) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 100) UPDATE owned_federation_deliveries d SET status=CASE WHEN origin_intent IS NULL OR expires_at<=clock_timestamp() THEN 'cancelled' ELSE 'dead' END,finished_at=clock_timestamp(),lease_token=NULL,lease_until=NULL FROM expired WHERE d.id=expired.id`, r.config.MaxAttempts)
	if e != nil {
		return d, e
	}
	var raw []byte
	e = r.db.QueryRowContext(ctx, `WITH candidate AS(SELECT d.id FROM owned_federation_deliveries d WHERE d.origin_intent IS NOT NULL AND d.expires_at>clock_timestamp() AND d.attempts<LEAST(d.max_attempts,$1) AND ((d.status='pending' AND d.next_attempt<=$2) OR (d.status='inflight' AND d.lease_until<=clock_timestamp())) AND NOT EXISTS(SELECT 1 FROM owned_federation_deliveries prior WHERE prior.publisher_id=d.publisher_id AND prior.actor_uri=d.actor_uri AND prior.id<d.id AND prior.status IN('pending','inflight')) ORDER BY d.next_attempt,d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1) UPDATE owned_federation_deliveries d SET status='inflight',attempts=attempts+1,lease_token=$3,lease_until=LEAST(clock_timestamp()+($4::bigint*interval '1 microsecond'),expires_at),dispatch_started_at=NULL FROM candidate c WHERE d.id=c.id RETURNING d.id,d.publisher_id::text,d.actor_uri,d.inbox_uri,d.activity_id,d.activity_type,d.payload,d.post_id::text,d.post_updated_at,d.post_audit_id,d.attempts,d.origin_intent,d.remote_binding,d.local_fingerprint,d.max_attempts,d.lease_until,d.expires_at,d.peer_epoch,d.peer_security_epoch,d.quote_id::text,d.cleanup_receipt_id`, r.config.MaxAttempts, r.config.Clock(), token, (2*r.config.DeliveryTimeout).Microseconds()).Scan(&d.id, &d.publisher, &d.remote, &d.inbox, &d.activity, &d.kind, &d.body, &d.post, &d.updated, &d.audit, &d.attempts, &raw, &d.binding, &d.fingerprint, &d.maxAttempts, &d.lease, &d.expires, &d.peerEpoch, &d.peerSecurityEpoch, &d.quote, &d.cleanup)
	d.token = token
	if e == nil && json.Unmarshal(raw, &d.intent) != nil {
		return d, f.ErrUnavailable
	}
	return d, e
}

// ProcessDeliveries performs at most limit attempts, one bounded transport call
// each, without redirects or discovery. Nil keys/transport makes no queue writes.
func (r *Repository) ProcessDeliveries(ctx context.Context, transport f.Transport, limit int) (int, error) {
	if transport == nil || r.keys == nil {
		return 0, f.ErrUnavailable
	}
	if _, ok := r.guard.(storedDiscoveryGuard); !ok {
		return 0, f.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	n := 0
	for n < limit {
		d, e := r.claim(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			return n, nil
		}
		if e != nil {
			return n, e
		}
		if e = r.deliver(ctx, transport, d); e != nil {
			return n, e
		}
		n++
	}
	return n, nil
}
func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	d := time.Minute * time.Duration(1<<uint(attempt-1))
	if d > 6*time.Hour {
		return 6 * time.Hour
	}
	return d
}

// observeDelivery commits one fresh observation before dispatch or records a
// fenced result afterward. It never calls the transport. Its locks protect only
// observation/commit, not remote effects after commit.
func (r *Repository) observeDelivery(ctx context.Context, d delivery, result string, prepared **http.Request) error {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	finish := func(status string) error {
		var finished any
		if status != "pending" {
			finished = r.config.Clock()
		}
		_, e := tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status=$3,next_attempt=$4,finished_at=$5,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='inflight' AND attempts=$6 AND lease_until>clock_timestamp()`, d.id, d.token, status, r.config.Clock().Add(retryDelay(d.attempts)), finished, d.attempts)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	// Account locks precede publisher/domain and delivery locks. No request is
	// reconstructed from a historical grant; the recorded session is only checked.
	var lock string
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, d.intent.Requester, d.intent.Owner)
	if e != nil {
		return e
	}
	for rows.Next() {
		if e = rows.Scan(&lock); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`, d.publisher).Scan(&lock); e != nil {
		return e
	}
	var p publisher
	if d.cleanup.Valid {
		p, e = r.withdrawalPublisher(ctx, tx, d.publisher)
	} else {
		p, e = r.publisher(ctx, tx, d.publisher, true)
	}
	if deliveryDenial(e) {
		return finish("cancelled")
	}
	if e != nil {
		return e
	}
	if allowed, err := r.deliveryAuthorityAllowed(ctx, tx, p, d); err != nil {
		return err
	} else if !allowed {
		return finish("cancelled")
	}
	var epoch int64
	epochQuery := `SELECT COALESCE((SELECT revision FROM owned_federation_delivery_peer_epochs WHERE publisher_id=$1 AND actor_uri=$2),0)`
	wantEpoch := d.peerEpoch
	if d.quote.Valid {
		// Independent quote consent is not a Follow grant. Security changes
		// (including block/unblock ABA) still permanently cancel old consent.
		if !d.peerSecurityEpoch.Valid {
			return finish("cancelled")
		}
		epochQuery = `SELECT COALESCE((SELECT security_revision FROM owned_federation_delivery_peer_epochs WHERE publisher_id=$1 AND actor_uri=$2),0)`
		wantEpoch = d.peerSecurityEpoch.Int64
	}
	if e = tx.QueryRowContext(ctx, epochQuery, d.publisher, d.remote).Scan(&epoch); e != nil {
		return e
	}
	if epoch != wantEpoch {
		return finish("cancelled")
	}
	b, e := r.blocked(ctx, tx, p.id, d.remote)
	if e != nil {
		return e
	}
	if b {
		return finish("cancelled")
	}
	if d.post.Valid {
		var updated time.Time
		var audit int64
		var visible bool
		e = tx.QueryRowContext(ctx, `SELECT updated_at,realm_id IS NULL AND visibility=0 AND deleted_at IS NULL AND shadowbanned_at IS NULL AND drafted_at IS NULL AND COALESCE(published_at<=$2,false),COALESCE((SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=owned_text_posts.id),0) FROM owned_text_posts WHERE id=$1 FOR UPDATE`, d.post.String, r.config.Clock()).Scan(&updated, &visible, &audit)
		if e != nil {
			return e
		}
		public, e := publisherPublic(ctx, tx, d.publisher)
		if e != nil {
			return e
		}
		visible = visible && public
		if d.kind != "Delete" && (audit != d.audit.Int64 || !updated.Equal(d.updated.Time) || !visible) || d.kind == "Delete" && visible {
			return finish("cancelled")
		}
		var follows bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in' AND status='accepted')`, p.id, d.remote).Scan(&follows); e != nil {
			return e
		}
		if !follows {
			return finish("cancelled")
		}
	}
	// Quote validation can lock admitted key rows. The reservation table lock
	// must come first so a concurrent key revocation cannot form an upgrade cycle.
	if _, e = lockDeliveryReservations(ctx, tx); e != nil {
		return e
	}
	quoteDelivery, quoteCurrent, e := r.quoteDeliveryCurrent(ctx, tx, p, d)
	if e != nil {
		return e
	}
	if quoteDelivery && !quoteCurrent {
		return finish("cancelled")
	}
	var lease string
	e = tx.QueryRowContext(ctx, `SELECT lease_token::text FROM owned_federation_deliveries WHERE id=$1 AND status='inflight' AND attempts=$2 AND lease_until>clock_timestamp() FOR UPDATE`, d.id, d.attempts).Scan(&lease)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if lease != d.token {
		return nil
	}
	// Drop superseded follow/accept messages instead of resurrecting old state.
	if !quoteDelivery && (d.kind == "Follow" || d.kind == "Accept" || d.kind == "Reject") {
		direction, status := "out", "pending"
		if d.kind == "Accept" {
			direction, status = "in", "accepted"
		} else if d.kind == "Reject" {
			direction, status = "in", "rejected"
		}
		var actual, follow string
		e = tx.QueryRowContext(ctx, `SELECT status,follow_id FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction=$3`, p.id, d.remote, direction).Scan(&actual, &follow)
		if errors.Is(e, sql.ErrNoRows) || e == nil && actual != status {
			return finish("cancelled")
		}
		if e != nil {
			return e
		}
		want := d.activity
		if d.kind != "Follow" {
			var a f.Activity
			if json.Unmarshal(d.body, &a) != nil {
				return finish("dead")
			}
			want, e = f.ObjectID(a.Object)
			if e != nil {
				return finish("dead")
			}
		}
		if want != follow {
			return finish("cancelled")
		}
	}
	k, e := r.remoteWithQuery(ctx, tx, d.remote)
	binding, bindingErr := deliveryBinding(k)
	if e != nil || bindingErr != nil || binding != d.binding || k.Inbox != d.inbox || !r.config.Allows(d.inbox) {
		return finish("dead")
	}
	signer, e := r.keys.LocalKey(ctx, p.id)
	fingerprint, fingerprintErr := localFingerprint(signer)
	if e != nil || fingerprintErr != nil || fingerprint != d.fingerprint {
		return finish("dead")
	}
	var a f.Activity
	if json.Unmarshal(d.body, &a) != nil || a.Actor != r.actorID(p) || a.ID != d.activity || a.Type != d.kind {
		return finish("dead")
	}
	var request *http.Request
	if result == "" {
		request, e = http.NewRequestWithContext(ctx, "POST", d.inbox, bytes.NewReader(d.body))
		if e != nil {
			return finish("dead")
		}
		if e = f.Sign(request, d.body, p.keyID, signer, r.config.Clock()); e != nil {
			return finish("dead")
		}
	}
	if quoteDelivery {
		_, current, e := r.quoteDeliveryCurrent(ctx, tx, p, d)
		if e != nil {
			return e
		}
		if !current {
			return finish("cancelled")
		}
	}
	if e = r.deliveryRemoteFence(ctx, tx, k); e != nil {
		return finish("cancelled")
	}
	// Fence the optional receipt policy before the final normal authority
	// observation; receipt insertion cannot add a policy wait after it.
	if !d.cleanup.Valid && d.post.Valid && (d.kind == "Create" || d.kind == "Update") {
		if _, e = r.cleanupPolicyCurrent(ctx, tx, d.publisher); e != nil {
			return e
		}
	}
	// Re-read current authority after every possible SQL/key/signing wait.
	if allowed, err := r.deliveryAuthorityAllowed(ctx, tx, p, d); err != nil {
		return err
	} else if !allowed {
		return finish("cancelled")
	}
	if result != "" {
		return finish(result)
	}
	// Receipt insertion can wait on table/index locks after the observation above.
	// Keep its provisional evidence behind a savepoint so a final denial cannot
	// commit a receipt for a dispatch that never passed the observation boundary.
	receiptCandidate := r.config.CleanupPolicyID != "" && !d.cleanup.Valid && d.post.Valid && (d.kind == "Create" || d.kind == "Update")
	if receiptCandidate {
		if _, e = tx.ExecContext(ctx, `SAVEPOINT federation_dispatch_receipt`); e != nil {
			return e
		}
	}
	res, e := tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET dispatch_started_at=clock_timestamp() WHERE id=$1 AND lease_token=$2 AND attempts=$3 AND status='inflight' AND lease_until>clock_timestamp() AND expires_at>clock_timestamp() AND dispatch_started_at IS NULL`, d.id, d.token, d.attempts)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return e
	}
	if e = r.recordDispatchReceipt(ctx, tx, d); e != nil {
		return e
	}
	if receiptCandidate {
		if allowed, err := r.deliveryAuthorityAllowed(ctx, tx, p, d); err != nil {
			return err
		} else if !allowed {
			if _, e = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT federation_dispatch_receipt`); e != nil {
				return e
			}
			return finish("cancelled")
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	*prepared = request
	return nil
}

func (r *Repository) deliver(ctx context.Context, transport f.Transport, d delivery) error {
	work, cancel := context.WithTimeout(ctx, r.config.DeliveryTimeout)
	defer cancel()
	work, leaseCancel := context.WithDeadline(work, d.lease)
	defer leaseCancel()
	var request *http.Request
	if e := r.observeDelivery(work, d, "", &request); e != nil {
		return e
	}
	if request == nil || work.Err() != nil {
		return nil
	}
	// All SQL transactions are closed. Revocation can commit now, including while
	// Send blocks. Bytes may already have left; a cancelled ledger cannot recall
	// them. A crash or expired lease can cause duplicate sends with the same ID.
	code, sendErr := transport.Send(work, request)
	status := "pending"
	if sendErr == nil && code >= 200 && code < 300 {
		status = "sent"
	} else if d.attempts >= d.maxAttempts || d.attempts >= r.config.MaxAttempts || sendErr == nil && code >= 300 && code < 500 && code != 408 && code != 429 {
		status = "dead"
	}
	if work.Err() != nil {
		return nil
	} // uncertain result is reclaimed after lease expiry
	return r.observeDelivery(work, d, status, nil)
}
func (r *Repository) Ready(ctx context.Context) error {
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_federation_publishers'),('owned_federation_peers'),('owned_federation_relationships'),('owned_federation_blocks'),('owned_federation_inbox'),('owned_federation_objects'),('owned_federation_post_state'),('owned_federation_deliveries'),('owned_federation_audit'),('owned_federation_quote_authorizations'),('owned_federation_dispatch_receipts'),('owned_federation_cleanup_policies'),('owned_federation_cleanup_epochs')) x(name)`).Scan(&ok)
	if e != nil || !ok {
		return f.ErrUnavailable
	}
	if _, e = r.db.ExecContext(ctx, `SELECT cleanup_receipt_id,quote_id,origin_intent,remote_binding,local_fingerprint,max_attempts,expires_at,peer_epoch,peer_security_epoch,dispatch_started_at FROM owned_federation_deliveries LIMIT 0;SELECT revision,security_revision FROM owned_federation_delivery_peer_epochs LIMIT 0;SELECT identity_handle,actor_uri,key_id,delivery_intent FROM owned_federation_publishers LIMIT 0;SELECT folded_name,publisher_id FROM owned_publisher_name_reservations LIMIT 0`); e != nil {
		return f.ErrUnavailable
	}
	if e = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='owned_federation_deliveries'::regclass AND conname='owned_federation_delivery_bounds' AND convalidated)`).Scan(&ok); e != nil || !ok {
		return f.ErrUnavailable
	}
	if e = r.db.QueryRowContext(ctx, `SELECT count(*)=19 FROM pg_trigger WHERE NOT tgisinternal AND tgenabled='O' AND tgname IN('owned_delivery_authority_grants','owned_delivery_authority_punishments','owned_delivery_authority_group_members','owned_delivery_publisher_members','owned_delivery_group_grants','owned_delivery_account','owned_delivery_publisher','owned_delivery_federation','owned_delivery_session','owned_delivery_session_delete','owned_delivery_relationships','owned_delivery_blocks','owned_delivery_immutable','owned_cleanup_policy_immutable','owned_cleanup_publisher','owned_cleanup_members','owned_cleanup_enabled','owned_dispatch_receipt_guard','owned_cleanup_delivery_guard')`).Scan(&ok); e != nil || !ok {
		return f.ErrUnavailable
	}
	return nil
}

// Historical cleanup has its own restricted receipt authority. Normal delivery
// always retains its original current-authority and no-revival checks.
func (r *Repository) deliveryAuthorityAllowed(ctx context.Context, tx *sql.Tx, p publisher, d delivery) (bool, error) {
	if d.post.Valid && d.kind == "Delete" && !d.cleanup.Valid {
		return false, nil
	}
	if d.cleanup.Valid {
		return r.cleanupAllowed(ctx, tx, p, d)
	}
	return r.deliveryAllowed(ctx, tx, p, d.intent)
}
