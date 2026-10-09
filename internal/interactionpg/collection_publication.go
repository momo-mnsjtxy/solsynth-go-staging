// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/sphere/posts"
)

const MaxPublicationSubscriptions = 4096

// WithPublicationNotifications binds the durable intent to one local app.
// Configure before serving requests. There is no external delivery capability.
func (r *Repository) WithPublicationNotifications(app string) *Repository {
	r.notificationApp = app
	return r
}

type PublicationNotificationSink interface {
	ProjectLocalInTransaction(context.Context, *sql.DB, *sql.Tx, notifications.Command) error
}

// RecordPublication is invoked only by canonical create/draft-to-published/due
// transitions. Snapshot exact subscription incarnations in the same transaction;
// an ordinary edit or adding an already-published post to a collection is silent.
func (r *Repository) RecordPublication(ctx context.Context, db *sql.DB, tx *sql.Tx, p posts.Post) error {
	if r == nil || r.db != db || tx == nil || !notifications.ValidApp(r.notificationApp) {
		return posts.ErrUnavailable
	}
	if p.Publisher.Type != 0 || p.Publisher.AccountID == nil || p.DraftedAt != nil || p.PublishedAt == nil || p.DeletedAt != nil || (p.Visibility != posts.Public && p.Visibility != posts.Unlisted) || p.RepliedPostID != nil {
		return nil
	}
	var eligible bool
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=$1 AND p.shadowbanned_at IS NULL AND u.shadowbanned_at IS NULL AND NOT EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.replied_post_id IS NOT NULL))`, p.ID).Scan(&eligible); e != nil {
		return e
	}
	if !eligible {
		return nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT s.id::text,s.account_id::text,s.collection_id::text FROM owned_taxonomy.post_category_subscriptions s JOIN owned_post_collections c ON c.id=s.collection_id JOIN owned_post_collection_items i ON i.collection_id=c.id WHERE i.post_id=$1 AND c.publisher_id=$2 AND c.deleted_at IS NULL AND s.deleted_at IS NULL ORDER BY s.account_id,s.id LIMIT $3`, p.ID, p.PublisherID, MaxPublicationSubscriptions+1)
	if e != nil {
		return e
	}
	type target struct{ subscription, account, collection string }
	targets := []target{}
	for rows.Next() {
		var v target
		if e = rows.Scan(&v.subscription, &v.account, &v.collection); e != nil {
			rows.Close()
			return e
		}
		targets = append(targets, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(targets) > MaxPublicationSubscriptions {
		return posts.ErrConflict
	}
	if len(targets) == 0 {
		return nil
	}
	publication, e := newID()
	if e != nil {
		return e
	}
	// Duplicate hooks never refill the audience with later subscribers.
	e = tx.QueryRowContext(ctx, `INSERT INTO owned_collection_publications(id,post_id,publisher_id,owner_id,published_at)VALUES($1,$2,$3,$4,$5) ON CONFLICT(post_id,published_at) DO NOTHING RETURNING id::text`, publication, p.ID, p.PublisherID, *p.Publisher.AccountID, p.PublishedAt).Scan(&publication)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	prior, notification := "", ""
	for _, v := range targets {
		if v.account != prior {
			notification, e = newID()
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_collection_publication_notifications(id,publication_id,account_id,app_id)VALUES($1,$2,$3,$4)`, notification, publication, v.account, r.notificationApp)
			if e != nil {
				return e
			}
			prior = v.account
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_collection_publication_subscriptions(notification_id,subscription_id,collection_id)VALUES($1,$2,$3)`, notification, v.subscription, v.collection); e != nil {
			return e
		}
	}
	return nil
}

// The same fixed SQL gates worker projection, inbox counts/pages, and replay.
// Reads evaluate it in one current statement snapshot after authority waits;
// no source content is stored in the queue, inbox, or stream. This is a stricter
// owned policy: all direct source posts/media must remain currently readable.
func (r *Repository) publicationEligibilitySQL() string {
	if r == nil || r.visibility == nil {
		return `FALSE`
	}
	visible := postpg.DetailPredicate(r.visibility)
	sourceVisible := `p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=clock_timestamp() AND p.shadowbanned_at IS NULL AND p.locked_at IS NULL AND u.deleted_at IS NULL AND u.shadowbanned_at IS NULL AND ` + accountpg.ActivePublisherOwnerPredicate("u") + ` AND ` + visible
	return `q.account_id=NULLIF($1::text,'')::uuid AND q.app_id=$2 AND q.suppressed_at IS NULL
 AND EXISTS(SELECT 1 FROM owned_authority_accounts recipient WHERE recipient.id=q.account_id AND NOT recipient.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=recipient.id AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())))
 AND EXISTS(SELECT 1 FROM owned_collection_publications publication JOIN owned_text_posts p ON p.id=publication.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE publication.id=q.publication_id AND p.publisher_id=publication.publisher_id AND u.account_id=publication.owner_id AND u.type=0 AND p.published_at=publication.published_at AND p.visibility IN(0,2) AND ` + sourceVisible + `
 AND NOT EXISTS(SELECT 1 FROM owned_post_relations reply WHERE reply.post_id=p.id AND reply.replied_post_id IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships mute WHERE mute.status=-50 AND ((mute.account_id=u.account_id AND mute.related_id=q.account_id) OR(mute.related_id=u.account_id AND mute.account_id=q.account_id)))
 AND EXISTS(SELECT 1 FROM owned_collection_publication_subscriptions captured JOIN owned_taxonomy.post_category_subscriptions live ON live.id=captured.subscription_id AND live.collection_id=captured.collection_id AND live.account_id=q.account_id JOIN owned_post_collections c ON c.id=captured.collection_id JOIN owned_post_collection_items item ON item.collection_id=c.id AND item.post_id=p.id WHERE captured.notification_id=q.id AND live.deleted_at IS NULL AND c.deleted_at IS NULL AND c.publisher_id=publication.publisher_id)
 AND NOT EXISTS(SELECT 1 FROM owned_post_relations relation CROSS JOIN LATERAL unnest(ARRAY[relation.replied_post_id,relation.forwarded_post_id,relation.chained_post_id]) source_id WHERE relation.post_id=publication.post_id AND source_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM owned_text_posts p JOIN owned_publishers u ON u.id=p.publisher_id WHERE p.id=source_id AND ` + sourceVisible + `))
 AND NOT EXISTS(SELECT 1 FROM (SELECT publication.post_id AS id UNION SELECT source_id FROM owned_post_relations relation CROSS JOIN LATERAL unnest(ARRAY[relation.replied_post_id,relation.forwarded_post_id,relation.chained_post_id]) source_id WHERE relation.post_id=publication.post_id AND source_id IS NOT NULL) targets JOIN owned_text_posts source_post ON source_post.id=targets.id CROSS JOIN LATERAL (SELECT l.file_id,l.owner_authorized FROM owned_post_media l WHERE l.post_id=targets.id UNION ALL SELECT t.file_id,EXISTS(SELECT 1 FROM owned_post_media media_grant WHERE media_grant.post_id=t.post_id AND media_grant.file_id=t.file_id AND media_grant.owner_authorized) FROM owned_post_thumbnails t WHERE t.post_id=targets.id) link LEFT JOIN owned_media_files f ON f.id=link.file_id LEFT JOIN owned_authority_accounts file_owner ON file_owner.id=f.account_id WHERE f.id IS NULL OR f.state<>'ready' OR file_owner.id IS NULL OR file_owner.disabled OR NOT(f.is_public OR f.account_id=q.account_id OR link.owner_authorized) OR EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=f.account_id AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())))
 AND NOT EXISTS(SELECT 1 FROM (SELECT p.realm_id AS id UNION SELECT u.realm_id UNION SELECT nested.realm_id FROM owned_post_relations relation CROSS JOIN LATERAL unnest(ARRAY[relation.replied_post_id,relation.forwarded_post_id,relation.chained_post_id]) source_id JOIN owned_text_posts nested ON nested.id=source_id WHERE relation.post_id=publication.post_id UNION SELECT nested_pub.realm_id FROM owned_post_relations relation CROSS JOIN LATERAL unnest(ARRAY[relation.replied_post_id,relation.forwarded_post_id,relation.chained_post_id]) source_id JOIN owned_text_posts nested ON nested.id=source_id JOIN owned_publishers nested_pub ON nested_pub.id=nested.publisher_id WHERE relation.post_id=publication.post_id) realms JOIN owned_realms realm ON realm.id=realms.id JOIN owned_authority_accounts realm_owner ON realm_owner.id=realm.account_id WHERE realm_owner.disabled OR EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=realm_owner.id AND x.kind='disable_account' AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())))
 )`
}

// CollectionPublicationVisibleSQL accepts only the canonical inbox alias and
// actor/app parameters $1/$2. It is never constructed from request text.
func (r *Repository) CollectionPublicationVisibleSQL() string {
	return `EXISTS(SELECT 1 FROM owned_collection_publication_notifications q WHERE q.id::text=substring(inbox.source_key FROM 17) AND ` + r.publicationEligibilitySQL() + `)`
}

func (r *Repository) ProcessPublicationNotifications(ctx context.Context, sink PublicationNotificationSink, app string, limit int) (int, error) {
	if r == nil || r.db == nil || sink == nil || r.visibility == nil || !notifications.ValidApp(app) || app != r.notificationApp {
		return 0, posts.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, posts.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, e := r.db.QueryContext(ctx, `SELECT id::text,account_id::text FROM owned_collection_publication_notifications WHERE app_id=$1 AND processed_at IS NULL ORDER BY created_at,id LIMIT $2`, app, limit)
	if e != nil {
		return 0, e
	}
	type item struct{ id, account string }
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.account); e != nil {
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
	n := 0
	for _, v := range items {
		done, e := r.processPublicationNotification(ctx, sink, app, v.id, v.account)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}
func (r *Repository) processPublicationNotification(ctx context.Context, sink PublicationNotificationSink, app, id, account string) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	plan, e := observePublicationDependencies(ctx, tx, id)
	if e != nil {
		return false, e
	}
	if e = plan.lock(ctx, tx, account); e != nil {
		return false, e
	}
	if e = plan.verify(ctx, tx, id); e != nil {
		return false, e
	}
	var processed *time.Time
	e = tx.QueryRowContext(ctx, `SELECT processed_at FROM owned_collection_publication_notifications WHERE id=$1 AND account_id=$2 AND app_id=$3 FOR UPDATE SKIP LOCKED`, id, account, app).Scan(&processed)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if processed != nil {
		return false, nil
	}
	eligible := func() (bool, error) {
		var allowed bool
		e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_collection_publication_notifications q WHERE q.id=$3 AND `+r.publicationEligibilitySQL()+`)`, account, app, id).Scan(&allowed)
		return allowed, e
	}
	active, e := eligible()
	if e != nil {
		return false, e
	}
	if active {
		var post string
		if e = tx.QueryRowContext(ctx, `SELECT p.post_id::text FROM owned_collection_publications p JOIN owned_collection_publication_notifications q ON q.publication_id=p.id WHERE q.id=$1`, id).Scan(&post); e != nil {
			return false, e
		}
		meta, _ := json.Marshal(map[string]string{"post_id": post, "action_uri": "/posts/" + post, "notification_type": "subscription"})
		if e = sink.ProjectLocalInTransaction(ctx, r.db, tx, notifications.Command{Key: "collection-post:" + id, AccountID: account, AppID: app, Topic: "posts.new", Title: "New post in a subscribed collection", Content: "A collection you subscribe to has a new post.", Meta: meta}); e != nil {
			return false, e
		}
		if e = plan.verify(ctx, tx, id); e != nil {
			return false, e
		}
		current, e := eligible()
		if e != nil {
			return false, e
		}
		if !current {
			return false, posts.ErrConflict
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_collection_publication_notifications SET processed_at=clock_timestamp(),suppressed_at=CASE WHEN $2::boolean THEN NULL::timestamptz ELSE clock_timestamp() END WHERE id=$1`, id, active)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}
