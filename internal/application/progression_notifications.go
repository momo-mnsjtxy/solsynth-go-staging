// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/ports/notifications"
)

// progressionNotifications hands committed achievements to the same durable
// account inbox queue. The stable source key makes crash retries idempotent.
// It never invokes an external sender or includes another account's rewards.
type progressionNotifications struct {
	repository *notificationpg.Repository
	app        string
}

func (s progressionNotifications) DeliverProgression(ctx context.Context, actor, eventID string, payload json.RawMessage) error {
	if s.repository == nil {
		return errors.New("progression inbox unavailable")
	}
	_, e := s.repository.Enqueue(ctx, notifications.Command{Key: "progression:" + eventID, AccountID: actor, AppID: s.app, Topic: "progression", Title: "Progression updated", Content: "Your achievement or quest progress earned a reward.", Meta: payload, External: false})
	return e
}
