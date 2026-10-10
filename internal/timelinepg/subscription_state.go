// SPDX-License-Identifier: AGPL-3.0-only
// Authored source and bounded compatibility policy: docs/publisher-subscription-state.md.
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

type SubscriptionNotifications interface {
	EnqueueSubscriptionAdded(context.Context, *sql.Tx, string, string, string) error
}
type SubscriptionStreams interface {
	SubscriptionContent(context.Context, *sql.Tx, string, string) (*time.Time, bool, error)
	SubscribedStreams(context.Context, *sql.Tx, string, int, int) ([]live.Stream, error)
}

func (r *Repository) WithSubscriptionNotifications(n SubscriptionNotifications) *Repository {
	r.subscriptionNotifications = n
	return r
}
func (r *Repository) WithSubscriptionStreams(s SubscriptionStreams) *Repository {
	r.subscriptionStreams = s
	return r
}

var _ timeline.SubscriptionStateRepository = (*Repository)(nil)

// All new routes revalidate the original proof inside the transaction. Actor
// locking serializes own receipt/notify state with revocation and follow writes.
func (r *Repository) subscriptionTransaction(ctx context.Context, actor, permission string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil || r.visibility == nil {
		return posts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	authorize := func() error {
		if r.guard == nil {
			return nil
		}
		if permission == "" {
			_, x := r.guard.Authenticate(ctx, r.db, tx, actor)
			return x
		}
		_, x := r.guard.Authorize(ctx, r.db, tx, actor, permission)
		return x
	}
	if e = authorize(); e != nil {
		return e
	}
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&active); e != nil {
		return missing(e)
	}
	if !active {
		return posts.ErrUnauthenticated
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return e
	}
	if e = authorize(); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) activeSubscription(ctx context.Context, tx *sql.Tx, actor, pub string) (timeline.Subscription, error) {
	v, e := scanSub(tx.QueryRowContext(ctx, `SELECT `+subColumns+` FROM owned_publisher_follows f WHERE f.account_id=$1 AND f.publisher_id=$2 AND f.state='accepted' AND f.ended_at IS NULL`, actor, pub))
	if errors.Is(e, posts.ErrNotFound) {
		return v, timeline.ErrSubscriptionNotFound
	}
	return v, e
}
func (r *Repository) subscriptionContent(ctx context.Context, tx *sql.Tx, actor, pub string) (*time.Time, bool, error) {
	var latest *time.Time
	// Read indicators cover public, published, visible top-level content only.
	e := tx.QueryRowContext(ctx, `SELECT max(COALESCE(p.published_at,p.created_at))`+r.base()+` AND `+accountpg.ActivePublisherOwnerPredicate("u")+` AND p.publisher_id=$2 AND p.visibility=0 AND NOT EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.replied_post_id IS NOT NULL)`, actor, pub).Scan(&latest)
	if e != nil {
		return nil, false, e
	}
	if r.subscriptionStreams == nil {
		return latest, false, nil
	}
	stream, isLive, e := r.subscriptionStreams.SubscriptionContent(ctx, tx, actor, pub)
	if e != nil {
		return nil, false, e
	}
	if stream != nil && (latest == nil || stream.After(*latest)) {
		latest = stream
	}
	return latest, isLive, nil
}
func (r *Repository) hydrateSubscription(ctx context.Context, tx *sql.Tx, viewer string, v *timeline.Subscription) error {
	realms, _ := r.visibility.(accountpg.RealmProvider)
	p, e := accountpg.New(r.db).WithRealms(realms).VisiblePublisherInTransaction(ctx, tx, viewer, v.PublisherID)
	if e != nil {
		return e
	}
	v.Publisher = &p
	return nil
}
func (r *Repository) readSubscription(ctx context.Context, tx *sql.Tx, actor, pub string) (out timeline.SubscriptionReadStatus, e error) {
	out.Subscription, e = r.activeSubscription(ctx, tx, actor, pub)
	if e != nil {
		return
	}
	if e = r.hydrateSubscription(ctx, tx, actor, &out.Subscription); e != nil {
		return out, e
	}
	out.LatestContentAt, _, e = r.subscriptionContent(ctx, tx, actor, pub)
	out.HasNewContent = out.LatestContentAt != nil && (out.Subscription.LastReadAt == nil || out.LatestContentAt.After(*out.Subscription.LastReadAt))
	return
}
func (r *Repository) SubscriptionReadStatus(ctx context.Context, actor, selector string) (out timeline.SubscriptionReadStatus, e error) {
	e = r.subscriptionTransaction(ctx, actor, "", func(tx *sql.Tx, _ time.Time) error {
		pub, _, x := r.publisher(ctx, tx, actor, selector, true)
		if x != nil {
			return x
		}
		out, x = r.readSubscription(ctx, tx, actor, pub)
		return x
	})
	return
}
func (r *Repository) UpdateSubscriptionReadStatus(ctx context.Context, actor, selector string, at *time.Time) (out timeline.SubscriptionReadStatus, e error) {
	if at != nil && (at.Year() < 1 || at.Year() > 9999) {
		return out, timeline.ErrInvalid
	}
	e = r.subscriptionTransaction(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		pub, _, x := r.publisher(ctx, tx, actor, selector, true)
		if x != nil {
			return x
		}
		v, x := r.activeSubscription(ctx, tx, actor, pub)
		if x != nil {
			return x
		}
		stamp := now
		if at != nil {
			stamp = at.UTC().Truncate(time.Microsecond)
		}
		if v.LastReadAt == nil || !v.LastReadAt.Equal(stamp) {
			if _, x = tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET last_read_at=$2,updated_at=$3 WHERE id=$1`, v.ID, stamp, now); x != nil {
				return x
			}
			if x = audit(ctx, tx, actor, "publisher.subscription.read", pub, now); x != nil {
				return x
			}
		}
		out, x = r.readSubscription(ctx, tx, actor, pub)
		return x
	})
	return
}
func (r *Repository) UpdateSubscriptionNotify(ctx context.Context, actor, selector string, notify bool) (out timeline.Subscription, e error) {
	e = r.subscriptionTransaction(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		pub, _, x := r.publisher(ctx, tx, actor, selector, true)
		if x != nil {
			return x
		}
		out, x = r.activeSubscription(ctx, tx, actor, pub)
		if x != nil {
			return x
		}
		if x = r.hydrateSubscription(ctx, tx, actor, &out); x != nil {
			return x
		}
		if out.Notify == notify {
			return nil
		}
		if _, x = tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET notify=$2,updated_at=$3 WHERE id=$1`, out.ID, notify, now); x != nil {
			return x
		}
		out.Notify = notify
		out.UpdatedAt = now
		return audit(ctx, tx, actor, "publisher.subscription.notify", pub, now)
	})
	return
}
func (r *Repository) MarkAllSubscriptionsRead(ctx context.Context, actor string) (updated int, e error) {
	e = r.subscriptionTransaction(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		where := ` FROM owned_publisher_follows f JOIN owned_publishers u ON u.id=f.publisher_id WHERE f.account_id=$1 AND f.state='accepted' AND f.ended_at IS NULL AND u.deleted_at IS NULL AND ` + accountpg.ActivePublisherOwnerPredicate("u") + ` AND ` + postpg.PublisherPredicate(r.visibility) + ` AND NOT (` + r.visibility.PostBlockedSQL() + `)`
		rows, x := tx.QueryContext(ctx, `SELECT f.publisher_id::text`+where+` ORDER BY f.publisher_id`, actor)
		if x != nil {
			return x
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if x = rows.Scan(&id); x != nil {
				rows.Close()
				return x
			}
			ids = append(ids, id)
		}
		x = rows.Err()
		rows.Close()
		if x != nil {
			return x
		}
		// Realm locks precede every publisher lock, in canonical order across the
		// whole batch. An unavailable scope rolls the batch back for a clean retry.
		if _, x = postpg.PrelockPublisherRealms(ctx, tx, r.visibility, ids); x != nil {
			return x
		}
		for _, id := range ids {
			pub, _, x := r.publisher(ctx, tx, actor, id, true)
			if isNotFound(x) {
				continue
			}
			if x != nil {
				return x
			}
			v, x := r.activeSubscription(ctx, tx, actor, pub)
			if errors.Is(x, timeline.ErrSubscriptionNotFound) {
				continue
			}
			if x != nil {
				return x
			}
			latest, _, x := r.subscriptionContent(ctx, tx, actor, pub)
			if x != nil {
				return x
			}
			if latest == nil || v.LastReadAt != nil && !latest.After(*v.LastReadAt) {
				continue
			}
			if _, x = tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET last_read_at=$2,updated_at=$3 WHERE id=$1`, v.ID, *latest, now); x != nil {
				return x
			}
			updated++
		}
		if updated == 0 {
			return nil
		}
		return audit(ctx, tx, actor, "publisher.subscriptions.read_all", "", now)
	})
	if e != nil {
		updated = 0
	}
	return
}
func (r *Repository) Subscribers(ctx context.Context, actor, selector string, offset, take int) (out []timeline.Subscriber, total int64, e error) {
	if offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return nil, 0, timeline.ErrInvalid
	}
	out = []timeline.Subscriber{}
	e = r.subscriptionTransaction(ctx, actor, "", func(tx *sql.Tx, _ time.Time) error {
		pub, _, x := r.publisher(ctx, tx, actor, selector, true)
		if x != nil {
			return x
		}
		if x = r.requireManager(ctx, tx, actor, pub); x != nil {
			return x
		}
		where := ` FROM owned_publisher_follows f JOIN owned_authority_accounts a ON a.id=f.account_id WHERE f.publisher_id=$1 AND f.state='accepted' AND f.ended_at IS NULL AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=a.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=statement_timestamp()) AND(pun.expires_at IS NULL OR pun.expires_at>statement_timestamp()))`
		if x = tx.QueryRowContext(ctx, `SELECT count(*)`+where, pub).Scan(&total); x != nil {
			return x
		}
		rows, x := tx.QueryContext(ctx, `SELECT `+subColumns+where+` ORDER BY f.created_at,f.id OFFSET $2 LIMIT $3`, pub, offset, take)
		if x != nil {
			return x
		}
		for rows.Next() {
			v, x := scanSub(rows)
			if x != nil {
				rows.Close()
				return x
			}
			out = append(out, timeline.Subscriber{Subscription: v})
		}
		x = rows.Err()
		rows.Close()
		if x != nil {
			return x
		}
		for i := range out {
			a, x := accountpg.New(r.db).PublicAccountInTransaction(ctx, tx, out[i].Subscription.AccountID)
			if x != nil {
				return x
			}
			out[i].Account = &a
		}
		return nil
	})
	return
}
func (r *Repository) AddSubscriber(ctx context.Context, actor, selector, target string) (out timeline.Subscriber, e error) {
	if !authority.ValidID(target) {
		return out, timeline.ErrInvalid
	}
	e = r.subscriptionTransaction(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		if r.subscriptionNotifications == nil {
			return posts.ErrUnavailable
		}
		pub, _, x := r.publisher(ctx, tx, actor, selector, true)
		if x != nil {
			return x
		}
		if x = r.requireManager(ctx, tx, actor, pub); x != nil {
			return x
		}
		// Fail boundedly on a concurrent target authority writer, avoiding reversed
		// actor/target lock waits. Recheck visibility after the target lock.
		var active bool
		x = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, target).Scan(&active)
		var pg *pgconn.PgError
		if errors.As(x, &pg) && pg.Code == "55P03" {
			return posts.ErrConflict
		}
		if x != nil {
			return missing(x)
		}
		if !active {
			return posts.ErrNotFound
		}
		if _, _, x = r.publisher(ctx, tx, target, pub, false); x != nil {
			return x
		}
		var exists bool
		if x = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_follows WHERE account_id=$1 AND publisher_id=$2)`, target, pub).Scan(&exists); x != nil {
			return x
		}
		if exists {
			return timeline.ErrSubscriberAdd
		}
		uid, x := id()
		if x != nil {
			return x
		}
		if _, x = tx.ExecContext(ctx, `INSERT INTO owned_publisher_follows(id,account_id,publisher_id,state,notify,created_at,updated_at,incarnation,incarnation_started_at)VALUES($1,$2,$3,'accepted',false,$4,$4,1,$4)`, uid, target, pub, now); x != nil {
			return x
		}
		out.Subscription, x = r.activeSubscription(ctx, tx, target, pub)
		if x != nil {
			return x
		}
		if x = r.hydrateSubscription(ctx, tx, target, &out.Subscription); x != nil {
			return x
		}
		a, x := accountpg.New(r.db).PublicAccountInTransaction(ctx, tx, target)
		if x != nil {
			return x
		}
		out.Account = &a
		if x = r.subscriptionNotifications.EnqueueSubscriptionAdded(ctx, tx, target, pub, uid); x != nil {
			return x
		}
		return audit(ctx, tx, actor, "publisher.subscriber.add", pub, now)
	})
	return
}
func (r *Repository) RemoveSubscriber(ctx context.Context, actor, selector, target string) error {
	if !authority.ValidID(target) {
		return timeline.ErrInvalid
	}
	return r.subscriptionTransaction(ctx, actor, "publishers.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		pub, _, e := r.publisher(ctx, tx, actor, selector, true)
		if e != nil {
			return e
		}
		if e = r.requireManager(ctx, tx, actor, pub); e != nil {
			return e
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE NOWAIT`, target).Scan(&held); e != nil {
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "55P03" {
				return posts.ErrConflict
			}
			return missing(e)
		}
		v, e := r.activeSubscription(ctx, tx, target, pub)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_follows SET ended_at=$2,updated_at=$2,ended_by_account_id=$3 WHERE id=$1`, v.ID, now, actor); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publisher.subscriber.remove", pub, now)
	})
}
func (r *Repository) SubscribedLiveStreams(ctx context.Context, actor string, offset, take int) (out []live.Stream, e error) {
	if offset < 0 || offset > 100000 || take < 1 || take > 100 {
		return nil, timeline.ErrInvalid
	}
	e = r.subscriptionTransaction(ctx, actor, "", func(tx *sql.Tx, _ time.Time) error {
		if r.subscriptionStreams == nil {
			return posts.ErrUnavailable
		}
		var x error
		out, x = r.subscriptionStreams.SubscribedStreams(ctx, tx, actor, offset, take)
		return x
	})
	return
}
