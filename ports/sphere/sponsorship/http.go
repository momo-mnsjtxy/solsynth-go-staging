// SPDX-License-Identifier: AGPL-3.0-only
package sponsorship

import (
	"errors"
	"net/http"
	"net/url"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"strconv"
	"strings"
)

func Parts(path string) []string {
	if strings.HasPrefix(path, "/api/") {
		path = strings.TrimPrefix(path, "/api/")
	} else if strings.HasPrefix(path, "/sphere/") {
		path = strings.TrimPrefix(path, "/sphere/")
	} else {
		return nil
	}
	parts := strings.Split(path, "/")
	for i, v := range parts {
		x, e := url.PathUnescape(v)
		if e != nil || x == "" || strings.ContainsAny(x, "/\\\x00") || len(x) > 256 {
			return nil
		}
		parts[i] = x
	}
	return parts
}
func Handles(path string) bool {
	p := Parts(path)
	return len(p) == 2 && p[0] == "ads" || len(p) == 3 && p[0] == "publishers" && p[2] == "ads" || len(p) >= 3 && len(p) <= 4 && p[0] == "posts" && (p[1] == "sponsor" || p[2] == "sponsor")
}
func WriteError(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	var p *posts.Failure
	var ac *accounts.Failure
	status := 503
	var body any = ErrUnavailable
	switch {
	case errors.As(e, &f):
		status = f.Status
		body = f
	case errors.As(e, &a):
		status = a.Status
		body = a
	case errors.As(e, &p):
		status = p.Status
		body = p
	case errors.As(e, &ac):
		status = ac.Status
		body = ac
	case errors.Is(e, c.ErrIdempotency), errors.Is(e, c.ErrConflict):
		status = 409
		body = ErrConflict
	case errors.Is(e, c.ErrInvalid):
		status = 400
		body = ErrInvalid
	case errors.Is(e, c.ErrForbidden):
		status = 403
		body = ErrForbidden
	}
	admin.Respond(w, status, body)
}
func QueryPage(r *http.Request) (Page, error) {
	q := Page{}
	for key, target := range map[string]*int{"take": &q.Take, "offset": &q.Offset} {
		v, ok := r.URL.Query()[key]
		if !ok {
			continue
		}
		if len(v) != 1 {
			return q, ErrInvalid
		}
		n, e := strconv.Atoi(v[0])
		if e != nil || n < 0 {
			return q, ErrInvalid
		}
		*target = n
	}
	return q.Bounds(), nil
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Handles(r.URL.EscapedPath()) {
			http.NotFound(w, r)
			return
		}
		if repo == nil || auth == nil {
			WriteError(w, ErrUnavailable)
			return
		}
		p := Parts(r.URL.EscapedPath())
		required := r.Method != "GET" || p[len(p)-1] == "history" || p[0] == "publishers"
		actor, e := auth(r, required)
		if e != nil {
			WriteError(w, e)
			return
		}
		if required && actor == "" {
			WriteError(w, authority.ErrInvalid)
			return
		}
		q, e := QueryPage(r)
		if e != nil {
			WriteError(w, e)
			return
		}
		var out any
		total := -1
		switch {
		case len(p) == 3 && p[0] == "posts" && p[2] == "sponsor" && r.Method == "POST":
			var request Request
			if admin.DecodeStrict(w, r, &request) != nil {
				e = ErrInvalid
				break
			}
			out, e = repo.CreateOrder(r.Context(), actor, p[1], r.Header.Get("Idempotency-Key"), request)
		case r.Method != "GET":
			w.Header().Set("Allow", "GET")
			admin.Respond(w, 405, ErrInvalid)
			return
		case len(p) == 3 && p[0] == "posts" && p[1] == "sponsor" && p[2] == "current":
			out, e = repo.Current(r.Context(), actor)
		case len(p) == 3 && p[0] == "posts" && p[1] == "sponsor" && p[2] == "leaderboard":
			out, total, e = repo.Leaderboard(r.Context(), actor, q)
		case len(p) == 3 && p[0] == "posts" && p[2] == "sponsor":
			v, x := repo.Total(r.Context(), actor, p[1])
			e = x
			out = map[string]any{"total_amount": v}
		case len(p) == 4 && p[0] == "posts" && p[2] == "sponsor" && p[3] == "history":
			out, total, e = repo.History(r.Context(), actor, p[1], q)
		case len(p) == 2 && p[0] == "ads":
			out, total, e = repo.Ads(r.Context(), actor, p[1], false, q)
		case len(p) == 3 && p[0] == "publishers" && p[2] == "ads":
			out, total, e = repo.Ads(r.Context(), actor, p[1], true, q)
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			WriteError(w, e)
			return
		}
		if total >= 0 {
			w.Header().Set("X-Total", strconv.Itoa(total))
		}
		admin.Respond(w, 200, out)
	})
}
