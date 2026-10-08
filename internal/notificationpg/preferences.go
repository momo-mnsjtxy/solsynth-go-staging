// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/notifications"
)

type PreferencePolicy interface {
	Preference(context.Context, *sql.DB, *sql.Tx, string, string, string) (string, error)
}

// Configure before starting request/worker goroutines.
func (r *Repository) WithPreferencePolicy(p PreferencePolicy) *Repository {
	r.preferences = p
	return r
}
func (r *Repository) preference(ctx context.Context, tx *sql.Tx, actor, app, topic string) (string, error) {
	if r.preferences == nil {
		return "normal", nil
	}
	v, e := r.preferences.Preference(ctx, r.db, tx, actor, app, topic)
	if e != nil {
		return "", e
	}
	if v != "normal" && v != "silent" && v != "reject" {
		return "", notifications.ErrUnavailable
	}
	return v, nil
}
