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
	var x realmIdentity
	err := q.QueryRowContext(ctx, `SELECT x.id::text,x.slug,x.name,x.description,x.account_id::text,x.created_at,fr.enabled,fr.actor_uri,fr.key_id,COALESCE((SELECT p.id::text FROM owned_publishers p WHERE p.realm_id=x.id AND p.type=1 AND p.deleted_at IS NULL ORDER BY p.id LIMIT 1),'') FROM owned_realms x JOIN owned_federation_realms fr ON fr.realm_id=x.id WHERE x.id=$1 AND x.is_public AND x.is_community AND x.deleted_at IS NULL`, id).Scan(&x.id, &x.slug, &x.name, &x.description, &x.owner, &x.created, &x.enabled, &x.actor, &x.key, &x.publisher)
	if err != nil {
		return x, mapped(err)
	}
	if !x.enabled || x.publisher == "" {
		return x, f.ErrNotFound
	}
	want := r.config.Origin + "/activitypub/realms/" + url.PathEscape(x.slug)
	if x.actor != want || x.key != want+"#main-key" {
		return x, f.ErrUnavailable
	}
	return x, nil
}

func (r *Repository) realmEnqueue(ctx context.Context, tx *sql.Tx, x realmIdentity, key f.RemoteKey, activity f.Activity) error {
	if !r.config.ValidRemote(key) {
		return f.ErrForbidden
	}
	body, err := json.Marshal(activity)
	if err != nil || len(body) > f.MaxBody {
		return f.ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO owned_federation_realm_deliveries(realm_id,actor_uri,inbox_uri,activity_id,activity_type,payload,next_attempt,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7)ON CONFLICT(activity_id,actor_uri)DO NOTHING`, x.id, key.Actor, key.Inbox, activity.ID, activity.Type, body, r.config.Clock())
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
	var lockedRealm string
	if err = tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_federation_realms WHERE realm_id=$1 FOR SHARE`, x.id).Scan(&lockedRealm); err != nil {
		return mapped(err)
	}
	x, err = r.realmByID(ctx, tx, x.id)
	if err != nil {
		return err
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
		if _, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$3 WHERE realm_id=$1 AND actor_uri=$2 AND status='pending' AND activity_type='Accept'`, x.id, key.Actor, r.config.Clock()); err != nil {
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
	var note f.Note
	if json.Unmarshal(activity.Object, &note) != nil || note.Type != "Note" || note.AttributedTo != key.Actor || len(note.Content) > 262144 {
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

func (r *Repository) ProcessRealmDeliveries(ctx context.Context, transport f.Transport, limit int) (int, error) {
	if transport == nil || limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	count := 0
	for count < limit {
		done, err := r.processRealmDelivery(ctx, transport)
		if err != nil {
			return count, err
		}
		if !done {
			return count, nil
		}
		count++
	}
	return count, nil
}

func (r *Repository) processRealmDelivery(ctx context.Context, transport f.Transport) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id int64
	var realmID, remote, inbox, activityID, kind string
	var body []byte
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT id,realm_id::text FROM owned_federation_realm_deliveries WHERE status='pending' AND next_attempt<=$1 ORDER BY next_attempt,id LIMIT 1`, r.config.Clock()).Scan(&id, &realmID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var lockedRealm string
	lockErr := tx.QueryRowContext(ctx, `SELECT realm_id::text FROM owned_federation_realms WHERE realm_id=$1 FOR SHARE`, realmID).Scan(&lockedRealm)
	if lockErr != nil && !errors.Is(lockErr, sql.ErrNoRows) {
		return false, lockErr
	}
	err = tx.QueryRowContext(ctx, `SELECT actor_uri,inbox_uri,activity_id,activity_type,payload,attempts FROM owned_federation_realm_deliveries WHERE id=$1 AND status='pending' AND next_attempt<=$2 FOR UPDATE`, id, r.config.Clock()).Scan(&remote, &inbox, &activityID, &kind, &body, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if errors.Is(lockErr, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$2 WHERE id=$1`, id, r.config.Clock())
		if err == nil {
			err = tx.Commit()
		}
		return true, err
	}
	x, err := r.realmByID(ctx, tx, realmID)
	if err != nil {
		_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$2 WHERE id=$1`, id, r.config.Clock())
		if err == nil {
			err = tx.Commit()
		}
		return true, err
	}
	var follows bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_realm_relationships WHERE realm_id=$1 AND actor_uri=$2 AND status='accepted')`, realmID, remote).Scan(&follows); err != nil {
		return false, err
	}
	if !follows && kind != "Accept" {
		_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$2 WHERE id=$1`, id, r.config.Clock())
		if err == nil {
			err = tx.Commit()
		}
		return true, err
	}
	key, keyErr := r.remoteWithQuery(ctx, tx, remote)
	if keyErr != nil || key.Inbox != inbox || !r.config.ValidRemote(key) {
		_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='dead',finished_at=$2 WHERE id=$1`, id, r.config.Clock())
		if err == nil {
			err = tx.Commit()
		}
		return true, err
	}
	signer, keyErr := r.keys.LocalKey(ctx, x.publisher)
	if keyErr != nil {
		return false, f.ErrUnavailable
	}
	var activity f.Activity
	if json.Unmarshal(body, &activity) != nil || activity.Actor != x.actor || activity.ID != activityID || activity.Type != kind {
		_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='dead',finished_at=$2 WHERE id=$1`, id, r.config.Clock())
		if err == nil {
			err = tx.Commit()
		}
		return true, err
	}
	request, sendErr := http.NewRequestWithContext(ctx, http.MethodPost, inbox, bytes.NewReader(body))
	if sendErr == nil {
		sendErr = f.Sign(request, body, x.key, signer, r.config.Clock())
	}
	status := 0
	if sendErr == nil {
		status, sendErr = transport.Send(ctx, request)
	}
	attempts++
	state := "pending"
	var finished any
	next := r.config.Clock().Add(time.Duration(1<<min(attempts, 8)) * time.Second)
	if sendErr == nil && status >= 200 && status < 300 {
		state, finished = "sent", r.config.Clock()
	} else if attempts >= r.config.MaxAttempts || sendErr == nil && status >= 400 && status < 500 && status != 408 && status != 429 {
		state, finished = "dead", r.config.Clock()
	}
	_, err = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status=$2,attempts=$3,next_attempt=$4,finished_at=$5 WHERE id=$1`, id, state, attempts, next, finished)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
