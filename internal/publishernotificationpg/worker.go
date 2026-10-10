// SPDX-License-Identifier: AGPL-3.0-only
package publishernotificationpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/notifications"
)

type intent struct {
	id, kind, source, publisher string
	generation                  int64
	occurred                    time.Time
}
type candidate struct {
	subscription, account string
	incarnation           int64
}
type observed struct {
	publisherOwner, publisherRealm string
	sourceRealm, sourceRealmOwner  string
}
type contentDetail struct {
	visibility int
	publisher  string
	content    string
}

func clipUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "..."
	end := maxBytes - len(suffix)
	if end < 0 {
		return ""
	}
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil || r.visibility == nil || r.sink == nil || !notifications.ValidApp(r.app) {
		return 0, notifications.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, notifications.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for n := 0; n < limit; n++ {
		ok, e := r.processOne(ctx)
		if e != nil {
			return n, e
		}
		if !ok {
			return n, nil
		}
	}
	return limit, nil
}

func (r *Repository) processOne(ctx context.Context) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var in intent
	e = tx.QueryRowContext(ctx, `SELECT id::text,kind,source_id::text,source_generation,publisher_id::text,occurred_at
	 FROM owned_publisher_content_notification_intents WHERE completed_at IS NULL ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&in.id, &in.kind, &in.source, &in.generation, &in.publisher, &in.occurred)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var c candidate
	e = tx.QueryRowContext(ctx, `SELECT f.id::text,f.account_id::text,f.incarnation FROM owned_publisher_follows f
	 WHERE f.publisher_id=$1 AND f.state='accepted' AND f.ended_at IS NULL AND f.incarnation_started_at<=$2
	 AND NOT EXISTS(SELECT 1 FROM owned_publisher_content_notification_receipts receipt WHERE receipt.intent_id=$3 AND receipt.subscription_id=f.id AND receipt.subscription_incarnation=f.incarnation)
	 ORDER BY f.account_id,f.id LIMIT 1`, in.publisher, in.occurred, in.id).Scan(&c.subscription, &c.account, &c.incarnation)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	if errors.Is(e, sql.ErrNoRows) {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_content_notification_intents SET completed_at=clock_timestamp() WHERE id=$1`, in.id); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	if e = r.lockCurrent(ctx, tx, in, c); errors.Is(e, sql.ErrNoRows) {
		return false, notifications.ErrConflict
	} else if e != nil {
		return false, e
	}
	allowed, detail, e := r.eligible(ctx, tx, in, c)
	if e != nil {
		return false, e
	}
	var outbox *string
	if allowed {
		meta := map[string]any{"publisher_id": in.publisher, "subscription_id": c.subscription, "subscription_incarnation": c.incarnation, "source_id": in.source, "source_kind": in.kind}
		topic, heading, notificationType := "posts.new", clipUTF8("New post from "+detail.publisher, 1024), "subscription"
		if in.kind == "livestream.started" {
			topic, heading = "livestream.started", clipUTF8(detail.publisher+" is live", 1024)
			meta["livestream_id"], meta["action_uri"] = in.source, "/livestreams/"+in.source
		} else {
			meta["post_id"], meta["action_uri"] = in.source, "/posts/"+in.source
			if detail.visibility == 4 {
				topic, heading, notificationType = "posts.close_friends", clipUTF8("Close friends post from "+detail.publisher, 1024), "close_friends"
			}
		}
		meta["notification_type"] = notificationType
		raw, x := json.Marshal(meta)
		if x != nil {
			return false, x
		}
		key := "publisher-content:" + in.id + ":" + c.subscription + ":" + strconv.FormatInt(c.incarnation, 10)
		id, x := r.sink.EnqueueInTransaction(ctx, r.db, tx, notifications.Command{Key: key, AccountID: c.account, AppID: r.app, Topic: topic, Title: heading, Content: clipUTF8(detail.content, 512), Meta: raw, External: true})
		if x != nil {
			return false, x
		}
		outbox = &id
		current, _, x := r.eligible(ctx, tx, in, c)
		if x != nil {
			return false, x
		}
		if !current {
			return false, notifications.ErrConflict
		}
	}
	outcome := "suppressed"
	if allowed {
		outcome = "enqueued"
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_content_notification_receipts(intent_id,subscription_id,subscription_incarnation,account_id,outcome,outbox_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, in.id, c.subscription, c.incarnation, c.account, outcome, outbox)
	if e != nil {
		return false, e
	}
	return true, tx.Commit()
}

func uniqueSorted(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Repository) observe(ctx context.Context, tx *sql.Tx, in intent) (observed, error) {
	var v observed
	if in.kind == "post.published" {
		e := tx.QueryRowContext(ctx, `SELECT COALESCE(u.account_id::text,publisher_realm.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(source.realm_id::text,''),COALESCE(source_realm.account_id::text,'')
		 FROM owned_text_posts source JOIN owned_publishers u ON u.id=source.publisher_id
		 LEFT JOIN owned_realms publisher_realm ON publisher_realm.id=u.realm_id
		 LEFT JOIN owned_realms source_realm ON source_realm.id=source.realm_id
		 WHERE source.id=$1 AND source.publisher_id=$2`, in.source, in.publisher).Scan(&v.publisherOwner, &v.publisherRealm, &v.sourceRealm, &v.sourceRealmOwner)
		return v, e
	}
	e := tx.QueryRowContext(ctx, `SELECT COALESCE(u.account_id::text,publisher_realm.account_id::text,''),COALESCE(u.realm_id::text,'')
	 FROM owned_livestreams source JOIN owned_publishers u ON u.id=source.publisher_id LEFT JOIN owned_realms publisher_realm ON publisher_realm.id=u.realm_id
	 WHERE source.id=$1 AND source.publisher_id=$2 AND source.generation=$3`, in.source, in.publisher, in.generation).Scan(&v.publisherOwner, &v.publisherRealm)
	return v, e
}

// lockCurrent follows the shared account -> realm -> publisher -> source ->
// subscription order. It observes ancestry without row locks, locks every owner
// account and realm in UUID order, then verifies the original mapping.
func (r *Repository) lockCurrent(ctx context.Context, tx *sql.Tx, in intent, c candidate) error {
	obs, e := r.observe(ctx, tx, in)
	if e != nil {
		return e
	}
	if obs.publisherOwner == "" {
		return sql.ErrNoRows
	}
	accounts := uniqueSorted(c.account, obs.publisherOwner, obs.sourceRealmOwner)
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, accounts)
	if e != nil {
		return e
	}
	locked := 0
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		locked++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if locked != len(accounts) {
		return notifications.ErrConflict
	}
	for _, realm := range uniqueSorted(obs.publisherRealm, obs.sourceRealm) {
		var id, owner string
		if e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text FROM owned_realms WHERE id=$1 FOR SHARE`, realm).Scan(&id, &owner); e != nil {
			return e
		}
		if realm == obs.publisherRealm && owner != obs.publisherOwner || realm == obs.sourceRealm && owner != obs.sourceRealmOwner {
			return notifications.ErrConflict
		}
	}
	var publisherOwner, publisherRealm string
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(u.account_id::text,r.account_id::text,''),COALESCE(u.realm_id::text,'') FROM owned_publishers u LEFT JOIN owned_realms r ON r.id=u.realm_id WHERE u.id=$1 FOR SHARE OF u`, in.publisher).Scan(&publisherOwner, &publisherRealm)
	if e != nil {
		return e
	}
	if publisherOwner != obs.publisherOwner || publisherRealm != obs.publisherRealm {
		return notifications.ErrConflict
	}
	var lockedSource, sourceRealm string
	if in.kind == "post.published" {
		e = tx.QueryRowContext(ctx, `SELECT id::text,COALESCE(realm_id::text,'') FROM owned_text_posts WHERE id=$1 AND publisher_id=$2 FOR SHARE`, in.source, in.publisher).Scan(&lockedSource, &sourceRealm)
		if e == nil && sourceRealm != obs.sourceRealm {
			return notifications.ErrConflict
		}
	} else {
		e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_livestreams WHERE id=$1 AND publisher_id=$2 AND generation=$3 FOR SHARE`, in.source, in.publisher, in.generation).Scan(&lockedSource)
	}
	if e != nil {
		return e
	}
	if lockedSource != in.source {
		return notifications.ErrConflict
	}
	var lockedSubscription string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publisher_follows WHERE id=$1 AND publisher_id=$2 FOR SHARE`, c.subscription, in.publisher).Scan(&lockedSubscription); e != nil {
		return e
	}
	if lockedSubscription != c.subscription {
		return notifications.ErrConflict
	}
	return nil
}

func (r *Repository) sourceEligibilitySQL(account, intentID, subscription, incarnation string) string {
	publisher := postpg.VisiblePublisherPredicate(r.visibility, "u", account, true)
	common := `f.id=` + subscription + ` AND f.account_id=` + account + ` AND f.publisher_id=i.publisher_id AND f.incarnation=` + incarnation + ` AND f.incarnation_started_at<=i.occurred_at AND f.state='accepted' AND f.ended_at IS NULL AND f.notify
	 AND recipient.id=f.account_id AND NOT recipient.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments pun WHERE pun.account_id=recipient.id AND pun.kind='disable_account' AND(pun.starts_at IS NULL OR pun.starts_at<=clock_timestamp())AND(pun.expires_at IS NULL OR pun.expires_at>clock_timestamp()))
	 AND ` + publisher + `
	 AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships mute WHERE mute.status=-50 AND ((mute.account_id=COALESCE(u.account_id,realm.account_id) AND mute.related_id=f.account_id) OR(mute.related_id=COALESCE(u.account_id,realm.account_id) AND mute.account_id=f.account_id)))`
	post := `EXISTS(SELECT 1 FROM owned_publisher_content_notification_intents i JOIN owned_text_posts p ON p.id=i.post_id JOIN owned_publishers u ON u.id=i.publisher_id LEFT JOIN owned_realms realm ON realm.id=u.realm_id JOIN owned_publisher_follows f ON f.publisher_id=i.publisher_id JOIN owned_authority_accounts recipient ON recipient.id=f.account_id WHERE i.id=` + intentID + ` AND i.kind='post.published' AND i.source_generation=0 AND p.id=i.source_id AND p.publisher_id=i.publisher_id AND p.published_at=i.occurred_at AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=clock_timestamp() AND p.shadowbanned_at IS NULL AND p.visibility IN(0,4,5) AND NOT EXISTS(SELECT 1 FROM owned_post_relations reply WHERE reply.post_id=p.id AND reply.replied_post_id IS NOT NULL) AND ` + postpg.RealmPredicate(r.visibility, false) + ` AND (p.visibility<>4 OR (` + r.visibility.PostVisibilitySQL() + `)) AND ` + common + `)`
	stream := `EXISTS(SELECT 1 FROM owned_publisher_content_notification_intents i JOIN owned_livestreams s ON s.id=i.livestream_id AND s.generation=i.source_generation JOIN owned_publishers u ON u.id=i.publisher_id LEFT JOIN owned_realms realm ON realm.id=u.realm_id JOIN owned_publisher_follows f ON f.publisher_id=i.publisher_id JOIN owned_authority_accounts recipient ON recipient.id=f.account_id WHERE i.id=` + intentID + ` AND i.kind='livestream.started' AND s.id=i.source_id AND s.publisher_id=i.publisher_id AND s.started_at=i.occurred_at AND s.deleted_at IS NULL AND s.visibility=0 AND s.status=1 AND s.operation_state='active' AND ` + common + `)`
	return `(` + post + ` OR ` + stream + `)`
}

func (r *Repository) eligible(ctx context.Context, tx *sql.Tx, in intent, c candidate) (bool, contentDetail, error) {
	var ok bool
	var detail contentDetail
	expr := r.sourceEligibilitySQL("$1", "$2", "$3", "$4")
	q := `SELECT ` + expr + `,
	 COALESCE((SELECT p.visibility FROM owned_text_posts p WHERE $6='post.published' AND p.id=$5),0),
	 COALESCE((SELECT u.nick FROM owned_publishers u WHERE u.id=$7),''),
	 COALESCE((SELECT COALESCE(NULLIF(left(p.description,40),''),NULLIF(left(p.content,100),''),NULLIF(left(p.title,100),''),'New post') FROM owned_text_posts p WHERE $6='post.published' AND p.id=$5),(SELECT COALESCE(NULLIF(left(s.title,100),''),'Live Stream') FROM owned_livestreams s WHERE $6='livestream.started' AND s.id=$5 AND s.generation=$8),'')`
	e := tx.QueryRowContext(ctx, q, c.account, in.id, c.subscription, c.incarnation, in.source, in.kind, in.publisher, in.generation).Scan(&ok, &detail.visibility, &detail.publisher, &detail.content)
	return ok, detail, e
}

// PublisherContentVisibleSQL is injected only as fixed SQL into notificationpg.
// The inbox alias and actor/app $1/$2 parameters are owned by that package.
func (r *Repository) PublisherContentVisibleSQL() string {
	if r == nil || r.visibility == nil {
		return `FALSE`
	}
	return `EXISTS(SELECT 1 FROM owned_publisher_content_notification_receipts receipt JOIN owned_publisher_content_notification_intents source ON source.id=receipt.intent_id WHERE receipt.outbox_id=inbox.id AND receipt.account_id=NULLIF($1::text,'')::uuid AND receipt.outcome='enqueued' AND ` + r.sourceEligibilitySQL("NULLIF($1::text,'')::uuid", "source.id", "receipt.subscription_id", "receipt.subscription_incarnation") + `)`
}

// PublisherContentDeliveryAllowed owns the current-policy lock plan for both
// outbox projection and provider dispatch. The notification id is stable across
// outbox, inbox and delivery rows.
func (r *Repository) PublisherContentDeliveryAllowed(ctx context.Context, tx *sql.Tx, account, app, notificationID string) (bool, error) {
	if r == nil || tx == nil || app != r.app {
		return false, notifications.ErrUnavailable
	}
	var in intent
	var c candidate
	e := tx.QueryRowContext(ctx, `SELECT i.id::text,i.kind,i.source_id::text,i.source_generation,i.publisher_id::text,i.occurred_at,receipt.subscription_id::text,receipt.account_id::text,receipt.subscription_incarnation
	 FROM owned_publisher_content_notification_receipts receipt JOIN owned_publisher_content_notification_intents i ON i.id=receipt.intent_id
	 WHERE receipt.outbox_id=$1 AND receipt.account_id=$2 AND receipt.outcome='enqueued'`, notificationID, account).Scan(&in.id, &in.kind, &in.source, &in.generation, &in.publisher, &in.occurred, &c.subscription, &c.account, &c.incarnation)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = r.lockCurrent(ctx, tx, in, c); errors.Is(e, sql.ErrNoRows) {
		return false, nil
	} else if e != nil {
		return false, e
	}
	allowed, _, e := r.eligible(ctx, tx, in, c)
	return allowed, e
}

func publisherContentKey(key string) bool { return strings.HasPrefix(key, "publisher-content:") }
