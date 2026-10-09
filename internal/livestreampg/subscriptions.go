// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/accountpg"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"time"
)

// SubscriptionReader uses the existing canonical stream projection without
// enabling any provider operations or issuing streaming credentials.
type SubscriptionReader struct{ repository *Repository }

func NewSubscriptionReader(p live.PublisherProvider, s live.SocialProvider, m live.MediaProvider) *SubscriptionReader {
	return &SubscriptionReader{&Repository{publishers: p, social: s, media: m}}
}
func (r *Repository) SubscriptionReader() *SubscriptionReader { return &SubscriptionReader{r} }
func (r *SubscriptionReader) where() string {
	return `s.deleted_at IS NULL AND s.visibility=0 AND u.deleted_at IS NULL AND ` + r.repository.social.PublisherVisibilitySQL("u", "$1") + ` AND ` + accountpg.ActivePublisherOwnerPredicate("u") + ` AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships b WHERE b.status=-100 AND ((b.account_id=COALESCE(u.account_id,realm.account_id) AND b.related_id=$1::uuid) OR (b.related_id=COALESCE(u.account_id,realm.account_id) AND b.account_id=$1::uuid)))`
}

const subscriptionJoin = ` FROM owned_livestreams s JOIN owned_publishers u ON u.id=s.publisher_id LEFT JOIN owned_realms realm ON realm.id=u.realm_id WHERE `

func (r *SubscriptionReader) SubscriptionContent(ctx context.Context, tx *sql.Tx, actor, pub string) (latest *time.Time, isLive bool, e error) {
	if r == nil || r.repository == nil || r.repository.publishers == nil || r.repository.social == nil || tx == nil {
		return nil, false, live.ErrUnavailable
	}
	e = tx.QueryRowContext(ctx, `SELECT max(s.started_at),COALESCE(bool_or(s.status=1 AND s.operation_state='active'),false)`+subscriptionJoin+r.where()+` AND s.publisher_id=$2`, actor, pub).Scan(&latest, &isLive)
	return
}
func (r *SubscriptionReader) SubscribedStreams(ctx context.Context, tx *sql.Tx, actor string, offset, take int) ([]live.Stream, error) {
	if r == nil || r.repository == nil || r.repository.publishers == nil || r.repository.social == nil || tx == nil {
		return nil, live.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+subscriptionJoin+r.where()+` AND s.status=1 AND s.operation_state='active' AND EXISTS(SELECT 1 FROM owned_publisher_follows f WHERE f.publisher_id=s.publisher_id AND f.account_id=$1::uuid AND f.state='accepted' AND f.ended_at IS NULL) ORDER BY s.started_at DESC NULLS LAST,s.id OFFSET $2 LIMIT $3`, actor, offset, take)
	if e != nil {
		return nil, e
	}
	items := []live.Stream{}
	for rows.Next() {
		v, x := scan(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []live.Stream{}
	for i := range items {
		_, x := r.repository.visible(ctx, tx, actor, &items[i])
		if errors.Is(x, live.ErrNotFound) || errors.Is(x, live.ErrForbidden) {
			continue
		}
		if x != nil {
			return nil, x
		}
		out = append(out, items[i])
	}
	return out, nil
}
