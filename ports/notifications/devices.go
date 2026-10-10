// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"context"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
)

const DevicePermission = "notifications.devices.manage"

type Device struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Provider   string     `json:"provider"`
	TokenHash  string     `json:"token_hash"`
	CreatedAt  time.Time  `json:"created_at"`
	VerifiedAt *time.Time `json:"verified_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}
type DeviceRequest struct {
	DeviceID     string `json:"device_id"`
	Label        string `json:"label"`
	Token        string `json:"token"`
	ConsentHours int    `json:"consent_hours"`
}
type DeviceChallenge struct {
	ID        string    `json:"challenge_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type DeviceProof struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}
type DeviceRepository interface {
	StartDevice(context.Context, string, DeviceRequest) (DeviceChallenge, error)
	ConfirmDevice(context.Context, string, DeviceProof) (Device, error)
	Devices(context.Context, string) ([]Device, error)
	RevokeDevice(context.Context, string, string) error
}
type DeviceAuthority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
type DeviceHTTP struct {
	Repository DeviceRepository
	Authority  DeviceAuthority
	Bind       func(context.Context, authority.Principal) context.Context
}

func DeviceHandles(path string) bool {
	return path == "/api/notifications/devices" || path == "/api/notifications/devices/confirm" || strings.HasPrefix(path, "/api/notifications/devices/")
}
func (h DeviceHTTP) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !DeviceHandles(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if h.Repository == nil || h.Authority == nil || h.Bind == nil {
			WriteError(w, ErrUnavailable)
			return
		}
		if r.URL.RawQuery != "" || r.URL.User != nil {
			WriteError(w, ErrInvalid)
			return
		}
		p, e := h.Authority.AuthenticateRequest(r)
		if e != nil {
			WriteError(w, e)
			return
		}
		ctx := h.Bind(r.Context(), p)
		var out any
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/notifications/devices":
			out, e = h.Repository.Devices(ctx, p.Account.ID)
		case r.Method == "POST" && r.URL.Path == "/api/notifications/devices":
			var v DeviceRequest
			if c.DecodeStrict(w, r, &v) != nil {
				e = ErrInvalid
			} else {
				out, e = h.Repository.StartDevice(ctx, p.Account.ID, v)
			}
		case r.Method == "POST" && r.URL.Path == "/api/notifications/devices/confirm":
			var v DeviceProof
			if c.DecodeStrict(w, r, &v) != nil {
				e = ErrInvalid
			} else {
				out, e = h.Repository.ConfirmDevice(ctx, p.Account.ID, v)
			}
		case r.Method == "DELETE" && authority.ValidID(strings.TrimPrefix(r.URL.Path, "/api/notifications/devices/")):
			e = h.Repository.RevokeDevice(ctx, p.Account.ID, strings.TrimPrefix(r.URL.Path, "/api/notifications/devices/"))
			if e == nil {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(204)
				return
			}
		default:
			w.WriteHeader(405)
			return
		}
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, out)
	})
}

// PushProvider sends to a fixed configured transport. A challenge is considered
// proven only when the receiving device returns its unrevealed random code.
type PushProvider interface {
	SendDevice(context.Context, string, map[string]string) error
}
