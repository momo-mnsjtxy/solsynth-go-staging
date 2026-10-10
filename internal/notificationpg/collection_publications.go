// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"strings"
)

// CollectionPublicationPolicy supplies a fixed, current-source predicate for
// the reserved collection-post source key. The canonical interaction adapter
// checks its durable subscription incarnation, source and all privacy policies.
// SQL must use inbox and text actor/app parameters $1/$2; request text is forbidden.
// Callers cast actor to text before uuid so nested NULLIF(actor, ”) stays text.
type CollectionPublicationPolicy interface{ CollectionPublicationVisibleSQL() string }

// PublisherContentPolicy owns the reserved publisher-content source key from
// post/stream projection through inbox reads and provider dispatch.
type PublisherContentPolicy interface {
	PublisherContentVisibleSQL() string
	PublisherContentDeliveryAllowed(context.Context, *sql.Tx, string, string, string) (bool, error)
}

func (r *Repository) WithCollectionPublications(p CollectionPublicationPolicy) *Repository {
	r.collections = p
	return r
}
func (r *Repository) WithPublisherContent(p PublisherContentPolicy) *Repository {
	r.publisherContent = p
	return r
}
func isPublisherContentKey(key string) bool { return strings.HasPrefix(key, "publisher-content:") }
func (r *Repository) inboxVisibilitySQL() string {
	ordinary := `left(inbox.source_key,16)<>'collection-post:' AND left(inbox.source_key,18)<>'publisher-content:'`
	parts := []string{`(` + ordinary + `)`}
	if r.collections != nil {
		parts = append(parts, `(`+r.collections.CollectionPublicationVisibleSQL()+`)`)
	}
	if r.publisherContent != nil {
		parts = append(parts, `(`+r.publisherContent.PublisherContentVisibleSQL()+`)`)
	}
	return `(` + strings.Join(parts, ` OR `) + `)`
}

func (r *Repository) publisherContentAllowed(ctx context.Context, tx *sql.Tx, key, account, app, id string) (bool, error) {
	if !isPublisherContentKey(key) {
		return true, nil
	}
	if r.publisherContent == nil {
		return false, nil
	}
	return r.publisherContent.PublisherContentDeliveryAllowed(ctx, tx, account, app, id)
}

func (r *Repository) visibleInboxIDs(ctx context.Context, tx *sql.Tx, actor, app string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM notification_v1.inbox WHERE account_id=$1::text::uuid AND app_id=$2 AND id=ANY($3::uuid[]) AND dismissed_at IS NULL AND `+r.inboxVisibilitySQL(), actor, app, ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out[id] = true
	}
	return out, rows.Err()
}
