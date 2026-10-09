// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/ports/notifications"
)

type publisherSubscriptionNotifications struct {
	repository *notificationpg.Repository
	db         *sql.DB
	app        string
}

func (n publisherSubscriptionNotifications) EnqueueSubscriptionAdded(ctx context.Context, tx *sql.Tx, account, publisher, subscription string) error {
	meta, e := json.Marshal(map[string]string{"publisher_id": publisher, "subscription_id": subscription})
	if e != nil {
		return e
	}
	_, e = n.repository.EnqueueInTransaction(ctx, n.db, tx, notifications.Command{Key: "publisher-subscriber-added:" + subscription, AccountID: account, AppID: n.app, Topic: "subscriber_added", Title: "Publisher subscription added", Content: "A publisher manager added you as a subscriber. Notifications are off until you enable them.", Meta: meta, External: false})
	return e
}
