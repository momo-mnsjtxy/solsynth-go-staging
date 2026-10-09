// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/passport/progression"
	"time"
)

// ProcessDue projects a bounded batch of committed canonical records. Durable
// anti-join receipts avoid a high-watermark race with transactions that commit
// out of order. The source is checked again under the canonical account lock.
// This is a trusted worker capability, not a request or client event endpoint.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return 0, progression.ErrUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, progression.ErrInvalid
	}
	if e := r.materializeFeatured(ctx); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT kind,account_id::text,source_id::text FROM (
 SELECT 'post.create'::text kind,a.actor_id account_id,p.id source_id,p.published_at occurred_at FROM owned_text_posts p JOIN owned_text_post_audit a ON a.post_id=p.id AND a.action='create' WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'post.react',r.account_id,r.id,r.created_at FROM owned_post_reactions r JOIN owned_text_posts p ON p.id=r.post_id WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'post.bookmark',r.account_id,r.id,r.created_at FROM owned_post_bookmarks r JOIN owned_text_posts p ON p.id=r.post_id WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT s.kind,s.account_id,s.id,s.occurred_at FROM owned_post_progression_sources s WHERE s.account_id IS NOT NULL
 UNION ALL SELECT 'stellar.support_month',m.account_id,m.id,m.occurred_at FROM commerce_v1.stellar_support_months m
 UNION ALL SELECT 'chat.use',s.account_id,m.id,m.created_at FROM chat_messages m JOIN chat_members s ON s.id=m.sender_id JOIN chat_rooms room ON room.id=m.chat_room_id WHERE m.type='text' AND m.deleted_at IS NULL AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL AND m.owned_request_hash IS NOT NULL
 UNION ALL SELECT 'chatroom.join',m.account_id,m.id,m.joined_at FROM chat_members m JOIN chat_rooms room ON room.id=m.chat_room_id WHERE m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL
 UNION ALL SELECT 'realm.join',m.account_id,m.id,m.joined_at FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id WHERE m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL
 UNION ALL SELECT 'publisher.create',a.actor_id,p.id,p.created_at FROM owned_publishers p JOIN owned_accounts_audit a ON a.target_id=p.id AND a.action='publishers.create' WHERE p.deleted_at IS NULL
 UNION ALL SELECT 'account.active',p.account_id,p.id,p.occurred_at FROM owned_presence_activity_days p
 UNION ALL SELECT 'relationship.friend_established',r.account_id,r.id,r.created_at FROM owned_social_effective_relationships r WHERE r.status>=100 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships reverse WHERE reverse.account_id=r.related_id AND reverse.related_id=r.account_id AND reverse.status>=100)
 ) c WHERE occurred_at<=clock_timestamp() AND EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=c.account_id) AND NOT EXISTS(SELECT 1 FROM owned_progression_projection_receipts p WHERE p.kind=c.kind AND p.account_id=c.account_id AND p.source_id=c.source_id) ORDER BY occurred_at,kind,source_id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type item struct{ kind, actor, source string }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.kind, &v.actor, &v.source); e != nil {
			rows.Close()
			return 0, e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	count := 0
	for _, item := range items {
		applied := false
		e = r.within(ctx, "", nil, func(tx *sql.Tx, now time.Time) error {
			var disabled bool
			if e := tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, item.actor).Scan(&disabled); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
				return e
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO owned_progression_projection_receipts(kind,account_id,source_id,processed_at,outcome)VALUES($1,$2,$3,$4,'applied')ON CONFLICT DO NOTHING`, item.kind, item.actor, item.source, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil || n == 0 {
				return e
			}
			applied = true
			outcome := "applied"
			if !disabled {
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_authority_punishments WHERE account_id=$1 AND kind='disable_account' AND(starts_at IS NULL OR starts_at<=$2)AND(expires_at IS NULL OR expires_at>$2))`, item.actor, now).Scan(&disabled); e != nil {
					return e
				}
			}
			if disabled {
				outcome = "disabled"
			} else {
				e = r.applyCanonical(ctx, tx, item.actor, item.kind, item.source, now)
				if errors.Is(e, progression.ErrNotFound) {
					outcome = "gone"
				} else if e != nil {
					return e
				}
			}
			_, e = tx.ExecContext(ctx, `UPDATE owned_progression_projection_receipts SET outcome=$4 WHERE kind=$1 AND account_id=$2 AND source_id=$3`, item.kind, item.actor, item.source, outcome)
			return e
		})
		if e != nil {
			return count, e
		}
		if applied {
			count++
		}
	}
	return count, nil
}

// materializeFeatured records one durable UTC-day snapshot per call. The
// persisted watermark makes repeated calls catch up days missed during worker
// downtime, while a single transaction makes ranking and watermark atomic.
func (r *Repository) materializeFeatured(ctx context.Context) error {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('progression:post.featured',0))`); e != nil {
		return e
	}
	var today time.Time
	if e = tx.QueryRowContext(ctx, `SELECT date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC')`).Scan(&today); e != nil {
		return e
	}
	yesterday := today.UTC().AddDate(0, 0, -1)
	day := yesterday
	var last time.Time
	e = tx.QueryRowContext(ctx, `SELECT day FROM owned_post_featured_days ORDER BY day DESC LIMIT 1 FOR UPDATE`).Scan(&last)
	if e == nil {
		day = last.UTC().AddDate(0, 0, 1)
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if day.After(yesterday) {
		return tx.Commit()
	}
	// Claim the UTC day inside this transaction before selecting candidates.
	// A rollback removes both the claim and every source row.
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_featured_days(day,completed_at,candidate_count)VALUES($1::date,clock_timestamp(),0)`, day); e != nil {
		return e
	}
	// Canonical writers acquire account/realm/publisher/post locks in this order.
	// Take broad day-window share locks in the same order before the ranking query
	// and its foreign-key checks so background materialization cannot invert it.
	for _, lock := range []string{
		`SELECT a.id FROM owned_authority_accounts a WHERE EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id LEFT JOIN owned_realms realm ON realm.id=u.realm_id WHERE COALESCE(u.account_id,realm.account_id)=a.id AND p.created_at >= ($1::date)::timestamp AT TIME ZONE 'UTC' AND p.created_at < ($1::date+1)::timestamp AT TIME ZONE 'UTC') ORDER BY a.id FOR SHARE`,
		`SELECT realm.id FROM owned_realms realm WHERE EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE u.realm_id=realm.id AND p.created_at >= ($1::date)::timestamp AT TIME ZONE 'UTC' AND p.created_at < ($1::date+1)::timestamp AT TIME ZONE 'UTC') ORDER BY realm.id FOR SHARE`,
		`SELECT u.id FROM owned_publishers u WHERE EXISTS(SELECT 1 FROM owned_text_posts p WHERE p.publisher_id=u.id AND p.created_at >= ($1::date)::timestamp AT TIME ZONE 'UTC' AND p.created_at < ($1::date+1)::timestamp AT TIME ZONE 'UTC') ORDER BY u.id FOR SHARE`,
		`SELECT p.id FROM owned_text_posts p WHERE p.created_at >= ($1::date)::timestamp AT TIME ZONE 'UTC' AND p.created_at < ($1::date+1)::timestamp AT TIME ZONE 'UTC' ORDER BY p.id FOR SHARE`,
	} {
		rows, lockErr := tx.QueryContext(ctx, lock, day)
		if lockErr != nil {
			return lockErr
		}
		for rows.Next() {
			var ignored string
			if lockErr = rows.Scan(&ignored); lockErr != nil {
				rows.Close()
				return lockErr
			}
		}
		lockErr = rows.Err()
		rows.Close()
		if lockErr != nil {
			return lockErr
		}
	}
	_, e = tx.ExecContext(ctx, `WITH bounds AS (
	 SELECT ($1::date)::timestamp AT TIME ZONE 'UTC' start_at,
	        ($1::date+1)::timestamp AT TIME ZONE 'UTC' end_at
	), eligible AS (
	 SELECT p.id,p.publisher_id,u.account_id,p.published_at,p.created_at,
	  COALESCE((SELECT sum(CASE WHEN reaction.attitude=0 THEN 1 ELSE -1 END) FROM owned_post_reactions reaction WHERE reaction.post_id=p.id),0)
	  +COALESCE((SELECT count(*) FROM owned_post_relations relation JOIN owned_text_posts reply ON reply.id=relation.post_id WHERE relation.replied_post_id=p.id AND reply.deleted_at IS NULL),0)
	  +trunc(p.awarded_score/10) score
	 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id
	 LEFT JOIN owned_realms realm ON realm.id=u.realm_id AND realm.deleted_at IS NULL
	 JOIN owned_authority_accounts a ON a.id=COALESCE(u.account_id,realm.account_id) CROSS JOIN bounds b
	 WHERE p.deleted_at IS NULL AND p.visibility=0 AND p.drafted_at IS NULL AND p.published_at IS NOT NULL AND p.published_at<b.end_at
	  AND p.created_at>=b.start_at AND p.created_at<b.end_at AND p.shadowbanned_at IS NULL
	  AND u.deleted_at IS NULL AND u.shadowbanned_at IS NULL AND (u.type=0 OR (u.type=1 AND realm.is_public)) AND NOT a.disabled
	  AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=statement_timestamp())AND(punishment.expires_at IS NULL OR punishment.expires_at>statement_timestamp()))
	  AND NOT EXISTS(SELECT 1 FROM owned_publisher_features gate WHERE gate.publisher_id=p.publisher_id AND gate.flag='postsRequireFollow' AND(gate.expired_at IS NULL OR gate.expired_at>statement_timestamp()))
	), ranked AS (
	 SELECT * FROM eligible ORDER BY score DESC,created_at DESC,id DESC LIMIT 5
	)
	INSERT INTO owned_post_progression_sources(id,kind,post_id,target_publisher_id,action_publisher_id,account_id,published_at,score,featured_day,featured_rank,occurred_at)
	 SELECT gen_random_uuid(),'post.featured',id,publisher_id,NULL,account_id,published_at,score,$1::date,featured_rank,($1::date+1)::timestamp AT TIME ZONE 'UTC'
	 FROM(SELECT ranked.*,row_number() OVER(ORDER BY score DESC,created_at DESC,id DESC)::smallint featured_rank FROM ranked) chosen WHERE account_id IS NOT NULL`, day)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_post_featured_days SET completed_at=clock_timestamp(),candidate_count=(SELECT count(*) FROM owned_post_progression_sources WHERE kind='post.featured' AND featured_day=$1::date) WHERE day=$1::date`, day)
	if e != nil {
		return e
	}
	return tx.Commit()
}

// CompletionSink receives only committed earned-reward packets. Delivery is at
// least once after a crash; eventID is stable and lets the transport deduplicate.
type CompletionSink interface {
	DeliverProgression(context.Context, string, string, json.RawMessage) error
}

func (r *Repository) DispatchCompletions(ctx context.Context, limit int, sink CompletionSink) (int, error) {
	if r == nil || r.db == nil || sink == nil {
		return 0, progression.ErrUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, progression.ErrInvalid
	}
	count := 0
	for count < limit {
		sent := false
		err := r.within(ctx, "", nil, func(tx *sql.Tx, now time.Time) error {
			var id, actor, grant string
			var payload json.RawMessage
			e := tx.QueryRowContext(ctx, `SELECT o.id::text,o.account_id::text,o.grant_id::text,o.payload FROM owned_progression_outbox o JOIN owned_authority_accounts a ON a.id=o.account_id WHERE o.delivered_at IS NULL AND NOT a.disabled ORDER BY o.created_at,o.id LIMIT 1 FOR UPDATE OF o SKIP LOCKED`).Scan(&id, &actor, &grant, &payload)
			if errors.Is(e, sql.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if e = sink.DeliverProgression(bounded, actor, id, payload); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_progression_outbox SET delivered_at=$2 WHERE id=$1`, id, now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_progression_grants SET notification_sent_at=$2,updated_at=$2 WHERE id=$1`, grant, now); e != nil {
				return e
			}
			sent = true
			return nil
		})
		if err != nil {
			return count, err
		}
		if !sent {
			return count, nil
		}
		count++
	}
	return count, nil
}
