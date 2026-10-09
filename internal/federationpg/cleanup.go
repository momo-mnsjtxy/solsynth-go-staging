// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

func (r *Repository) cleanupPolicyBinding() string {
	origins := append([]string(nil), r.config.AllowedOrigins...)
	sort.Strings(origins)
	raw, _ := json.Marshal([]any{"federation-tombstone-v1", r.config.Origin, origins, r.config.MaxAttempts, r.config.DeliveryTimeout.String()})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ActivateCleanupPolicy requires a current root ordinary proof for the publisher
// owner, active manager membership, and the separate cleanup activation grant.
// It is never exposed through settings or called by New, Ready or a worker.
// Activate every changed transport policy under a fresh ID, even when restoring
// prior configuration. A retired incarnation can never be reused or backfilled.
func (r *Repository) ActivateCleanupPolicy(ctx context.Context, actor, publisherID string) error {
	if r == nil || !authority.ValidID(publisherID) || !authority.ValidID(r.config.CleanupPolicyID) {
		return f.ErrInvalid
	}
	tx, p, e := r.beginLocal(ctx, actor, publisherID)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if p.owner != actor {
		return f.ErrForbidden
	}
	if e = r.authorizeCleanupActivation(ctx, tx, actor, p.id); e != nil {
		return e
	}
	var binding string
	var retired sql.NullTime
	e = tx.QueryRowContext(ctx, `SELECT binding,retired_at FROM owned_federation_cleanup_policies WHERE publisher_id=$1 AND id=$2`, publisherID, r.config.CleanupPolicyID).Scan(&binding, &retired)
	if e == nil {
		if retired.Valid || binding != r.cleanupPolicyBinding() {
			return f.ErrForbidden
		}
		if e = r.authorizeCleanupActivation(ctx, tx, actor, p.id); e != nil {
			return e
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_cleanup_policies SET retired_at=clock_timestamp() WHERE publisher_id=$1 AND retired_at IS NULL`, publisherID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_cleanup_policies(publisher_id,id,binding,origin) VALUES($1,$2,$3,$4)`, publisherID, r.config.CleanupPolicyID, r.cleanupPolicyBinding(), r.config.Origin); e != nil {
		return e
	}
	if e = r.authorizeCleanupActivation(ctx, tx, actor, p.id); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) cleanupPolicyCurrent(ctx context.Context, tx *sql.Tx, publisherID string) (bool, error) {
	if r.config.CleanupPolicyID == "" {
		return false, nil
	}
	var binding string
	e := tx.QueryRowContext(ctx, `SELECT binding FROM owned_federation_cleanup_policies WHERE publisher_id=$1 AND id=$2 AND retired_at IS NULL FOR SHARE`, publisherID, r.config.CleanupPolicyID).Scan(&binding)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return e == nil && binding == r.cleanupPolicyBinding(), e
}

// recordDispatchReceipt runs only in the transaction committing a successful
// normal dispatch observation. The immutable receipt records possible external
// exposure, including a crash/ambiguous acceptance before acknowledgement.
func (r *Repository) recordDispatchReceipt(ctx context.Context, tx *sql.Tx, d delivery) error {
	if d.cleanup.Valid || !d.post.Valid || (d.kind != "Create" && d.kind != "Update") {
		return nil
	}
	current, e := r.cleanupPolicyCurrent(ctx, tx, d.publisher)
	if e != nil || !current {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_dispatch_receipts(source_delivery_id,policy_id,dispatch_token) VALUES($1,$2,$3) ON CONFLICT(source_delivery_id) DO NOTHING`, d.id, r.config.CleanupPolicyID, d.token)
	return e
}

type cleanupReceipt struct {
	actor, key, object, name string
	stamp                    []byte
}

// cleanupAllowed never authorizes publication. Only publisher/post visibility
// withdrawal is excluded; original requester/session ancestry, owner, grants,
// membership, enabled state, name, keys and policy history remain mandatory.
func (r *Repository) cleanupAllowed(ctx context.Context, tx *sql.Tx, p publisher, d delivery) (bool, error) {
	guard, ok := r.guard.(storedDiscoveryGuard)
	if !ok {
		return false, f.ErrUnavailable
	}
	i := d.intent
	if !d.cleanup.Valid || !d.post.Valid || d.quote.Valid || d.kind != "Delete" || !authority.ValidID(i.Requester) || !authority.ValidID(i.Owner) || !authority.ValidID(i.Session) || i.Owner != p.owner || i.Version < 1 || i.Epoch < 1 {
		return false, nil
	}
	var receipt cleanupReceipt
	e := tx.QueryRowContext(ctx, `SELECT local_actor,local_key_id,object_uri,publisher_name,authority_stamp FROM owned_federation_dispatch_receipts WHERE source_delivery_id=$1 AND publisher_id=$2 AND policy_id=$3 AND expires_at>clock_timestamp()`, d.cleanup.Int64, p.id, nullablePolicy(r.config.CleanupPolicyID)).Scan(&receipt.actor, &receipt.key, &receipt.object, &receipt.name, &receipt.stamp)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if receipt.actor != p.actorURI || receipt.key != p.keyID || receipt.object != r.objectID(d.post.String) || receipt.name != p.name || !bytes.Equal(d.body, cleanupBody(receipt.actor, receipt.object, d.cleanup.Int64)) {
		return false, nil
	}
	current, e := r.cleanupPolicyCurrent(ctx, tx, p.id)
	if e != nil || !current {
		return false, e
	}
	allowed, e := guard.StoredSessionEligible(ctx, r.db, tx, i.Requester, i.Session, i.Version, i.Epoch, f.Permission)
	if e != nil || !allowed {
		return false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_authority_accounts a ON a.id=p.account_id JOIN owned_federation_publishers f ON f.publisher_id=p.id JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$2 WHERE p.id=$1 AND p.account_id=$3 AND p.type=0 AND p.realm_id IS NULL AND p.deleted_at IS NULL AND f.enabled AND NOT a.disabled AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=100 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=a.id AND x.kind IN('disable_account','permission_modification') AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp()))) AND $4::timestamptz>clock_timestamp() AND $5::timestamptz>clock_timestamp()`, p.id, i.Requester, i.Owner, i.Expires, d.expires).Scan(&allowed)
	if e != nil || !allowed {
		return false, e
	}
	var stamp []byte
	e = tx.QueryRowContext(ctx, `SELECT owned_federation_cleanup_stamp($1::uuid,$2::uuid,$3::uuid)`, i.Requester, p.id, i.Session).Scan(&stamp)
	return e == nil && bytes.Equal(stamp, receipt.stamp), e
}
func nullablePolicy(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func cleanupBody(actor, object string, source int64) []byte {
	return marshal(f.Activity{Context: f.Context, ID: fmt.Sprintf("%s/cleanup/%d", object, source), Type: "Delete", Actor: actor, Object: marshal(map[string]string{"id": object, "type": "Tombstone"}), To: []string{f.Public}, CC: []string{actor + "/followers"}})
}

// enqueueWithdrawals selects only destinations with immutable dispatch evidence.
// It never consults or copies private post content and never refreshes a deadline.
// Reconciliation can safely finish with no eligible cleanup: a never-dispatched
// post grants no authority to contact a peer. Current denial suppresses cleanup.
func (r *Repository) enqueueWithdrawals(ctx context.Context, tx *sql.Tx, p publisher, postID string) error {
	current, e := r.cleanupPolicyCurrent(ctx, tx, p.id)
	if e != nil || !current {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT ON(d.actor_uri) c.source_delivery_id,c.local_actor,c.object_uri FROM owned_federation_dispatch_receipts c JOIN owned_federation_deliveries d ON d.id=c.source_delivery_id WHERE d.publisher_id=$1 AND d.post_id=$2 ORDER BY d.actor_uri,c.source_delivery_id DESC LIMIT 1001`, p.id, postID)
	if e != nil {
		return e
	}
	type candidate struct {
		source        int64
		actor, object string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.source, &c.actor, &c.object); e != nil {
			rows.Close()
			return e
		}
		candidates = append(candidates, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(candidates) > 1000 {
		return f.ErrUnavailable
	}
	for _, c := range candidates {
		body := cleanupBody(c.actor, c.object, c.source)
		// The INSERT guard derives and verifies the complete source binding. The
		// ordinary immutable queue provides a single stable activity and retry budget.
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_deliveries(publisher_id,actor_uri,inbox_uri,activity_id,activity_type,payload,post_id,post_updated_at,post_audit_id,next_attempt,created_at,origin_intent,remote_binding,local_fingerprint,max_attempts,expires_at,peer_epoch,peer_security_epoch,cleanup_receipt_id) SELECT d.publisher_id,d.actor_uri,d.inbox_uri,$2,'Delete',$3,d.post_id,d.post_updated_at,d.post_audit_id,$4,$4,d.origin_intent,d.remote_binding,d.local_fingerprint,d.max_attempts,c.expires_at,d.peer_epoch,d.peer_security_epoch,c.source_delivery_id FROM owned_federation_dispatch_receipts c JOIN owned_federation_deliveries d ON d.id=c.source_delivery_id WHERE c.source_delivery_id=$1 AND c.policy_id=$5 AND c.expires_at>clock_timestamp() ON CONFLICT DO NOTHING`, c.source, fmt.Sprintf("%s/cleanup/%d", c.object, c.source), body, r.config.Clock(), r.config.CleanupPolicyID)
		if e != nil {
			return e
		}
	}
	return nil
}

type cleanupActivationGuard interface {
	AuthorizeWithGrant(context.Context, *sql.DB, *sql.Tx, string, string, ...string) (authority.Principal, error)
}

func (r *Repository) authorizeCleanupActivation(ctx context.Context, tx *sql.Tx, actor, publisherID string) error {
	guard, ok := r.guard.(cleanupActivationGuard)
	if !ok {
		return f.ErrUnavailable
	}
	proof, e := guard.AuthorizeWithGrant(ctx, r.db, tx, actor, f.CleanupPermission, f.Permission)
	if e != nil {
		return e
	}
	if proof.Session.Type != 0 || proof.Session.ParentID != "" || proof.Session.ClientID != "" {
		return f.ErrForbidden
	}
	var allowed bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$2 WHERE p.id=$1 AND p.account_id=$2 AND p.type=0 AND p.realm_id IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=100)`, publisherID, actor).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return f.ErrForbidden
	}
	return nil
}
