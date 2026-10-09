// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	f "solsynth.local/go-migration/ports/federation"
)

func (r *Repository) Receive(ctx context.Context, name string, v f.Verified) error {
	p, e := r.publisherName(ctx, r.db, name, true)
	if e != nil {
		return e
	}
	// Verification binds the exact signed request route. An owned handle alias
	// may reach the same stable actor without granting another actor's proof.
	if !v.ValidFor(r.config.Origin + "/activitypub/actors/" + url.PathEscape(name)) {
		return f.ErrSignature
	}
	a, k := v.Activity(), v.Remote()
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// A single publisher lock serializes inbox deduplication, block/follow changes,
	// and post fanout. Canonical owner disable is checked under its account lock.
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 AND NOT disabled FOR UPDATE`, p.owner).Scan(&owner)
	if e != nil {
		return mapped(e)
	}
	var id string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, p.id).Scan(&id)
	if e != nil {
		return mapped(e)
	}
	p, e = r.publisher(ctx, tx, p.id, true)
	if e != nil {
		return e
	}
	if b, e := r.blocked(ctx, tx, p.id, k.Actor); e != nil {
		return e
	} else if b {
		return f.ErrForbidden
	}
	{
		current, x := r.remoteWithQuery(ctx, tx, k.Actor)
		if x != nil || !v.Fresh(r.config.Clock()) || !sameQuoteKey(current, k) {
			return f.ErrSignature
		}
	}
	var existingActor string
	var digest []byte
	e = tx.QueryRowContext(ctx, `SELECT actor_uri,digest FROM owned_federation_inbox WHERE publisher_id=$1 AND activity_id=$2`, p.id, a.ID).Scan(&existingActor, &digest)
	if e == nil {
		if existingActor != k.Actor || !bytes.Equal(digest, v.Digest()) {
			return f.ErrReplay
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e = r.ensurePeer(ctx, tx, k); e != nil {
		return e
	}
	switch a.Type {
	case "QuoteRequest":
		if e = r.receiveQuoteRequest(ctx, tx, p, k, a); e != nil {
			return e
		}
	case "Follow":
		target, e := f.ObjectID(a.Object)
		if e != nil || target != r.actorID(p) {
			return f.ErrForbidden
		}
		var prior string
		e = tx.QueryRowContext(ctx, `SELECT status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in'`, p.id, k.Actor).Scan(&prior)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		// A new Follow ID does not bypass a prior explicit rejection.
		if prior == "rejected" {
			return f.ErrForbidden
		}
		status := "pending"
		if !p.manual {
			status = "accepted"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_relationships(publisher_id,actor_uri,direction,status,follow_id,updated_at) VALUES($1,$2,'in',$3,$4,$5) ON CONFLICT(publisher_id,actor_uri,direction) DO UPDATE SET status=EXCLUDED.status,follow_id=EXCLUDED.follow_id,updated_at=EXCLUDED.updated_at`, p.id, k.Actor, status, a.ID, r.config.Clock())
		if e != nil {
			return e
		}
		if status == "accepted" {
			accept, e := r.activity(p, "Accept", a, k.Actor)
			if e != nil {
				return e
			}
			if e = r.enqueue(ctx, tx, p, k, accept, "", nil); e != nil {
				return e
			}
		}
	case "Accept", "Reject":
		original, e := f.ObjectID(a.Object)
		if e != nil {
			return e
		}
		// The remote actor must be the exact target of our original pending Follow.
		var follow, status string
		e = tx.QueryRowContext(ctx, `SELECT follow_id,status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, k.Actor).Scan(&follow, &status)
		if e != nil || follow != original || status != "pending" {
			return f.ErrForbidden
		}
		if len(a.Object) > 0 && a.Object[0] == '{' {
			var embedded f.Activity
			if json.Unmarshal(a.Object, &embedded) != nil || embedded.Actor != r.actorID(p) || embedded.Type != "Follow" {
				return f.ErrForbidden
			}
			target, e := f.ObjectID(embedded.Object)
			if e != nil || target != k.Actor {
				return f.ErrForbidden
			}
		}
		result := "accepted"
		if a.Type == "Reject" {
			result = "rejected"
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status=$3,updated_at=$4 WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, k.Actor, result, r.config.Clock())
		if e != nil {
			return e
		}
	case "Undo":
		var original f.Activity
		if json.Unmarshal(a.Object, &original) != nil || original.Type != "Follow" || original.Actor != k.Actor {
			return f.ErrUnsupported
		}
		target, e := f.ObjectID(original.Object)
		if e != nil || target != r.actorID(p) {
			return f.ErrForbidden
		}
		result, e := tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status='undone',updated_at=$4 WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in' AND follow_id=$3`, p.id, k.Actor, original.ID, r.config.Clock())
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return f.ErrForbidden
		}
		// Withdraw any acceptance still waiting for this peer.
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',finished_at=$3 WHERE publisher_id=$1 AND actor_uri=$2 AND status='pending' AND activity_type='Accept' AND quote_id IS NULL`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
	case "Create", "Update", "Delete":
		var follows bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out' AND status='accepted')`, p.id, k.Actor).Scan(&follows)
		if e != nil {
			return e
		}
		if !follows || (!f.Addressed(a, r.actorID(p)) && !f.IsPublic(a)) {
			return f.ErrForbidden
		}
		if e = r.receiveObject(ctx, tx, p, k, a); e != nil {
			return quotePolicyFailure(tx, e)
		}
	default:
		return f.ErrUnsupported
	}
	current, e := r.remoteWithQuery(ctx, tx, k.Actor)
	if e != nil || !v.Fresh(r.config.Clock()) || !sameQuoteKey(current, k) {
		return f.ErrSignature
	}
	if e = r.quotePublicationCurrent(ctx, tx, p, a); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_inbox(publisher_id,activity_id,actor_uri,digest,received_at) VALUES($1,$2,$3,$4,$5)`, p.id, a.ID, k.Actor, v.Digest(), r.config.Clock()); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) receiveObject(ctx context.Context, tx *sql.Tx, p publisher, k f.RemoteKey, a f.Activity) error {
	id, e := f.ObjectID(a.Object)
	if e != nil || !r.config.Allows(id) {
		return f.ErrInvalid
	}
	actorOrigin, _ := f.Origin(k.Actor)
	objectOrigin, _ := f.Origin(id)
	if actorOrigin != objectOrigin {
		return f.ErrForbidden
	}
	var oldActor string
	var deleted bool
	e = tx.QueryRowContext(ctx, `SELECT actor_uri,deleted FROM owned_federation_objects WHERE publisher_id=$1 AND object_id=$2`, p.id, id).Scan(&oldActor, &deleted)
	exists := e == nil
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if exists && oldActor != k.Actor {
		return f.ErrForbidden
	}
	if a.Type == "Delete" {
		// Ownership must already be established by a verified Create. Unknown
		// object IDs cannot be reserved by another actor on the same origin.
		if !exists {
			return f.ErrNotFound
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=COALESCE(revoked_at,$4) WHERE publisher_id=$1 AND actor_uri=$2 AND object_uri=$3`, p.id, k.Actor, id, r.config.Clock()); e != nil {
			return e
		}
		// Retain the tombstone so later Create cannot resurrect this object.
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_objects(publisher_id,object_id,actor_uri,object,deleted,updated_at) VALUES($1,$2,$3,$4,true,$5) ON CONFLICT(publisher_id,object_id) DO UPDATE SET object=EXCLUDED.object,deleted=true,updated_at=EXCLUDED.updated_at`, p.id, id, k.Actor, string(marshal(map[string]string{"id": id, "type": "Tombstone"})), r.config.Clock())
		return e
	}
	if !f.IsPublic(a) || deleted || a.Type == "Update" && !exists || a.Type == "Create" && exists {
		return f.ErrForbidden
	}
	var n f.Note
	if json.Unmarshal(a.Object, &n) != nil || n.Type != "Note" || n.AttributedTo != k.Actor || len(n.Content) > 262144 || (!f.Has(n.To, f.Public) && !f.Has(n.CC, f.Public)) {
		return f.ErrForbidden
	}
	if e = r.validateQuoteUse(ctx, tx, p, k, &n); e != nil {
		return e
	}
	// Cache only the defined public Note projection. Extension fields, hidden
	// recipients, remote markup, attachments, and credentials are not retained.
	n.Context = f.Context
	n.Content = html.EscapeString(n.Content)
	n.MediaType = "text/html"
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_objects(publisher_id,object_id,actor_uri,object,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(publisher_id,object_id) DO UPDATE SET object=EXCLUDED.object,updated_at=EXCLUDED.updated_at`, p.id, id, k.Actor, string(marshal(n)), r.config.Clock())
	return e
}
