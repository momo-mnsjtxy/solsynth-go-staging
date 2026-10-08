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
	rows, e := r.db.QueryContext(ctx, `SELECT kind,account_id::text,source_id::text FROM (
 SELECT 'post.create'::text kind,a.actor_id account_id,p.id source_id,p.published_at occurred_at FROM owned_text_posts p JOIN owned_text_post_audit a ON a.post_id=p.id AND a.action='create' WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'post.react',r.account_id,r.id,r.created_at FROM owned_post_reactions r JOIN owned_text_posts p ON p.id=r.post_id WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'post.bookmark',r.account_id,r.id,r.created_at FROM owned_post_bookmarks r JOIN owned_text_posts p ON p.id=r.post_id WHERE p.deleted_at IS NULL AND p.published_at IS NOT NULL
 UNION ALL SELECT 'chat.use',s.account_id,m.id,m.created_at FROM chat_messages m JOIN chat_members s ON s.id=m.sender_id JOIN chat_rooms room ON room.id=m.chat_room_id WHERE m.type='text' AND m.deleted_at IS NULL AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL AND m.owned_request_hash IS NOT NULL
 UNION ALL SELECT 'chatroom.join',m.account_id,m.id,m.joined_at FROM chat_members m JOIN chat_rooms room ON room.id=m.chat_room_id WHERE m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND room.deleted_at IS NULL AND room.owned_owner_id IS NOT NULL
 UNION ALL SELECT 'realm.join',m.account_id,m.id,m.joined_at FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id WHERE m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL
 UNION ALL SELECT 'publisher.create',a.actor_id,p.id,p.created_at FROM owned_publishers p JOIN owned_accounts_audit a ON a.target_id=p.id AND a.action='publishers.create' WHERE p.deleted_at IS NULL
 UNION ALL SELECT 'account.active',p.account_id,p.id,p.occurred_at FROM owned_presence_activity_days p
 UNION ALL SELECT 'relationship.friend_established',r.account_id,r.id,r.created_at FROM owned_social_relationships r WHERE r.status>=100 AND EXISTS(SELECT 1 FROM owned_social_relationships reverse WHERE reverse.account_id=r.related_id AND reverse.related_id=r.account_id AND reverse.status>=100)
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
