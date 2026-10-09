// SPDX-License-Identifier: AGPL-3.0-only
package realtime

import (
	"context"
	"net/http"
	"time"

	"solsynth.local/go-migration/ports/messager/rooms"
	"solsynth.local/go-migration/ports/notifications"
)

// bindDevice accepts the same explicit owned selector as MLS HTTP operations.
// It grants nothing: the connection lease and every encrypted event recheck it.
func bindDevice(r *http.Request) (*http.Request, error) {
	device := r.Header.Get("X-Device-Id")
	if len(r.Header.Values("X-Device-Id")) > 1 || device != "" && !rooms.ValidMLSIdentifier(device) {
		return r, notifications.ErrInvalid
	}
	return r.WithContext(rooms.WithMLSWrite(r.Context(), rooms.MLSWriteContext{Device: device})), nil
}

func (h *Handler) openConnection(ctx context.Context, actor string) (string, error) {
	if h.cfg.Connections == nil {
		return "", nil
	}
	return h.cfg.Connections.OpenConnection(ctx, actor)
}
func (h *Handler) currentConnection(ctx context.Context, actor, connection string) error {
	if h.cfg.Connections == nil {
		return nil
	}
	if e := h.cfg.Connections.RenewConnection(ctx, actor, connection); e != nil {
		return e
	}
	device, e := h.cfg.Connections.CurrentConnectionDevice(ctx, actor, connection)
	if e != nil {
		return e
	}
	if device != rooms.MLSWriteFrom(ctx).Device {
		return notifications.ErrInvalid
	}
	return nil
}
func (h *Handler) closeConnection(ctx context.Context, actor, connection string) {
	if h.cfg.Connections == nil {
		return
	}
	// Cancellation does not leave a live process lease behind. Revoked/expired
	// authority may deny this best-effort close; the bounded lease then expires.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = h.cfg.Connections.CloseConnection(closeCtx, actor, connection)
}
