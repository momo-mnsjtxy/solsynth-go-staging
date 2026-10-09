// SPDX-License-Identifier: AGPL-3.0-only
package activities

import (
	"context"
	"net/http"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"strings"
)

type SensorRequest struct {
	Sensor       string  `json:"sensor"`
	Content      Content `json:"content"`
	Visibility   *int    `json:"visibility"`
	LeaseMinutes int     `json:"lease_minutes"`
}
type ProviderRequest struct {
	Provider       string `json:"-"`
	ConsentMinutes int    `json:"consent_minutes,omitempty"`
	ConnectionID   string `json:"connection_id"`
	Visibility     *int   `json:"visibility"`
	LeaseMinutes   int    `json:"lease_minutes"`
}
type SensorRepository interface {
	ObserveSensor(context.Context, string, SensorRequest) (Activity, error)
	ObserveProvider(context.Context, string, ProviderRequest) (Activity, error)
}

func SensorHandles(p string) bool {
	return p == "/api/accounts/me/presence/sensors" || p == "/api/accounts/me/presence/providers/steam" || p == "/api/accounts/me/presence/providers/lastfm"
}
func (s *Service) SensorHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.Repository == nil {
			failure(w, ErrUnavailable)
			return
		}
		if !SensorHandles(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if r.Method == "DELETE" && r.URL.Path != "/api/accounts/me/presence/sensors" {
			s.deleteProvider(w, r)
			return
		}
		if r.Method != "POST" {
			c.Respond(w, 405, ErrInvalid)
			return
		}
		if r.URL.RawQuery != "" {
			c.RespondError(w, authority.ErrInvalid)
			return
		}
		repo, ok := s.Repository.(SensorRepository)
		if !ok || s.Authority == nil || s.Bind == nil {
			c.RespondError(w, authority.ErrUnavailable)
			return
		}
		proof, e := s.Authority.AuthenticateRequest(r)
		if e != nil {
			c.RespondError(w, e)
			return
		}
		ctx := s.Bind(r.Context(), proof)
		var out Activity
		if r.URL.Path == "/api/accounts/me/presence/sensors" {
			var v SensorRequest
			if x := c.DecodeStrict(w, r, &v); x != nil {
				e = ErrInvalid
			}
			if e == nil {
				out, e = repo.ObserveSensor(ctx, proof.Account.ID, v)
			}
		} else {
			var v ProviderRequest
			v.Provider = strings.TrimPrefix(r.URL.Path, "/api/accounts/me/presence/providers/")
			if x := c.DecodeStrict(w, r, &v); x != nil {
				e = ErrInvalid
			}
			if e == nil {
				out, e = repo.ObserveProvider(ctx, proof.Account.ID, v)
			}
		}
		if e != nil {
			failure(w, e)
			return
		}
		c.Respond(w, 200, out)
	})
}

type ProviderConsentRepository interface {
	RevokeProviderConsent(context.Context, string, string) error
}

func (s *Service) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || s.Authority == nil || s.Bind == nil {
		failure(w, ErrInvalid)
		return
	}
	p, e := s.Authority.AuthenticateRequest(r)
	if e != nil {
		failure(w, e)
		return
	}
	var body struct {
		ConnectionID string `json:"connection_id"`
	}
	if c.DecodeStrict(w, r, &body) != nil {
		failure(w, ErrInvalid)
		return
	}
	repo, ok := s.Repository.(ProviderConsentRepository)
	if !ok {
		failure(w, ErrUnavailable)
		return
	}
	if e = repo.RevokeProviderConsent(s.Bind(r.Context(), p), p.Account.ID, body.ConnectionID); e != nil {
		failure(w, e)
		return
	}
	send(w, 204, nil)
}
