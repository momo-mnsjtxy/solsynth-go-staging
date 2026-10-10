// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
)

const PresenceScanPermission = "accounts.statuses.update"

type ProviderScan struct{ AccountRef, ProviderIdentifier, Stage string }
type ProviderOperations interface {
	ScheduleSteamPresence(context.Context, string, ProviderScan) (int, error)
	SendNightOwlReminder(context.Context, string, string, int) (string, error)
}

func providerPath(p []string) bool {
	if len(p) == 6 && p[4] == "notifications" && p[5] == "night-owl" {
		return true
	}
	if len(p) >= 6 && p[3] == "presences" && p[4] == "steam" {
		return p[5] == "scan" && (len(p) == 6 || len(p) == 7 || len(p) == 8 && p[6] == "stages") || len(p) == 7 && p[5] == "scan-by-steam-id"
	}
	return false
}
func serveProviderOperation(w http.ResponseWriter, r *http.Request, repo Repository, actor string, p []string) {
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		c.Respond(w, 405, c.ErrInvalid)
		return
	}
	if r.URL.RawQuery != "" {
		c.RespondError(w, c.ErrInvalid)
		return
	}
	ext, ok := repo.(ProviderOperations)
	if !ok {
		c.RespondError(w, c.ErrUnavailable)
		return
	}
	if p[4] == "notifications" {
		hour := 0
		if r.Body != nil && r.ContentLength != 0 {
			var q *struct {
				Hour int `json:"hour"`
			}
			if e := c.DecodeStrict(w, r, &q); e != nil {
				c.RespondError(w, e)
				return
			}
			if q != nil {
				hour = q.Hour
			}
		}
		if hour < 0 || hour > 2 {
			c.RespondError(w, c.ErrInvalid)
			return
		}
		id, e := ext.SendNightOwlReminder(r.Context(), actor, p[3], hour)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		c.Respond(w, 202, map[string]any{"queued": true, "notification_id": id})
		return
	}
	if e := c.EmptyBody(w, r); e != nil {
		c.RespondError(w, e)
		return
	}
	q := ProviderScan{}
	if p[5] == "scan-by-steam-id" {
		q.ProviderIdentifier = p[6]
	} else if len(p) == 7 {
		q.AccountRef = p[6]
	} else if len(p) == 8 {
		q.Stage = p[7]
	}
	n, e := ext.ScheduleSteamPresence(r.Context(), actor, q)
	if e != nil {
		c.RespondError(w, e)
		return
	}
	c.Respond(w, 202, map[string]any{"queued": n})
}
