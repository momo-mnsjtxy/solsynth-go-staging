// SPDX-License-Identifier: AGPL-3.0-only
package media

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Service) billingHTTP(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	pool := ""
	cleanup := false
	recognized := strings.HasPrefix(p, "/api/billing/") || p == "/api/files/recycle" || strings.HasPrefix(p, "/api/pools/") && strings.HasSuffix(p, "/recycle") || p == "/api/media/admin/quota-records"
	if !recognized {
		return false
	}
	if q, e := url.ParseQuery(r.URL.RawQuery); e != nil || len(q) != 0 && (p != "/api/billing/quota/records" || r.Method != "GET") {
		failure(w, ErrInvalid)
		return true
	}
	repo, ok := s.Repository.(BillingRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return true
	}
	permission := ReadPermission
	if r.Method == "DELETE" {
		permission = DeletePermission
		cleanup = true
	}
	if p == "/api/media/admin/quota-records" && r.Method == "POST" {
		permission = "media.quotas.manage"
	}
	principal, e := s.principal(r, permission)
	if e != nil {
		failure(w, e)
		return true
	}
	actor := principal.Account.ID
	var out any
	switch {
	case p == "/api/media/admin/quota-records" && r.Method == "POST":
		var v QuotaRecord
		e = decodeManagement(w, r, &v)
		if e == nil {
			out, e = repo.AddQuotaRecord(r.Context(), actor, v)
		}
	case cleanup && p == "/api/files/recycle":
		var n int
		n, e = repo.CleanupRecycled(r.Context(), actor, "")
		out = map[string]int{"count": n}
	case cleanup && strings.HasPrefix(p, "/api/pools/"):
		pool = strings.TrimSuffix(strings.TrimPrefix(p, "/api/pools/"), "/recycle")
		var n int
		n, e = repo.CleanupRecycled(r.Context(), actor, pool)
		out = map[string]int{"count": n}
	case p == "/api/billing/quota" && r.Method == "GET":
		out, e = s.Repository.Quota(r.Context(), actor)
	case p == "/api/billing/quota/records" && r.Method == "GET":
		take, offset, expired := 20, 0, false
		for key, v := range r.URL.Query() {
			if len(v) != 1 {
				e = ErrInvalid
				break
			}
			switch key {
			case "take", "offset":
				n, err := strconv.Atoi(v[0])
				if err != nil || strconv.Itoa(n) != v[0] {
					e = ErrInvalid
				}
				if key == "take" {
					take = n
				} else {
					offset = n
				}
			case "expired":
				if v[0] != "true" && v[0] != "false" {
					e = ErrInvalid
				}
				expired = v[0] == "true"
			default:
				e = ErrInvalid
			}
		}
		if e == nil {
			var total int64
			out, total, e = repo.QuotaRecords(r.Context(), actor, expired, take, offset)
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		}
	case (p == "/api/billing/usage" || strings.HasPrefix(p, "/api/billing/usage/")) && r.Method == "GET":
		pool = strings.TrimPrefix(p, "/api/billing/usage")
		pool = strings.TrimPrefix(pool, "/")
		out, e = repo.StorageUsage(r.Context(), actor, pool)
	default:
		e = ErrNotFound
	}
	if e != nil {
		failure(w, e)
	} else {
		reply(w, 200, out)
	}
	return true
}
