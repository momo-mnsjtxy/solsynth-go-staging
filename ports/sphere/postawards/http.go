// SPDX-License-Identifier: AGPL-3.0-only
package postawards

import (
	"errors"
	"net/http"
	"net/url"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/sponsorship"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strconv"
	"strings"
)

type Auth func(*http.Request, bool) (string, error)

func Handles(path string) bool {
	p := parts(path)
	return len(p) >= 3 && len(p) <= 4 && p[0] == "posts" && p[2] == "awards" && (len(p) == 3 || p[3] == "pending") || len(p) >= 3 && len(p) <= 4 && p[0] == "merchants" && (p[2] == "wallet" && len(p) == 3 || p[2] == "settlements" && (len(p) == 3 || p[3] == "settle"))
}
func writeError(w http.ResponseWriter, e error) {
	var f *Failure
	if errors.As(e, &f) {
		admin.Respond(w, f.Status, f)
		return
	}
	if errors.Is(e, c.ErrIdempotency) {
		admin.Respond(w, 409, ErrConflict)
		return
	}
	sponsorship.WriteError(w, e)
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			writeError(w, ErrUnavailable)
			return
		}
		p := parts(r.URL.Path)
		required := r.Method != "GET" || p[0] == "merchants"
		actor, e := auth(r, required)
		if e != nil {
			writeError(w, e)
			return
		}
		if required && actor == "" {
			writeError(w, authority.ErrInvalid)
			return
		}
		query, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil {
			writeError(w, ErrInvalid)
			return
		}
		q := Page{}
		for k, v := range query {
			if len(v) != 1 || (k != "offset" && k != "take") {
				writeError(w, ErrInvalid)
				return
			}
			n, x := strconv.Atoi(v[0])
			if x != nil {
				writeError(w, ErrInvalid)
				return
			}
			if k == "offset" {
				q.Offset = n
			} else {
				q.Take = n
			}
		}
		q, e = q.Checked()
		if e != nil {
			writeError(w, e)
			return
		}
		var out any
		total := -1
		switch {
		case p[0] == "posts" && len(p) == 3 && r.Method == "POST":
			var q Request
			if admin.DecodeStrict(w, r, &q) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.CreateOrder(r.Context(), actor, p[1], r.Header.Get("Idempotency-Key"), q)
		case p[0] == "posts" && len(p) == 3 && r.Method == "GET":
			out, total, e = repo.List(r.Context(), actor, p[1], q)
		case p[0] == "posts" && len(p) == 4 && r.Method == "GET":
			out, e = repo.Pending(r.Context(), actor, p[1])
		case p[0] == "merchants" && p[2] == "wallet" && r.Method == "PATCH":
			var q WalletPatch
			if admin.DecodeStrict(w, r, &q) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.BindWallet(r.Context(), actor, p[1], q)
		case p[0] == "merchants" && p[2] == "wallet" && r.Method == "GET":
			out, e = repo.Merchant(r.Context(), actor, p[1])
		case p[0] == "merchants" && p[2] == "settlements" && len(p) == 3 && r.Method == "GET":
			out, total, e = repo.Settlements(r.Context(), actor, p[1], q)
		case p[0] == "merchants" && len(p) == 4 && p[3] == "settle" && r.Method == "POST":
			if admin.EmptyBody(w, r) != nil {
				e = ErrInvalid
				break
			}
			var count int
			count, e = repo.SettleMerchant(r.Context(), actor, p[1], 100)
			out = map[string]int{"settled": count}
		default:
			admin.Respond(w, 405, ErrInvalid)
			return
		}
		if e != nil {
			writeError(w, e)
			return
		}
		if total >= 0 {
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
		admin.Respond(w, 200, out)
	})
}

func parts(path string) []string {
	wallet := strings.HasPrefix(path, "/wallet/")
	sphere := strings.HasPrefix(path, "/sphere/")
	if wallet {
		path = "/api/" + strings.TrimPrefix(path, "/wallet/")
	}
	p := sponsorship.Parts(path)
	if len(p) > 0 && (wallet && p[0] != "merchants" || sphere && p[0] != "posts") {
		return nil
	}
	return p
}
