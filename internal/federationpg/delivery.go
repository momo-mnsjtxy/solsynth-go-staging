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
	rows, e := r.db.QueryContext(ctx, `SELECT p.id::text FROM owned_text_posts p JOIN owned_federation_publishers f ON f.publisher_id=p.publisher_id AND f.enabled JOIN owned_publishers pub ON pub.id=p.publisher_id AND pub.deleted_at IS NULL JOIN owned_authority_accounts a ON a.id=pub.account_id AND NOT a.disabled LEFT JOIN owned_federation_post_state s ON s.post_id=p.id WHERE (s.post_id IS NULL AND p.realm_id IS NULL AND p.visibility=0 AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=$1) OR (s.post_id IS NOT NULL AND (s.source_updated_at<>p.updated_at OR s.source_audit_id<>COALESCE((SELECT max(audit.id) FROM owned_text_post_audit audit WHERE audit.post_id=p.id),0) OR s.public<>(p.realm_id IS NULL AND p.visibility=0 AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND COALESCE(p.published_at<=$1,false)))) ORDER BY p.updated_at,p.id LIMIT $2`, r.config.Clock(), limit)
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
	p, e := r.publisher(ctx, tx, pid, true)
	if e != nil {
		return false, e
	}
	var lock string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 AND NOT disabled FOR UPDATE`, p.owner).Scan(&lock); e != nil {
		return false, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, pid).Scan(&lock); e != nil {
		return false, mapped(e)
	}
	if _, e = r.publisher(ctx, tx, pid, true); e != nil {
		return false, e
	}
	var updated time.Time
	var sourceAudit int64
	var visible bool
	e = tx.QueryRowContext(ctx, `SELECT updated_at,realm_id IS NULL AND visibility=0 AND deleted_at IS NULL AND drafted_at IS NULL AND COALESCE(published_at<=$2,false),COALESCE((SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=owned_text_posts.id),0) FROM owned_text_posts WHERE id=$1 FOR UPDATE`, id, r.config.Clock()).Scan(&updated, &visible, &sourceAudit)
	if e != nil {
		return false, e
	}
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
	if visible || wasPublic {
		var object any
		kind := "Delete"
		if visible {
			note, _, _, e := r.note(ctx, tx, id)
			if e != nil {
				return false, e
			}
			object = note
			kind = "Create"
			if wasPublic {
				kind = "Update"
			}
		} else {
			object = map[string]string{"id": r.objectID(id), "type": "Tombstone"}
		}
		// First Create shares the stable ID returned by the public outbox.
		aid := r.objectID(id) + "/activity"
		if revision > 1 {
			aid = fmt.Sprintf("%s/activities/%d", r.objectID(id), revision)
		}
		a := f.Activity{Context: f.Context, ID: aid, Type: kind, Actor: r.actorID(p), Object: marshal(object), To: []string{f.Public}, CC: []string{r.actorID(p) + "/followers"}}
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
			k, e := r.remote(ctx, peer)
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
	audit                                    sql.NullInt64
	id                                       int64
	publisher, remote, inbox, activity, kind string
	body                                     []byte
	post                                     sql.NullString
	updated                                  sql.NullTime
	attempts                                 int
	token                                    string
}

func (r *Repository) claim(ctx context.Context) (delivery, error) {
	var d delivery
	token, e := newID()
	if e != nil {
		return d, e
	}
	// Expired final attempts are made terminal even if the worker died after
	// sending and before recording its result. Delivery remains at-least-once.
	_, e = r.db.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='dead',finished_at=$1,lease_token=NULL,lease_until=NULL WHERE status IN('pending','inflight') AND attempts>=$2 AND (lease_until IS NULL OR lease_until<=$1)`, r.config.Clock(), r.config.MaxAttempts)
	if e != nil {
		return d, e
	}
	e = r.db.QueryRowContext(ctx, `WITH candidate AS (SELECT d.id FROM owned_federation_deliveries d WHERE d.attempts<$1 AND ((d.status='pending' AND d.next_attempt<=$2) OR (d.status='inflight' AND d.lease_until<=$2)) AND NOT EXISTS(SELECT 1 FROM owned_federation_deliveries prior WHERE prior.publisher_id=d.publisher_id AND prior.actor_uri=d.actor_uri AND prior.id<d.id AND prior.status IN('pending','inflight')) ORDER BY d.next_attempt,d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1) UPDATE owned_federation_deliveries d SET status='inflight',attempts=attempts+1,lease_token=$3,lease_until=$4 FROM candidate c WHERE d.id=c.id RETURNING d.id,d.publisher_id::text,d.actor_uri,d.inbox_uri,d.activity_id,d.activity_type,d.payload,d.post_id::text,d.post_updated_at,d.post_audit_id,d.attempts`, r.config.MaxAttempts, r.config.Clock(), token, r.config.Clock().Add(2*r.config.DeliveryTimeout)).Scan(&d.id, &d.publisher, &d.remote, &d.inbox, &d.activity, &d.kind, &d.body, &d.post, &d.updated, &d.audit, &d.attempts)
	d.token = token
	return d, e
}

// ProcessDeliveries performs at most limit attempts, one bounded transport call
// each, without redirects or discovery. Nil keys/transport makes no queue writes.
func (r *Repository) ProcessDeliveries(ctx context.Context, transport f.Transport, limit int) (int, error) {
	if transport == nil || r.keys == nil {
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
func (r *Repository) deliver(ctx context.Context, transport f.Transport, d delivery) error {
	ctx, cancel := context.WithTimeout(ctx, r.config.DeliveryTimeout)
	defer cancel()
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
		_, e := tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status=$3,next_attempt=$4,finished_at=$5,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='inflight'`, d.id, d.token, status, r.config.Clock().Add(retryDelay(d.attempts)), finished)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	// Hold canonical publisher/post locks until the bounded send completes, so a
	// visibility change, block or disable cannot commit before a stale send.
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1`, d.publisher).Scan(&owner)
	if e != nil {
		return e
	}
	var lock string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, owner).Scan(&lock); e != nil {
		return e
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`, d.publisher).Scan(&lock); e != nil {
		return e
	}
	p, e := r.publisher(ctx, tx, d.publisher, true)
	if errors.Is(e, f.ErrNotFound) {
		return finish("cancelled")
	}
	if e != nil {
		return e
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
		e = tx.QueryRowContext(ctx, `SELECT updated_at,realm_id IS NULL AND visibility=0 AND deleted_at IS NULL AND drafted_at IS NULL AND COALESCE(published_at<=$2,false),COALESCE((SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=owned_text_posts.id),0) FROM owned_text_posts WHERE id=$1 FOR UPDATE`, d.post.String, r.config.Clock()).Scan(&updated, &visible, &audit)
		if e != nil {
			return e
		}
		if audit != d.audit.Int64 || !updated.Equal(d.updated.Time) || d.kind != "Delete" && !visible {
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
	var lease string
	e = tx.QueryRowContext(ctx, `SELECT lease_token::text FROM owned_federation_deliveries WHERE id=$1 AND status='inflight' FOR UPDATE`, d.id).Scan(&lease)
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
	if d.kind == "Follow" || d.kind == "Accept" || d.kind == "Reject" {
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
	k, e := r.remote(ctx, d.remote)
	if e != nil || k.Inbox != d.inbox || !r.config.Allows(d.inbox) {
		return finish("dead")
	}
	signer, e := r.keys.LocalKey(ctx, p.id)
	if e != nil {
		return finish("dead")
	}
	var a f.Activity
	if json.Unmarshal(d.body, &a) != nil || a.Actor != r.actorID(p) || a.ID != d.activity || a.Type != d.kind {
		return finish("dead")
	}
	request, e := http.NewRequestWithContext(ctx, "POST", d.inbox, bytes.NewReader(d.body))
	if e != nil {
		return finish("dead")
	}
	if e = f.Sign(request, d.body, r.actorID(p)+"#main-key", signer, r.config.Clock()); e != nil {
		return finish("dead")
	}
	code, sendErr := transport.Send(ctx, request)
	status := "pending"
	if sendErr == nil && code >= 200 && code < 300 {
		status = "sent"
	} else if d.attempts >= r.config.MaxAttempts || sendErr == nil && (code >= 300 && code < 500 && code != 408 && code != 429) {
		status = "dead"
	}
	// The network deadline may have expired; rollback preserves the lease so a
	// later explicit worker call can recover safely, rather than committing with
	// a cancelled context or claiming that an uncertain network result succeeded.
	if ctx.Err() != nil {
		return nil
	}
	return finish(status)
}
func (r *Repository) Ready(ctx context.Context) error {
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES ('owned_federation_publishers'),('owned_federation_peers'),('owned_federation_relationships'),('owned_federation_blocks'),('owned_federation_inbox'),('owned_federation_objects'),('owned_federation_post_state'),('owned_federation_deliveries'),('owned_federation_audit')) x(name)`).Scan(&ok)
	if e != nil || !ok {
		return f.ErrUnavailable
	}
	return nil
}
