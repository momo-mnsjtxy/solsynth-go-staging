// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

const relationColumns = `id::text,account_id::text,related_id::text,status,alias,created_at,updated_at,expired_at`

func scanRelationship(s scanner) (social.Relationship, error) {
	var a social.Relationship
	e := s.Scan(&a.ID, &a.AccountID, &a.RelatedID, &a.Status, &a.Alias, &a.CreatedAt, &a.UpdatedAt, &a.ExpiredAt)
	return a, mapped(e)
}
func relation(ctx context.Context, q querier, a, b string) (social.Relationship, error) {
	return scanRelationship(q.QueryRowContext(ctx, `SELECT `+relationColumns+` FROM owned_social_effective_relationships WHERE account_id=$1 AND related_id=$2`, a, b))
}
func blocked(ctx context.Context, q querier, a, b string) (bool, error) {
	var v bool
	e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1 AND related_id=$2) OR (account_id=$2 AND related_id=$1)) AND status=-100)`, a, b).Scan(&v)
	return v, e
}
func hydrateRelation(ctx context.Context, q querier, v *social.Relationship) error {
	var e error
	v.Account, e = account(ctx, q, v.AccountID)
	if e != nil {
		return e
	}
	v.Related, e = account(ctx, q, v.RelatedID)
	return e
}
func (r *Repository) Relationships(ctx context.Context, actor string, requests bool, offset, take int) (out []social.Relationship, total int64, err error) {
	out = []social.Relationship{}
	if actor == "" {
		return out, 0, social.ErrForbidden
	}
	if e := social.ValidatePage(offset, take); e != nil {
		return out, 0, e
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		cols := relationColumns
		from := ` FROM owned_social_effective_relationships WHERE account_id=$1`
		if requests {
			cols = `id::text,account_id::text,related_id::text,0::integer AS status,NULL::text AS alias,created_at,created_at AS updated_at,expired_at`
			from = ` FROM owned_social_effective_requests WHERE (account_id=$1 OR related_id=$1)`
		}
		from += ` AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=related_id AND NOT a.disabled) AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=account_id AND NOT a.disabled)`
		// Materialize the time-dependent projection once so the count and page
		// cannot disagree when a deadline passes between separate SQL calls.
		var raw []byte
		if e := tx.QueryRowContext(ctx, `WITH filtered AS MATERIALIZED(SELECT `+cols+from+`),page AS(SELECT * FROM filtered ORDER BY created_at,id OFFSET $2 LIMIT $3) SELECT(SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg(to_jsonb(page) ORDER BY created_at,id) FROM page),'[]'::jsonb)`, actor, offset, take).Scan(&total, &raw); e != nil {
			return e
		}
		if e := json.Unmarshal(raw, &out); e != nil {
			return e
		}

		for i := range out {
			if e := hydrateRelation(ctx, tx, &out[i]); e != nil {
				return e
			}
		}
		return nil
	})
	return
}
func (r *Repository) Relationship(ctx context.Context, actor, target string) (out social.Relationship, err error) {
	if !authority.ValidID(target) || actor == "" {
		return out, social.ErrInvalid
	}
	err = r.read(ctx, actor, func(tx *sql.Tx) error {
		var e error
		out, e = relation(ctx, tx, actor, target)
		if e != nil {
			return e
		}
		return hydrateRelation(ctx, tx, &out)
	})
	return
}
func putRelation(ctx context.Context, tx *sql.Tx, a, b string, status int, now time.Time) (social.Relationship, error) {
	id, e := newID()
	if e != nil {
		return social.Relationship{}, e
	}
	return scanRelationship(tx.QueryRowContext(ctx, `INSERT INTO owned_social_relationships(id,account_id,related_id,status,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(account_id,related_id)DO UPDATE SET status=EXCLUDED.status,updated_at=EXCLUDED.updated_at,expired_at=NULL,degrade_to=NULL,policy_revision=nextval('owned_social_policy_revision') RETURNING `+relationColumns, id, a, b, status, now))
}
func deleteRequests(ctx context.Context, tx *sql.Tx, a, b string) error {
	_, e := tx.ExecContext(ctx, `DELETE FROM owned_social_requests WHERE (account_id=$1 AND related_id=$2) OR (account_id=$2 AND related_id=$1)`, a, b)
	return e
}
func requestFriend(ctx context.Context, tx *sql.Tx, a, b string, now time.Time) (social.Relationship, error) {
	if yes, e := blocked(ctx, tx, a, b); e != nil {
		return social.Relationship{}, e
	} else if yes {
		return social.Relationship{}, social.ErrForbidden
	}
	if v, e := relation(ctx, tx, a, b); e == nil && (v.Status >= social.Friend || v.Status == social.Muted) {
		return social.Relationship{}, social.ErrConflict
	} else if e != nil && !errors.Is(e, social.ErrNotFound) {
		return social.Relationship{}, e
	}
	id, e := newID()
	if e != nil {
		return social.Relationship{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_social_requests(id,account_id,related_id,created_at,expired_at)VALUES($1,$2,$3,$4,$5)`, id, a, b, now, now.Add(7*24*time.Hour))
	expires := now.Add(7 * 24 * time.Hour)
	return social.Relationship{ID: id, AccountID: a, RelatedID: b, Status: social.Pending, CreatedAt: now, UpdatedAt: now, ExpiredAt: &expires}, mapped(e)
}
func (r *Repository) ChangeRelationship(ctx context.Context, actor, target string, c social.RelationshipChange) (out social.Relationship, err error) {
	if !authority.ValidID(target) || actor == target {
		return out, social.ErrInvalid
	}
	if e := social.ValidateRelationshipChange(c); e != nil {
		return out, e
	}
	if c.Alias != nil && !social.ValidText(*c.Alias, 128) {
		return out, social.ErrInvalid
	}
	err = r.write(ctx, actor, []string{target}, social.RelationshipPermissions(c), func(tx *sql.Tx, now time.Time) error {
		var e error
		if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if e = expirePair(ctx, tx, actor, target, now); e != nil {
			return e
		}
		action := c.Action
		if action == social.SetStatus {
			switch c.Status {
			case social.Blocked:
				action = social.Block
			case social.CloseFriend:
				action = social.SetClose
			case social.Friend:
				v, x := relation(ctx, tx, actor, target)
				if x != nil {
					return x
				}
				if v.Status == social.Blocked {
					if _, e = tx.ExecContext(ctx, `DELETE FROM owned_social_relationships WHERE account_id=$1 AND related_id=$2`, actor, target); e != nil {
						return e
					}
					action = social.RequestFriend
				} else {
					action = social.UnsetClose
				}
			default:
				return social.ErrInvalid
			}
		}
		switch action {
		case social.RequestFriend:
			out, e = requestFriend(ctx, tx, actor, target, now)
		case social.AcceptFriend:
			var id string
			if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_social_effective_requests WHERE account_id=$1 AND related_id=$2`, target, actor).Scan(&id); e != nil {
				return mapped(e)
			}
			if yes, x := blocked(ctx, tx, actor, target); x != nil {
				return x
			} else if yes {
				return social.ErrForbidden
			}
			if _, e = putRelation(ctx, tx, target, actor, social.Friend, now); e != nil {
				return e
			}
			out, e = putRelation(ctx, tx, actor, target, social.Friend, now)
			if e == nil {
				e = deleteRequests(ctx, tx, actor, target)
			}
		case social.DeclineFriend, social.CancelFriend:
			a, b := target, actor
			if action == social.CancelFriend {
				a, b = actor, target
			}
			result, x := tx.ExecContext(ctx, `DELETE FROM owned_social_requests WHERE account_id=$1 AND related_id=$2`, a, b)
			if x != nil {
				return x
			}
			n, x := result.RowsAffected()
			if x != nil {
				return x
			}
			if n != 1 {
				return social.ErrNotFound
			}
		case social.Block:
			if e = deleteRequests(ctx, tx, actor, target); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `DELETE FROM owned_social_relationships WHERE ((account_id=$1 AND related_id=$2) OR(account_id=$2 AND related_id=$1)) AND status>=100`, actor, target); e != nil {
				return e
			}
			out, e = putRelation(ctx, tx, actor, target, social.Blocked, now)
		case social.Mute:
			current, x := relation(ctx, tx, actor, target)
			if x == nil && current.Status == social.Muted {
				return social.ErrConflict
			}
			if x != nil && !errors.Is(x, social.ErrNotFound) {
				return x
			}
			// Replacing the sender's pending intent with mute cannot leave a
			// stale request that the recipient could accept over this preference.
			if _, e = tx.ExecContext(ctx, `DELETE FROM owned_social_requests WHERE account_id=$1 AND related_id=$2`, actor, target); e != nil {
				return e
			}
			out, e = putRelation(ctx, tx, actor, target, social.Muted, now)
		case social.Unmute:
			current, x := relation(ctx, tx, actor, target)
			if x != nil {
				return x
			}
			if current.Status != social.Muted {
				return social.ErrConflict
			}
			out, e = putRelation(ctx, tx, actor, target, social.Friend, now)
		case social.Forget:
			// A user can remove their block or friendship, never the other user's block.
			if _, e = tx.ExecContext(ctx, `DELETE FROM owned_social_relationships WHERE (account_id=$1 AND related_id=$2) OR(account_id=$2 AND related_id=$1 AND status>=100)`, actor, target); e != nil {
				return e
			}
			e = deleteRequests(ctx, tx, actor, target)
		case social.SetClose, social.UnsetClose:
			if yes, x := blocked(ctx, tx, actor, target); x != nil {
				return x
			} else if yes {
				return social.ErrForbidden
			}
			v, x := relation(ctx, tx, actor, target)
			if x != nil {
				return x
			}
			reverse, x := relation(ctx, tx, target, actor)
			if x != nil {
				return x
			}
			if v.Status < social.Friend || reverse.Status < social.Friend {
				return social.ErrForbidden
			}
			status := social.Friend
			if action == social.SetClose {
				status = social.CloseFriend
			}
			out, e = putRelation(ctx, tx, actor, target, status, now)
		case social.SetAlias:
			out, e = scanRelationship(tx.QueryRowContext(ctx, `UPDATE owned_social_relationships SET alias=$3,updated_at=$4 WHERE account_id=$1 AND related_id=$2 RETURNING `+relationColumns, actor, target, c.Alias, now))
		default:
			return social.ErrInvalid
		}
		if e != nil {
			return e
		}
		if (action == social.Block || action == social.Mute) && out.ID != "" && c.ExpiresIn != nil {
			duration, x := social.RelationshipDuration(*c.ExpiresIn)
			if x != nil {
				return x
			}
			expires := now.Add(duration)
			if _, e = tx.ExecContext(ctx, `UPDATE owned_social_relationships SET expired_at=$2,degrade_to=$3 WHERE id=$1`, out.ID, expires, c.DegradeTo); e != nil {
				return e
			}
			out.ExpiredAt = &expires
		}
		if out.ID != "" {
			if e = hydrateRelation(ctx, tx, &out); e != nil {
				return e
			}
		}
		return audit(ctx, tx, actor, "relationships."+string(action), target, now)
	})
	return
}
