// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"database/sql"
	"errors"
	f "solsynth.local/go-migration/ports/federation"
)

func (r *Repository) Change(ctx context.Context, actor, ref string, c f.Change) error {
	switch c.Action {
	case "follow", "unfollow", "accept", "reject", "block", "unblock":
	default:
		return f.ErrInvalid
	}
	k, e := r.remote(ctx, c.RemoteActor)
	if e != nil {
		return e
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if !p.enabled {
		return f.ErrNotFound
	}
	if _, e = r.localKey(ctx, p); e != nil {
		return e
	}
	if e = r.ensurePeer(ctx, tx, k); e != nil {
		return e
	}
	b, e := r.blocked(ctx, tx, p.id, k.Actor)
	if e != nil {
		return e
	}
	if b && c.Action != "unblock" && c.Action != "block" {
		return f.ErrForbidden
	}
	switch c.Action {
	case "block":
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=COALESCE(revoked_at,$3) WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor, r.config.Clock()); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_blocks(publisher_id,actor_uri,created_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status='rejected',updated_at=$3 WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',finished_at=$3,lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND actor_uri=$2 AND status IN('pending','inflight')`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_objects WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor)
		if e != nil {
			return e
		}
	case "unblock":
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_blocks WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status='undone',updated_at=$3 WHERE publisher_id=$1 AND actor_uri=$2 AND status='rejected'`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
	case "follow":
		var status string
		e = tx.QueryRowContext(ctx, `SELECT status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, k.Actor).Scan(&status)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if status == "accepted" || status == "pending" {
			return nil
		}
		a, e := r.activity(p, "Follow", k.Actor, k.Actor)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_relationships(publisher_id,actor_uri,direction,status,follow_id,updated_at) VALUES($1,$2,'out','pending',$3,$4) ON CONFLICT(publisher_id,actor_uri,direction) DO UPDATE SET status='pending',follow_id=EXCLUDED.follow_id,updated_at=EXCLUDED.updated_at`, p.id, k.Actor, a.ID, r.config.Clock())
		if e != nil {
			return e
		}
		if e = r.enqueue(ctx, tx, p, k, a, "", nil); e != nil {
			return e
		}
	case "unfollow":
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=COALESCE(revoked_at,$3) WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor, r.config.Clock()); e != nil {
			return e
		}
		var id, status string
		e = tx.QueryRowContext(ctx, `SELECT follow_id,status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, k.Actor).Scan(&id, &status)
		if e != nil {
			return mapped(e)
		}
		if status == "undone" {
			return nil
		}
		original := f.Activity{ID: id, Type: "Follow", Actor: r.actorID(p), Object: marshal(k.Actor)}
		a, e := r.activity(p, "Undo", original, k.Actor)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status='undone',updated_at=$3 WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, k.Actor, r.config.Clock())
		if e != nil {
			return e
		}
		if e = r.enqueue(ctx, tx, p, k, a, "", nil); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_objects WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor)
		if e != nil {
			return e
		}
	case "accept", "reject":
		var id, status string
		e = tx.QueryRowContext(ctx, `SELECT follow_id,status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in'`, p.id, k.Actor).Scan(&id, &status)
		if e != nil {
			return mapped(e)
		}
		result, kind := "accepted", "Accept"
		if c.Action == "reject" {
			result, kind = "rejected", "Reject"
		}
		if status == result {
			return nil
		}
		if status != "pending" {
			return f.ErrForbidden
		}
		original := f.Activity{ID: id, Type: "Follow", Actor: k.Actor, Object: marshal(r.actorID(p))}
		a, e := r.activity(p, kind, original, k.Actor)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status=$3,updated_at=$4 WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in'`, p.id, k.Actor, result, r.config.Clock())
		if e != nil {
			return e
		}
		if e = r.enqueue(ctx, tx, p, k, a, "", nil); e != nil {
			return e
		}
	}
	if e = r.audit(ctx, tx, actor, p.id, c.Action); e != nil {
		return e
	}
	return tx.Commit()
}
