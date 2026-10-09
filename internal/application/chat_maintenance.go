// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"time"

	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/ports/notifications"
)

func (b *Backend) InspectChatEvents(ctx context.Context) (notificationpg.ChatEventInspection, error) {
	if b == nil || b.notificationWorker == nil || b.notificationWorker.Repository == nil {
		return notificationpg.ChatEventInspection{}, notifications.ErrUnavailable
	}
	return b.notificationWorker.Repository.InspectChatEvents(ctx, b.notificationWorker.ChatNoneEncryptionMode)
}

func (b *Backend) RedriveChatEvent(ctx context.Context, kind, id string) error {
	if b == nil || b.notificationWorker == nil || b.notificationWorker.Repository == nil {
		return notifications.ErrUnavailable
	}
	return b.notificationWorker.Repository.RedriveChatEvent(ctx, kind, id)
}

func (b *Backend) PurgeRealtimeEvents(ctx context.Context, before time.Time, limit int) (int, error) {
	if b == nil || b.notificationWorker == nil || b.notificationWorker.Repository == nil {
		return 0, notifications.ErrUnavailable
	}
	return b.notificationWorker.Repository.PurgeStreamEvents(ctx, before, limit)
}
