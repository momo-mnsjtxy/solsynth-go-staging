// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
)

type notificationDeviceModule struct {
	repo    *notificationpg.DeviceStore
	handler http.Handler
}

func notificationDevicesFor(db *sql.DB, g *authoritypg.TransactionGuard, v *authority.Verifier, c *notificationpg.DeviceConfig, rewards *progressionpg.Repository, policy *notificationpg.Repository) (out notificationDeviceModule, e error) {
	if c == nil {
		out.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			notifications.WriteError(w, notifications.ErrUnavailable)
		})
		return
	}
	out.repo, e = notificationpg.NewDevices(db, g, *c, rewards)
	if e != nil {
		return
	}
	out.repo.WithDeliveryPolicy(policy)
	out.handler = (notifications.DeviceHTTP{Repository: out.repo, Authority: v, Bind: g.Bind}).Handler()
	return
}
func (m notificationDeviceModule) ready(ctx context.Context) error {
	if m.repo == nil {
		return nil
	}
	return m.repo.Ready(ctx)
}
