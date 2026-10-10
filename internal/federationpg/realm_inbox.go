// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"time"

	f "solsynth.local/go-migration/ports/federation"
)

func (r *Repository) realmByID(ctx context.Context, q queryer, id string) (realmIdentity, error) {
	x, active, err := r.realmStateByID(ctx, q, id)
	if err != nil {
		return x, err
	}
	if !active {
		return x, f.ErrNotFound
	}
	return x, nil
}

func (r *Repository) realmStateByID(ctx context.Context, q queryer, id string) (realmIdentity, bool, error) {
	var x realmIdentity
	var active bool
	err := q.QueryRowContext(ctx, `SELECT x.id::text,x.slug,x.name,x.description,x.account_id::text,x.created_at,fr.enabled,fr.actor_uri,fr.key_id,COALESCE((SELECT p.id::text FROM owned_publishers p WHERE p.realm_id=x.id AND p.type=1 ORDER BY(p.deleted_at IS NULL AND p.shadowbanned_at IS NULL)DESC,p.id LIMIT 1),''),fr.enabled AND x.is_public AND x.is_community AND x.deleted_at IS NULL AND NOT a.disabled AND EXISTS(SELECT 1 FROM owned_publishers p WHERE p.realm_id=x.id AND p.type=1 AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL) FROM owned_realms x JOIN owned_authority_accounts a ON a.id=x.account_id JOIN owned_federation_realms fr ON fr.realm_id=x.id WHERE x.id=$1`, id).Scan(&x.id, &x.slug, &x.name, &x.description, &x.owner, &x.created, &x.enabled, &x.actor, &x.key, &x.publisher, &active)
	if err != nil {
		return x, false, mapped(err)
	}
	if x.publisher == "" {
		return x, false, f.ErrNotFound
	}
	want := r.config.Origin + "/activitypub/realms/" + url.PathEscape(x.slug)
	if x.actor != want || x.key != want+"#main-key" {
		return x, false, f.ErrUnavailable
	}
	return x, active, nil
}

// lockRealmAuthority holds every mutable local row that can authorize a realm
// dispatch. The order matches realm and publisher management: accounts, realm,
// publishers, member federation settings, then the federation realm anchor.
func (r *Repository) lockRealmAuthority(ctx context.Context, tx *sql.Tx, realmID, postID string) (realmIdentity, bool, string, error) {
	var owner, group, source, sourceOwner string
	err := tx.QueryRowContext(ctx, `SELECT r.account_id::text,COALESCE((SELECT p.id::text FROM owned_publishers p WHERE p.realm_id=r.id AND p.type=1 ORDER BY(p.deleted_at IS NULL AND p.shadowbanned_at IS NULL)DESC,p.id LIMIT 1),''),COALESCE(post.publisher_id::text,''),COALESCE(source.account_id::text,'') FROM owned_realms r LEFT JOIN owned_text_posts post ON post.id=NULLIF($2,'')::uuid AND post.realm_id=r.id LEFT JOIN owned_publishers source ON source.id=post.publisher_id WHERE r.id=$1`, realmID, postID).Scan(&owner, &group, &source, &sourceOwner)
	if err != nil {
		return realmIdentity{}, false, source, mapped(err)
	}
	if group == "" {
		return realmIdentity{}, false, source, f.ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 OR id=NULLIF($2,'')::uuid ORDER BY id FOR SHARE`, owner, sourceOwner)
	if err != nil {
		return realmIdentity{}, false, source, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return realmIdentity{}, false, source, err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return realmIdentity{}, false, source, err
	}
	rows.Close()
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1 FOR SHARE`, realmID).Scan(&locked); err != nil {
		return realmIdentity{}, false, source, mapped(err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 OR id=NULLIF($2,'')::uuid ORDER BY id FOR SHARE`, group, source)
	if err != nil {
		return realmIdentity{}, false, source, err
	}
	for rows.Next() {
		if err = rows.Scan(&locked); err != nil {
			rows.Close()
			return realmIdentity{}, false, source, err
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return realmIdentity{}, false, source, err
	}
	rows.Close()
	if source != "" && source != group {
		var currentOwner string
		if err = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1`, source).Scan(&currentOwner); err != nil || currentOwner != sourceOwner {
			if err == nil {
				err = f.ErrUnavailable
			}
			return realmIdentity{}, false, source, mapped(err)
		}
		rows, err = tx.QueryContext(ctx, `SELECT publisher_id::text FROM owned_federation_publishers WHERE publisher_id=$1 FOR SHARE`, source)
		if err != nil {
			return realmIdentity{}, false, source, err
		}
		for rows.Next() {
			if err = rows.Scan(&locked); err != nil {
				rows.Close()
				return realmIdentity{}, false, source, err
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return realmIdentity{}, false, source, err
		}
		rows.Close()
	}
	if err = tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_federation_realms WHERE realm_id=$1 FOR SHARE`, realmID).Scan(&locked); err != nil {
		return realmIdentity{}, false, source, mapped(err)
	}
	x, active, err := r.realmStateByID(ctx, tx, realmID)
	if err != nil || x.owner != owner || x.publisher != group {
		if err == nil {
			err = f.ErrUnavailable
		}
		return x, false, source, err
	}
	return x, active, source, nil
}

func (r *Repository) realmEnqueue(ctx context.Context, tx *sql.Tx, x realmIdentity, key f.RemoteKey, activity f.Activity) error {
	return r.realmEnqueuePost(ctx, tx, x, key, activity, "", nil)
}

func (r *Repository) realmEnqueuePost(ctx context.Context, tx *sql.Tx, x realmIdentity, key f.RemoteKey, activity f.Activity, postID string, updated *time.Time) error {
	if !r.config.ValidRemote(key) {
		return f.ErrForbidden
	}
	body, err := json.Marshal(activity)
	if err != nil || len(body) > f.MaxBody {
		return f.ErrInvalid
	}
	binding, err := deliveryBinding(key)
	if err != nil {
		return f.ErrForbidden
	}
	signer, err := r.keys.LocalKey(ctx, x.publisher)
	if err != nil {
		return f.ErrUnavailable
	}
	fingerprint, err := localFingerprint(signer)
	if err != nil {
		return f.ErrUnavailable
	}
	var post, postUpdated any
	if postID != "" && updated != nil {
		post, postUpdated = postID, *updated
	}
	now := r.config.Clock()
	expires := now.Add(time.Duration(r.config.MaxAttempts) * 24 * time.Hour)
	_, err = tx.ExecContext(ctx, `INSERT INTO owned_federation_realm_deliveries(realm_id,actor_uri,inbox_uri,activity_id,activity_type,payload,post_id,post_updated_at,remote_binding,local_fingerprint,max_attempts,expires_at,next_attempt,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)ON CONFLICT(activity_id,actor_uri)DO NOTHING`, x.id, key.Actor, key.Inbox, activity.ID, activity.Type, body, post, postUpdated, binding, fingerprint, r.config.MaxAttempts, expires, now)
	return err
}

func (r *Repository) ReceiveRealm(ctx context.Context, slug string, verified f.Verified) error {
	x, err := r.realm(ctx, r.db, slug, true)
	if err != nil {
		return err
	}
	if !verified.ValidFor(x.actor + "/inbox") {
		return f.ErrSignature
	}
	activity, key := verified.Activity(), verified.Remote()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	x, active, _, err = r.lockRealmAuthority(ctx, tx, x.id, "")
	if err != nil || !active {
		if err != nil {
			return err
		}
		return f.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE owned_federation_moderation_rules IN SHARE MODE`); err != nil {
		return err
	}
	decision, err := r.moderationWith(ctx, tx, key.Actor, string(activity.Object))
	if err != nil {
		return err
	}
	silenced := decision.Action == "silence" && activity.Type == "Create"
	if !decision.Allowed && !silenced {
		return f.ErrForbidden
	}
	current, err := r.remoteWithQuery(ctx, tx, key.Actor)
	if err != nil || !verified.Fresh(r.config.Clock()) || !sameQuoteKey(current, key) {
		return f.ErrSignature
	}
	if err = r.ensurePeer(ctx, tx, key); err != nil {
		return err
	}
	var priorActor string
	var priorDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT actor_uri,digest FROM owned_federation_realm_inbox WHERE realm_id=$1 AND activity_id=$2`, x.id, activity.ID).Scan(&priorActor, &priorDigest)
	if err == nil {
		if priorActor != key.Actor || !bytes.Equal(priorDigest, verified.Digest()) {
			return f.ErrReplay
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	switch activity.Type {
	case "Follow":
		target, targetErr := f.ObjectID(activity.Object)
		if targetErr != nil || target != x.actor {
			return f.ErrForbidden
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO owned_federation_realm_relationships(realm_id,actor_uri,status,follow_id,updated_at)VALUES($1,$2,'accepted',$3,$4)ON CONFLICT(realm_id,actor_uri)DO UPDATE SET status='accepted',follow_id=EXCLUDED.follow_id,updated_at=EXCLUDED.updated_at`, x.id, key.Actor, activity.ID, r.config.Clock())
		if err != nil {
			return err
		}
		id, idErr := newID()
		if idErr != nil {
			return idErr
		}
		accept := f.Activity{Context: f.Context, ID: x.actor + "/activities/" + id, Type: "Accept", Actor: x.actor, Object: marshal(activity), To: []string{key.Actor}, Published: r.config.Clock()}
		if err = r.realmEnqueue(ctx, tx, x, key, accept); err != nil {
			return err
		}
	case "Undo":
		var original f.Activity
		if json.Unmarshal(activity.Object, &original) != nil || original.Type != "Follow" || original.Actor != key.Actor {
			return f.ErrUnsupported
		}
		target, targetErr := f.ObjectID(original.Object)
		if targetErr != nil || target != x.actor {
			return f.ErrForbidden
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE owned_federation_realm_relationships SET status='undone',updated_at=$4 WHERE realm_id=$1 AND actor_uri=$2 AND follow_id=$3`, x.id, key.Actor, original.ID, r.config.Clock())
		if updateErr != nil {
			return updateErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return f.ErrForbidden
		}
		if _, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$3,lease_token=NULL,lease_until=NULL WHERE realm_id=$1 AND actor_uri=$2 AND status IN('pending','inflight') AND dispatch_started_at IS NULL`, x.id, key.Actor, r.config.Clock()); err != nil {
			return err
		}
	case "Create":
		if silenced {
			break
		}
		if err = r.receiveRealmCreate(ctx, tx, x, key, activity); err != nil {
			return err
		}
	default:
		return f.ErrUnsupported
	}
	current, err = r.remoteWithQuery(ctx, tx, key.Actor)
	if err != nil || !verified.Fresh(r.config.Clock()) || !sameQuoteKey(current, key) {
		return f.ErrSignature
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owned_federation_realm_inbox(realm_id,activity_id,actor_uri,digest,received_at)VALUES($1,$2,$3,$4,$5)`, x.id, activity.ID, key.Actor, verified.Digest(), r.config.Clock())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) receiveRealmCreate(ctx context.Context, tx *sql.Tx, x realmIdentity, key f.RemoteKey, activity f.Activity) error {
	var follows bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_realm_relationships WHERE realm_id=$1 AND actor_uri=$2 AND status='accepted')`, x.id, key.Actor).Scan(&follows); err != nil {
		return err
	}
	if !follows || !f.IsPublic(activity) {
		return f.ErrForbidden
	}
	objectID, objectErr := f.ObjectID(activity.Object)
	actorOrigin, _ := f.Origin(key.Actor)
	objectOrigin, _ := f.Origin(objectID)
	var note f.Note
	if objectErr != nil || !r.config.Allows(objectID) || actorOrigin != objectOrigin || json.Unmarshal(activity.Object, &note) != nil || note.ID != objectID || note.Type != "Note" || note.AttributedTo != key.Actor || len(note.Content) > 262144 || (!f.Has(note.To, f.Public) && !f.Has(note.CC, f.Public)) {
		return f.ErrForbidden
	}
	if err := r.validateAttachments(key.Actor, note.Attachments); err != nil {
		return err
	}
	decision, err := r.moderationWith(ctx, tx, key.Actor, note.Content)
	if err != nil {
		return err
	}
	if !decision.Allowed || decision.Action == "media_reject" && len(note.Attachments) > 0 {
		return f.ErrForbidden
	}
	note.Context = f.Context
	note.Content = html.EscapeString(note.Content)
	note.Summary = html.EscapeString(note.Summary)
	note.MediaType = "text/html"
	note.To = []string{f.Public}
	note.CC = []string{x.actor + "/followers"}
	id, err := newID()
	if err != nil {
		return err
	}
	announce := f.Activity{Context: f.Context, ID: x.actor + "/activities/" + id, Type: "Announce", Actor: x.actor, Object: marshal(note), To: []string{f.Public}, CC: []string{x.actor + "/followers"}, Published: r.config.Clock()}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owned_federation_realm_outbox(realm_id,activity_id,activity,created_at)VALUES($1,$2,$3,$4)`, x.id, announce.ID, marshal(announce), r.config.Clock()); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT actor_uri FROM owned_federation_realm_relationships WHERE realm_id=$1 AND status='accepted' AND actor_uri<>$2 ORDER BY actor_uri`, x.id, key.Actor)
	if err != nil {
		return err
	}
	actors := []string{}
	for rows.Next() {
		var actor string
		if err = rows.Scan(&actor); err != nil {
			rows.Close()
			return err
		}
		actors = append(actors, actor)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, actor := range actors {
		peer, peerErr := r.remoteWithQuery(ctx, tx, actor)
		if peerErr != nil {
			return peerErr
		}
		if err = r.realmEnqueue(ctx, tx, x, peer, announce); err != nil {
			return err
		}
	}
	return nil
}

type realmDelivery struct {
	id                                          int64
	realm, remote, inbox, activity, kind, token string
	body                                        []byte
	post                                        sql.NullString
	updated                                     sql.NullTime
	binding, fingerprint                        string
	attempts, maxAttempts                       int
	lease, expires                              time.Time
}

func (r *Repository) ProcessRealmDeliveries(ctx context.Context, transport f.Transport, limit int) (int, error) {
	if transport == nil || r.keys == nil {
		return 0, f.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	count := 0
	for count < limit {
		d, err := r.claimRealmDelivery(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		if err = r.deliverRealm(ctx, transport, d); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (r *Repository) claimRealmDelivery(ctx context.Context) (realmDelivery, error) {
	var d realmDelivery
	token, err := newID()
	if err != nil {
		return d, err
	}
	_, err = r.db.ExecContext(ctx, `WITH expired AS(SELECT id FROM owned_federation_realm_deliveries WHERE status IN('pending','inflight') AND (expires_at<=clock_timestamp() OR (attempts>=LEAST(max_attempts,$1) AND (lease_until IS NULL OR lease_until<=clock_timestamp()))) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 100) UPDATE owned_federation_realm_deliveries d SET status=CASE WHEN expires_at<=clock_timestamp() THEN 'cancelled' ELSE 'dead' END,finished_at=clock_timestamp(),lease_token=NULL,lease_until=NULL FROM expired WHERE d.id=expired.id`, r.config.MaxAttempts)
	if err != nil {
		return d, err
	}
	err = r.db.QueryRowContext(ctx, `WITH candidate AS(SELECT d.id FROM owned_federation_realm_deliveries d WHERE d.expires_at>clock_timestamp() AND d.attempts<LEAST(d.max_attempts,$1) AND ((d.status='pending' AND d.next_attempt<=$2)OR(d.status='inflight' AND d.lease_until<=clock_timestamp())) AND NOT EXISTS(SELECT 1 FROM owned_federation_realm_deliveries prior WHERE prior.realm_id=d.realm_id AND prior.actor_uri=d.actor_uri AND prior.id<d.id AND prior.status IN('pending','inflight')) ORDER BY d.next_attempt,d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1) UPDATE owned_federation_realm_deliveries d SET status='inflight',attempts=attempts+1,lease_token=$3,lease_until=LEAST(clock_timestamp()+($4::bigint*interval '1 microsecond'),expires_at),dispatch_started_at=NULL FROM candidate c WHERE d.id=c.id RETURNING d.id,d.realm_id::text,d.actor_uri,d.inbox_uri,d.activity_id,d.activity_type,d.payload,d.post_id::text,d.post_updated_at,d.remote_binding,d.local_fingerprint,d.attempts,d.max_attempts,d.lease_until,d.expires_at`, r.config.MaxAttempts, r.config.Clock(), token, (2*r.config.DeliveryTimeout).Microseconds()).Scan(&d.id, &d.realm, &d.remote, &d.inbox, &d.activity, &d.kind, &d.body, &d.post, &d.updated, &d.binding, &d.fingerprint, &d.attempts, &d.maxAttempts, &d.lease, &d.expires)
	d.token = token
	return d, err
}

func (r *Repository) observeRealmDelivery(ctx context.Context, d realmDelivery, result string, prepared **http.Request) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var x realmIdentity
	var realmErr error
	var active bool
	x, active, _, realmErr = r.lockRealmAuthority(ctx, tx, d.realm, func() string {
		if d.post.Valid {
			return d.post.String
		}
		return ""
	}())
	if realmErr == nil && !active && d.kind != "Delete" && d.kind != "Undo" {
		realmErr = f.ErrNotFound
	}
	finish := func(status string) error {
		var finished any
		if status != "pending" {
			finished = r.config.Clock()
		}
		_, e := tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status=$3,next_attempt=$4,finished_at=$5,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2 AND status='inflight' AND attempts=$6 AND lease_until>clock_timestamp()`, d.id, d.token, status, r.config.Clock().Add(retryDelay(d.attempts)), finished, d.attempts)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	cancel := realmErr != nil
	var postSource string
	if d.post.Valid {
		var updated time.Time
		var visible bool
		err = tx.QueryRowContext(ctx, `SELECT updated_at,visibility=0 AND drafted_at IS NULL AND deleted_at IS NULL AND shadowbanned_at IS NULL AND COALESCE(published_at<=$2,false),publisher_id::text FROM owned_text_posts WHERE id=$1 AND realm_id=$3 FOR UPDATE`, d.post.String, r.config.Clock(), d.realm).Scan(&updated, &visible, &postSource)
		if err != nil {
			cancel = true
		}
		if err == nil && d.kind == "Delete" {
			if visible {
				cancel = true
			}
		} else if err == nil && d.kind == "Undo" {
			if !d.updated.Valid || !updated.Equal(d.updated.Time) {
				cancel = true
			}
		} else if err == nil && (!visible || !d.updated.Valid || !updated.Equal(d.updated.Time)) {
			cancel = true
		} else if err == nil && !cancel && postSource != x.publisher {
			if _, sourceErr := r.publisher(ctx, tx, postSource, true); sourceErr != nil {
				cancel = true
			}
		}
	}
	var relationshipStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM owned_federation_realm_relationships WHERE realm_id=$1 AND actor_uri=$2 FOR SHARE`, d.realm, d.remote).Scan(&relationshipStatus); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		cancel = true
	} else if relationshipStatus != "accepted" {
		cancel = true
	}
	// All realm paths acquire authority, post, relationship, then delivery locks. This prevents a
	// reconciler and a worker from waiting on one another in reverse order.
	var lease string
	err = tx.QueryRowContext(ctx, `SELECT lease_token::text FROM owned_federation_realm_deliveries WHERE id=$1 AND status='inflight' AND attempts=$2 AND lease_until>clock_timestamp() FOR UPDATE`, d.id, d.attempts).Scan(&lease)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if lease != d.token {
		return nil
	}
	if cancel {
		return finish("cancelled")
	}
	current, err := r.remoteWithQuery(ctx, tx, d.remote)
	if err != nil {
		return finish("cancelled")
	}
	binding, err := deliveryBinding(current)
	if err != nil || binding != d.binding || current.Inbox != d.inbox {
		return finish("cancelled")
	}
	signer, err := r.keys.LocalKey(ctx, x.publisher)
	if err != nil {
		return finish("cancelled")
	}
	fingerprint, err := localFingerprint(signer)
	if err != nil || fingerprint != d.fingerprint {
		return finish("cancelled")
	}
	var activity f.Activity
	if json.Unmarshal(d.body, &activity) != nil || activity.ID != d.activity || activity.Type != d.kind || activity.Actor != x.actor {
		return finish("dead")
	}
	var request *http.Request
	if result == "" {
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, d.inbox, bytes.NewReader(d.body))
		if err != nil {
			return finish("dead")
		}
		if err = f.Sign(request, d.body, x.key, signer, r.config.Clock()); err != nil {
			return finish("dead")
		}
	}
	// Signing and key access may wait. Fence the peer afterwards, then re-read
	// every mutable local authority immediately before marking dispatch started.
	if err = r.deliveryRemoteFence(ctx, tx, current); err != nil {
		return finish("cancelled")
	}
	fresh, freshActive, freshErr := r.realmStateByID(ctx, tx, d.realm)
	if freshErr != nil || fresh.actor != x.actor || fresh.key != x.key || fresh.publisher != x.publisher || !freshActive && d.kind != "Delete" && d.kind != "Undo" {
		return finish("cancelled")
	}
	if d.post.Valid && d.kind != "Delete" && d.kind != "Undo" && postSource != fresh.publisher {
		if _, sourceErr := r.publisher(ctx, tx, postSource, true); sourceErr != nil {
			return finish("cancelled")
		}
	}
	currentSigner, keyErr := r.keys.LocalKey(ctx, fresh.publisher)
	if keyErr != nil {
		return finish("cancelled")
	}
	currentFingerprint, fingerprintErr := localFingerprint(currentSigner)
	if fingerprintErr != nil || currentFingerprint != d.fingerprint {
		return finish("cancelled")
	}
	if result != "" {
		return finish(result)
	}
	resultSQL, err := tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET dispatch_started_at=$3 WHERE id=$1 AND lease_token=$2 AND status='inflight' AND dispatch_started_at IS NULL`, d.id, d.token, r.config.Clock())
	if err != nil {
		return err
	}
	if n, _ := resultSQL.RowsAffected(); n != 1 {
		return nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	*prepared = request
	return nil
}

func (r *Repository) deliverRealm(ctx context.Context, transport f.Transport, d realmDelivery) error {
	work, cancel := context.WithTimeout(ctx, r.config.DeliveryTimeout)
	defer cancel()
	work, leaseCancel := context.WithDeadline(work, d.lease)
	defer leaseCancel()
	var request *http.Request
	if err := r.observeRealmDelivery(work, d, "", &request); err != nil {
		return err
	}
	if request == nil || work.Err() != nil {
		return nil
	}
	code, sendErr := transport.Send(work, request)
	status := "pending"
	if sendErr == nil && code >= 200 && code < 300 {
		status = "sent"
	} else if d.attempts >= d.maxAttempts || d.attempts >= r.config.MaxAttempts || sendErr == nil && code >= 300 && code < 500 && code != 408 && code != 429 {
		status = "dead"
	}
	if work.Err() != nil {
		return nil
	}
	return r.observeRealmDelivery(work, d, status, nil)
}
