// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) publicFund(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.Method != "GET" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/wallets/funds/"), "/")
	if !strings.HasPrefix(path, "/api/wallets/funds/") || len(parts) < 1 || len(parts) > 2 || !ValidID(parts[0]) || len(parts) == 2 && parts[1] != "contributors" {
		return false
	}
	if h.Service == nil {
		fail(w, ErrUnavailable)
		return true
	}
	repo, e := h.Service.funds()
	if e != nil {
		fail(w, e)
		return true
	}
	f, e := repo.Fund(r.Context(), "", parts[0])
	if e != nil {
		fail(w, e)
		return true
	}
	if len(parts) == 1 {
		writeJSON(w, 200, f)
		return true
	}
	if !f.IsRaising {
		fail(w, ErrInvalid)
		return true
	}
	out := []FundRecipient{}
	for _, p := range f.Recipients {
		if p.IsReceived {
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
	return true
}
func (h *Handler) fundHTTP(w http.ResponseWriter, r *http.Request, p Principal, suffix string) bool {
	if suffix != "/funds" && !strings.HasPrefix(suffix, "/funds/") {
		return false
	}
	var out any
	var e error
	if suffix == "/funds" {
		switch r.Method {
		case "POST":
			if len(r.Header.Values("Idempotency-Key")) > 1 {
				e = ErrInvalid
				break
			}
			q := CreateFundRequest{IsOpen: true}
			if e = decode(w, r, &q, false); e == nil {
				out, e = h.Service.CreateFund(r.Context(), p, q, r.Header.Get("Idempotency-Key"))
			}
		case "GET":
			offset, take := 0, 20
			var status *int
			for key, target := range map[string]*int{"offset": &offset, "take": &take} {
				if raw, ok := r.URL.Query()[key]; ok {
					if len(raw) != 1 {
						e = ErrInvalid
						break
					}
					*target, e = strconv.Atoi(raw[0])
					if e != nil {
						e = ErrInvalid
						break
					}
				}
			}
			if raw, ok := r.URL.Query()["status"]; ok && e == nil {
				if len(raw) != 1 {
					e = ErrInvalid
				} else {
					var n int
					n, e = strconv.Atoi(raw[0])
					if e != nil {
						e = ErrInvalid
					} else {
						status = &n
					}
				}
			}
			if e == nil {
				var total int
				out, total, e = h.Service.Funds(r.Context(), p, offset, take, status)
				if e == nil {
					w.Header().Set("X-Total", strconv.Itoa(total))
				}
			}
		default:
			http.NotFound(w, r)
			return true
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(suffix, "/funds/"), "/")
		if r.Method != "POST" || len(parts) != 2 || !ValidID(parts[0]) {
			http.NotFound(w, r)
			return true
		}
		switch parts[1] {
		case "receive":
			out, e = h.Service.ReceiveFund(r.Context(), p, parts[0])
		case "contribute":
			var q ContributeFundRequest
			if e = decode(w, r, &q, false); e == nil {
				out, e = h.Service.ContributeFund(r.Context(), p, parts[0], q)
			}
		case "cancel":
			out, e = h.Service.CancelFund(r.Context(), p, parts[0])
		default:
			http.NotFound(w, r)
			return true
		}
	}
	if e != nil {
		fail(w, e)
	} else {
		writeJSON(w, 200, out)
	}
	return true
}
