// SPDX-License-Identifier: AGPL-3.0-only
// Post publication captures reply/chain watcher intents in its own transaction.
// ProcessDue projects them later, after ordered recipient/source locks, without
// contacting providers while a publication transaction holds domain locks.
package publishernotificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
)

const watchWitnessesSQL = `SELECT COALESCE(jsonb_agg(jsonb_build_object('source',source,'id',id,'post',post_id) ORDER BY source,id),'[]'::jsonb) FROM (
 SELECT 'bookmark'::text source,b.id,b.post_id FROM owned_post_bookmarks b WHERE b.account_id=$1 AND b.post_id=ANY($2::uuid[])
 UNION SELECT 'reaction',r.id,r.post_id FROM owned_post_reactions r WHERE r.account_id=$1 AND r.post_id=ANY($2::uuid[])
 UNION SELECT 'reply',p.id,l.replied_post_id FROM owned_post_relations l JOIN owned_text_posts p ON p.id=l.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE u.account_id=$1 AND l.replied_post_id=ANY($2::uuid[]) AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=clock_timestamp() AND u.deleted_at IS NULL
) witness`

func (r *Repository) recordWatchPublication(ctx context.Context, tx *sql.Tx, p posts.Post) error {
	var reply, chain *string
	if e := tx.QueryRowContext(ctx, `SELECT replied_post_id::text,chained_post_id::text FROM owned_post_relations WHERE post_id=$1`, p.ID).Scan(&reply, &chain); errors.Is(e, sql.ErrNoRows) {
		return nil
	} else if e != nil {
		return e
	}
	timeline := timelinepg.New(r.db).WithVisibility(r.visibility)
	for _, trigger := range []struct {
		head  *string
		event sphere.WatchEvent
	}{{reply, sphere.WatchReplies}, {chain, sphere.WatchChains}} {
		if trigger.head == nil {
			continue
		}
		targets := []string{*trigger.head}
		if trigger.event == sphere.WatchChains {
			rows, e := tx.QueryContext(ctx, `SELECT p.id::text FROM owned_text_posts p WHERE p.id=$1 OR EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.chained_post_id=$1) ORDER BY p.id`, *trigger.head)
			if e != nil {
				return e
			}
			targets = nil
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				targets = append(targets, id)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		accounts, e := timeline.PublicationWatcherAccounts(ctx, tx, targets, trigger.event)
		if e != nil {
			return e
		}
		for _, account := range accounts {
			if p.Publisher.AccountID != nil && account == *p.Publisher.AccountID {
				continue
			}
			var witnesses []byte
			if e = tx.QueryRowContext(ctx, watchWitnessesSQL, account, targets).Scan(&witnesses); e != nil {
				return e
			}
			var captured []struct{ Post string }
			if e = json.Unmarshal(witnesses, &captured); e != nil {
				return e
			}
			// Watch membership may be removed between account resolution and
			// capture. It must not turn an otherwise valid publication into an
			// empty-witness constraint failure.
			if len(captured) == 0 {
				continue
			}
			// Only this recipient's watched posts and the head affect its
			// decision. Avoid copying and locking an entire large chain once
			// per recipient; reply-witness posts join the dependency plan later.
			dependencies := []string{*trigger.head}
			for _, witness := range captured {
				dependencies = append(dependencies, witness.Post)
			}
			dependencies = uniqueSorted(dependencies...)
			id, e := uuid()
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_watch_notification_intents(id,publication_id,account_id,event,head_id,targets,witnesses)
 SELECT $1,i.id,$2,$3,$4,$5,$6::jsonb FROM owned_publisher_content_notification_intents i WHERE i.kind='post.published' AND i.source_id=$7
 ON CONFLICT(publication_id,account_id,event) DO NOTHING`, id, account, string(trigger.event), *trigger.head, dependencies, witnesses, p.ID)
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func (r *Repository) watchReadableSQL(account string) string {
	accountID := `NULLIF(` + account + `,'')::uuid`
	return `p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=clock_timestamp() AND p.shadowbanned_at IS NULL AND p.locked_at IS NULL AND u.deleted_at IS NULL AND u.shadowbanned_at IS NULL AND ` + accountpg.ActivePublisherOwnerPredicate("u") + ` AND ` + accountpg.PublisherRelationshipPredicate("u", account, true) + ` AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships muted WHERE muted.status=-50 AND muted.related_id=` + accountID + ` AND muted.account_id=COALESCE(u.account_id,(SELECT realm_owner.account_id FROM owned_realms realm_owner WHERE realm_owner.id=u.realm_id))) AND ` + strings.ReplaceAll(postpg.DetailPredicate(r.visibility), "$1", account)
}

// The same fixed expression is used before/after inbox projection and on every
// inbox/replay read. A captured reaction/bookmark ID cannot be replaced by a new
// watch, and current preferences can silence an already queued notification.
func (r *Repository) watchEligibilitySQL(account string) string {
	if r == nil || r.visibility == nil {
		return `FALSE`
	}
	// DetailPredicate deliberately accepts a text actor (including an empty
	// anonymous value). Cast UUID comparisons individually so its NULLIF never
	// tries to parse the empty-string sentinel as a UUID.
	account = `(` + account + `)::text`
	accountID := `NULLIF(` + account + `,'')::uuid`
	readable := r.watchReadableSQL(account)
	return `NOT q.suppressed AND q.account_id=` + accountID + ` AND EXISTS(SELECT 1 FROM owned_authority_accounts recipient WHERE recipient.id=q.account_id AND NOT recipient.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=recipient.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=clock_timestamp())AND(pun.expires_at IS NULL OR pun.expires_at>clock_timestamp())))
 AND EXISTS(SELECT 1 FROM owned_publisher_content_notification_intents i JOIN owned_text_posts p ON p.id=i.post_id JOIN owned_publishers u ON u.id=p.publisher_id JOIN owned_post_relations relation ON relation.post_id=p.id WHERE i.id=q.publication_id AND p.published_at=i.occurred_at AND (u.account_id IS NULL OR u.account_id<>q.account_id) AND ((q.event='replies' AND relation.replied_post_id=q.head_id) OR(q.event='chains' AND relation.chained_post_id=q.head_id)) AND ` + readable + `)
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(q.witnesses) witness LEFT JOIN owned_post_watch_preferences pref ON pref.account_id=q.account_id AND pref.source=witness->>'source'
 WHERE CASE WHEN q.event='chains' THEN COALESCE(pref.notify_chains,true) ELSE COALESCE(pref.notify_replies,witness->>'source'='bookmark') END
 AND (q.event<>'chains' OR (witness->>'post')::uuid=q.head_id OR EXISTS(SELECT 1 FROM owned_post_relations chain_member WHERE chain_member.post_id=(witness->>'post')::uuid AND chain_member.chained_post_id=q.head_id))
 AND EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=(witness->>'post')::uuid AND ` + readable + `)
 AND ((witness->>'source'='bookmark' AND EXISTS(SELECT 1 FROM owned_post_bookmarks b WHERE b.id=(witness->>'id')::uuid AND b.account_id=q.account_id AND b.post_id=(witness->>'post')::uuid))
 OR (witness->>'source'='reaction' AND EXISTS(SELECT 1 FROM owned_post_reactions reaction WHERE reaction.id=(witness->>'id')::uuid AND reaction.account_id=q.account_id AND reaction.post_id=(witness->>'post')::uuid))
 OR (witness->>'source'='reply' AND EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id JOIN owned_post_relations relation ON relation.post_id=p.id WHERE p.id=(witness->>'id')::uuid AND u.account_id=q.account_id AND relation.replied_post_id=(witness->>'post')::uuid AND ` + readable + `))))`
}

func (r *Repository) processWatchOne(ctx context.Context) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var id, account, source, event string
	e = tx.QueryRowContext(ctx, `SELECT q.id::text,q.account_id::text,i.source_id::text,q.event FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id WHERE q.processed_at IS NULL ORDER BY q.created_at,q.id LIMIT 1`).Scan(&id, &account, &source, &event)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	plan, e := observeWatchDependencies(ctx, tx, id)
	if e != nil {
		return false, e
	}
	if e = plan.lock(ctx, tx, account); e != nil {
		return false, e
	}
	var locked string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_post_watch_notification_intents WHERE id=$1 AND processed_at IS NULL FOR UPDATE SKIP LOCKED`, id).Scan(&locked); errors.Is(e, sql.ErrNoRows) {
		return false, nil
	} else if e != nil {
		return false, e
	}
	if e = plan.verify(ctx, tx, id); e != nil {
		return false, e
	}
	eligible := func() (bool, error) {
		var allowed bool
		e := tx.QueryRowContext(ctx, `SELECT `+r.watchEligibilitySQL("$1::text")+` FROM owned_post_watch_notification_intents q WHERE q.id=$2`, account, id).Scan(&allowed)
		return allowed, e
	}
	allowed, e := eligible()
	if e != nil {
		return false, e
	}
	var notification *string
	if allowed {
		var publisher, name, nick, content, head string
		if e = tx.QueryRowContext(ctx, `SELECT u.id::text,u.name,u.nick,COALESCE(NULLIF(p.description,''),p.content),q.head_id::text FROM owned_post_watch_notification_intents q JOIN owned_publisher_content_notification_intents i ON i.id=q.publication_id JOIN owned_text_posts p ON p.id=i.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE q.id=$1`, id).Scan(&publisher, &name, &nick, &content, &head); e != nil {
			return false, e
		}
		eventType, title := "reply", nick+" replied to a post you watch"
		if event == "chains" {
			eventType, title = "chain", nick+" added a new post to a chain you watch"
		}
		meta, _ := json.Marshal(map[string]string{"post_id": source, "action_uri": "/posts/" + source, "notification_type": "post_watch", "watch_event_type": eventType, "chain_head_id": head, "actor_publisher_id": publisher, "actor_publisher_name": name, "actor_publisher_nick": nick})
		key := "publisher-content:watch:" + id
		// The complete account/domain lock plan is already held. Enqueue only
		// reacquires this recipient's account lock in the same transaction.
		projected, e := r.sink.EnqueueInTransaction(ctx, r.db, tx, notifications.Command{Key: key, AccountID: account, AppID: r.app, Topic: "posts.watch." + event, Title: clipUTF8(title, 1024), Content: clipUTF8(content, 512), Meta: meta, External: true})
		if e != nil {
			return false, e
		}
		notification = &projected
		if e = plan.verify(ctx, tx, id); e != nil {
			return false, e
		}
		current, e := eligible()
		if e != nil {
			return false, e
		}
		if !current {
			return false, notifications.ErrConflict
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_post_watch_notification_intents SET processed_at=clock_timestamp(),suppressed=$2,notification_id=$3 WHERE id=$1`, id, !allowed || notification == nil, notification)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// The reserved publisher-content lane calls this before taking a standalone
// recipient lock. Rebuild and lock the complete sorted plan for both inbox
// projection and external delivery; provider I/O happens after its lease commits.
func (r *Repository) watchDeliveryAllowed(ctx context.Context, tx *sql.Tx, account, notification string) (found, allowed bool, err error) {
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_post_watch_notification_intents WHERE notification_id=$1 AND account_id=$2`, notification, account).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	plan, err := observeWatchDependencies(ctx, tx, id)
	if err != nil {
		return true, false, err
	}
	if err = plan.lock(ctx, tx, account); err != nil {
		return true, false, err
	}
	if err = plan.verify(ctx, tx, id); err != nil {
		return true, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT `+r.watchEligibilitySQL("$1::text")+` FROM owned_post_watch_notification_intents q WHERE q.id=$2`, account, id).Scan(&allowed)
	return true, allowed, err
}
