// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"time"
)

const subColumns = `f.id::text,f.account_id::text,f.publisher_id::text,f.state,f.created_at,f.updated_at,f.ended_at,f.reason,f.notify`

func scanSub(s interface{ Scan(...any) error }) (timeline.Subscription, error) {
	var v timeline.Subscription
	e := s.Scan(&v.ID, &v.AccountID, &v.PublisherID, &v.State, &v.CreatedAt, &v.UpdatedAt, &v.EndedAt, &v.Reason, &v.Notify)
	v.IsActive = v.State == "accepted" && v.EndedAt == nil
	v.IsPendingRequest = v.State == "pending" && v.EndedAt == nil
	return v, missing(e)
}
func (r *Repository) subStatus(ctx context.Context, tx *sql.Tx, actor, pub string, approval bool) (timeline.SubscriptionStatus, error) {
	out := timeline.SubscriptionStatus{RequiresApproval: approval, Status: "none", Notify: true}
	v, e := scanSub(tx.QueryRowContext(ctx, `SELECT `+subColumns+` FROM owned_publisher_follows f WHERE f.account_id=$1 AND f.publisher_id=$2`, actor, pub))
	if errors.Is(e, posts.ErrNotFound) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Subscription = &v
	out.Notify = v.Notify
	if approval {
		out.FollowRequest = &v
	}
	if v.EndedAt != nil {
		out.Status = "ended"
		return out, nil
	}
	out.IsPending = v.State == "pending"
	out.IsActive = v.State == "accepted"
	switch v.State {
	case "accepted":
		out.Status = "subscribed"
		if approval {
			out.Status = "following"
		}
	case "pending":
		out.Status = "pending"
	case "rejected":
		out.Status = "rejected"
	}
	return out, nil
}
func (r *Repository) SubscriptionStatus(ctx context.Context, actor, selector string) (timeline.SubscriptionStatus, error) {
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return timeline.SubscriptionStatus{}, e
	}
	defer tx.Rollback()
	pub, approval, e := r.publisher(ctx, tx, actor, selector, false)
	if e != nil {
		return timeline.SubscriptionStatus{}, e
	}
	v, e := r.subStatus(ctx, tx, actor, pub, approval)
	if e == nil {
		e = tx.Commit()
	}
	return v, e
}
func (r *Repository) Subscribe(ctx context.Context, actor, selector string) (timeline.SubscriptionStatus, error) {
	var out timeline.SubscriptionStatus
	e := r.write(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		pub, approval, e := r.publisher(ctx, tx, actor, selector, true)
		if e != nil {
			return e
		}
		existing, e := r.subStatus(ctx, tx, actor, pub, approval)
		if e != nil {
			return e
		}
		if existing.Subscription != nil && existing.Subscription.EndedAt == nil {
			if existing.Subscription.State == "rejected" {
				return posts.ErrForbidden
			}
			out = existing
			return nil
		}
		uid, e := id()
		if e != nil {
			return e
		}
		state := "accepted"
		if approval {
			state = "pending"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_follows(id,account_id,publisher_id,state,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(account_id,publisher_id)DO UPDATE SET state=EXCLUDED.state,reason=NULL,ended_at=NULL,updated_at=EXCLUDED.updated_at`, uid, actor, pub, state, now)
		if e != nil {
			return e
		}
		out, e = r.subStatus(ctx, tx, actor, pub, approval)
		if e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publisher.subscribe", pub, now)
	})
	return out, e
}
func (r *Repository) Unsubscribe(ctx context.Context, actor, selector string) (timeline.SubscriptionStatus, error) {
	var out timeline.SubscriptionStatus
	e := r.write(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		// Cleanup is permitted even after a social block; it never returns publisher
		// content or grants access. Resolve only active canonical publisher identity.
		var pub string
		var approval bool
		e := tx.QueryRowContext(ctx, `SELECT u.id::text,COALESCE(f.requires_approval,false) FROM owned_publishers u LEFT JOIN owned_publisher_follow_policy f ON f.publisher_id=u.id WHERE (u.id::text=$1 OR lower(u.name)=lower($1)) AND u.deleted_at IS NULL FOR UPDATE OF u`, selector).Scan(&pub, &approval)
		if e != nil {
			return missing(e)
		}
		result, e := tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET ended_at=$3,updated_at=$3 WHERE account_id=$1 AND publisher_id=$2 AND ended_at IS NULL`, actor, pub, now)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return posts.ErrNotFound
		}
		out = timeline.SubscriptionStatus{Status: "none", RequiresApproval: approval, Notify: true}
		return audit(ctx, tx, actor, "publisher.unsubscribe", pub, now)
	})
	return out, e
}
func (r *Repository) Subscriptions(ctx context.Context, actor string, offset, take int) ([]timeline.SubscriptionItem, int64, error) {
	if offset < 0 || take < 1 || take > 100 {
		return nil, 0, timeline.ErrInvalid
	}
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	where := ` FROM owned_publisher_follows f JOIN owned_publishers u ON u.id=f.publisher_id JOIN owned_authority_accounts a ON a.id=u.account_id WHERE f.account_id=$1 AND f.ended_at IS NULL AND f.state='accepted' AND u.deleted_at IS NULL AND NOT a.disabled`
	if r.visibility != nil {
		where += ` AND NOT (` + r.visibility.PostBlockedSQL() + `)`
	}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, actor).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+subColumns+`,u.id::text,u.name,u.nick,u.bio,u.type,u.account_id::text,u.realm_id::text,u.created_at,u.updated_at`+where+` ORDER BY f.created_at DESC,f.id DESC OFFSET $2 LIMIT $3`, actor, offset, take)
	if e != nil {
		return nil, 0, e
	}
	out := []timeline.SubscriptionItem{}
	for rows.Next() {
		var v timeline.SubscriptionItem
		v.Publisher = &accounts.Publisher{}
		p := v.Publisher
		e = rows.Scan(&v.ID, &v.AccountID, &v.PublisherID, &v.State, &v.CreatedAt, &v.UpdatedAt, &v.EndedAt, &v.Reason, &v.Notify, &p.ID, &p.Name, &p.Nick, &p.Bio, &p.Type, &p.AccountID, &p.RealmID, &p.CreatedAt, &p.UpdatedAt)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		v.IsActive = true
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		e = tx.QueryRowContext(ctx, `SELECT max(p.published_at)`+r.base()+` AND p.publisher_id=$2 AND NOT EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND(l.replied_post_id IS NOT NULL OR l.chained_post_id IS NOT NULL))`, actor, out[i].PublisherID).Scan(&out[i].LatestContentAt)
		if e != nil {
			return nil, 0, e
		}
		out[i].HasNewContent = out[i].LatestContentAt != nil
	}
	e = tx.Commit()
	return out, total, e
}
func (r *Repository) FollowRequests(ctx context.Context, actor, selector string) ([]timeline.Subscription, error) {
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	pub, _, e := r.publisher(ctx, tx, actor, selector, false)
	if e != nil {
		return nil, e
	}
	var manager bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND role>=100 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, pub, actor).Scan(&manager)
	if e != nil {
		return nil, e
	}
	if !manager {
		return nil, posts.ErrForbidden
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+subColumns+` FROM owned_publisher_follows f JOIN owned_authority_accounts a ON a.id=f.account_id WHERE f.publisher_id=$1 AND f.state='pending' AND f.ended_at IS NULL AND NOT a.disabled ORDER BY f.created_at,f.id LIMIT 501`, pub)
	if e != nil {
		return nil, e
	}
	out := []timeline.Subscription{}
	for rows.Next() {
		v, e := scanSub(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if len(out) > 500 {
		return nil, timeline.ErrInvalid
	}
	if e == nil {
		e = tx.Commit()
	}
	return out, e
}
func (r *Repository) DecideFollow(ctx context.Context, actor, selector, request string, approve bool, reason *string) (timeline.Subscription, error) {
	var out timeline.Subscription
	if !authority.ValidID(request) || reason != nil && (len(*reason) > 1024 || strings.ContainsRune(*reason, 0)) {
		return out, timeline.ErrInvalid
	}
	e := r.write(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		pub, _, e := r.publisher(ctx, tx, actor, selector, true)
		if e != nil {
			return e
		}
		if e = requireManager(ctx, tx, actor, pub); e != nil {
			return e
		}
		// The requested publisher scopes the request ID; one publisher's manager
		// cannot approve another publisher's request by substituting its UUID.
		out, e = scanSub(tx.QueryRowContext(ctx, `SELECT `+subColumns+` FROM owned_publisher_follows f WHERE f.id=$1 AND f.publisher_id=$2 AND f.ended_at IS NULL FOR UPDATE`, request, pub))
		if e != nil {
			return e
		}
		if out.State != "pending" {
			return posts.ErrConflict
		}
		if approve {
			if _, _, e = r.publisher(ctx, tx, out.AccountID, pub, false); e != nil {
				return e
			}
			var active bool
			if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1`, out.AccountID).Scan(&active); e != nil {
				return e
			}
			if !active {
				return posts.ErrNotFound
			}
			out.State = "accepted"
		} else {
			out.State = "rejected"
		}
		out.IsActive = out.State == "accepted"
		out.IsPendingRequest = false
		out.Reason = reason
		out.UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET state=$2,reason=$3,updated_at=$4 WHERE id=$1`, out.ID, out.State, reason, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publisher.follow."+out.State, pub, now)
	})
	return out, e
}
