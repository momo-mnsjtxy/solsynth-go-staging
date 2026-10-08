// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

type NotificationSink interface {
	Enqueue(context.Context, notifications.Command) (string, error)
}

// ProcessNotifications projects durable moderation events only into the
// canonical local inbox. No domain locks cross the sink call; replay after a
// crash uses the exact same key and body, so concurrent workers cannot duplicate.
func (r *Repository) ProcessNotifications(ctx context.Context, sink NotificationSink, app string, limit int) (int, error) {
	if r == nil || sink == nil || !notifications.ValidApp(app) {
		return 0, c.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, c.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id,realm_id::text,post_id::text,account_id::text,COALESCE(reason,'') FROM owned_content_moderation_notifications WHERE projected_at IS NULL ORDER BY id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	type event struct {
		id                           int64
		realm, post, account, reason string
	}
	events := []event{}
	for rows.Next() {
		var v event
		if e = rows.Scan(&v.id, &v.realm, &v.post, &v.account, &v.reason); e != nil {
			rows.Close()
			return 0, e
		}
		events = append(events, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	done := 0
	for _, v := range events {
		var persistedApp string
		if e = r.db.QueryRowContext(ctx, `UPDATE owned_content_moderation_notifications SET app_id=COALESCE(app_id,$2::text) WHERE id=$1 RETURNING app_id`, v.id, app).Scan(&persistedApp); e != nil {
			return done, e
		}
		if persistedApp != app {
			return done, c.ErrConflict
		}
		meta, _ := json.Marshal(map[string]string{"post_id": v.post, "realm_id": v.realm})
		_, e = sink.Enqueue(ctx, notifications.Command{Key: fmt.Sprintf("content-moderation:%d", v.id), AccountID: v.account, AppID: app, Topic: "post.moderation", Title: "Post removed from realm", Content: v.reason, Meta: meta, External: false})
		if e != nil && !errors.Is(e, authority.ErrDisabled) {
			return done, e
		}
		if _, e = r.db.ExecContext(ctx, `UPDATE owned_content_moderation_notifications SET projected_at=$2 WHERE id=$1 AND projected_at IS NULL`, v.id, r.config.Now().UTC()); e != nil {
			return done, e
		}
		done++
	}
	return done, nil
}
