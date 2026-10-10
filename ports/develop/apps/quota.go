// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"net/http"
)

// The retained bot controller evidences Total and Used. No retained quota
// service establishes bonus records, formulas or account-wide aggregation.
type DeveloperQuota struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}
type QuotaStore interface {
	DeveloperQuota(context.Context, string) (DeveloperQuota, error)
}

func (s *Service) serveQuota(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/develop/quota" {
		return false
	}
	if r.Method != http.MethodGet {
		method(w, "GET")
		return true
	}
	actor, e := s.actor(r)
	if e != nil {
		failure(w, e)
		return true
	}
	if r.URL.RawQuery != "" {
		failure(w, ErrInvalid)
		return true
	}
	if s.Quota == nil {
		failure(w, ErrUnavailable)
		return true
	}
	out, e := s.Quota.DeveloperQuota(r.Context(), actor)
	if e != nil {
		failure(w, e)
	} else {
		respond(w, http.StatusOK, out)
	}
	return true
}
