// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"solsynth.local/go-migration/ports/authority"
)

type HTTP struct {
	Repository Repository
	Identity   Identity
	App        string
}

func (h HTTP) Handler() http.Handler { return http.HandlerFunc(h.ServeHTTP) }
func WriteError(w http.ResponseWriter, e error) {
	code, status := "NOTIFICATION_UNAVAILABLE", 503
	var f *authority.Failure
	if errors.As(e, &f) {
		code, status = f.Code, f.Status
	} else {
		switch {
		case errors.Is(e, ErrInvalid):
			code, status = "INVALID_NOTIFICATION_REQUEST", 400
		case errors.Is(e, ErrNotFound):
			code, status = "NOTIFICATION_NOT_FOUND", 404
		case errors.Is(e, ErrConflict):
			code, status = "NOTIFICATION_CONFLICT", 409
		case errors.Is(e, ErrCursor):
			code, status = "REALTIME_CURSOR_RESET_REQUIRED", 409
		}
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="notifications"`)
	}
	WriteJSON(w, status, map[string]string{"code": code})
}
func WriteJSON(w http.ResponseWriter, status int, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		status = 503
		b = []byte(`{"code":"NOTIFICATION_UNAVAILABLE"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// RequestScope fails closed on alternate credential/identity channels. The host
// supplies Identity from its private proof context, never an account header.
func RequestScope(r *http.Request, identity Identity, app string) (string, error) {
	if identity == nil || !ValidApp(app) {
		return "", ErrUnavailable
	}
	if r.URL.User != nil {
		return "", ErrInvalid
	}
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			return "", authority.ErrForbidden
		}
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return "", ErrInvalid
	}
	for _, k := range []string{"tk", "token", "access_token", "account_id"} {
		if _, exists := q[k]; exists {
			return "", ErrInvalid
		}
	}
	actor, e := identity.Account(r)
	if e != nil {
		return "", e
	}
	if !authority.ValidID(actor) {
		return "", authority.ErrInvalid
	}
	return actor, nil
}
func One(q url.Values, key string) (string, error) {
	v, ok := q[key]
	if !ok {
		return "", nil
	}
	if len(v) != 1 || v[0] == "" {
		return "", ErrInvalid
	}
	return v[0], nil
}
func number(q url.Values, key string, def, max int) (int, error) {
	v, e := One(q, key)
	if e != nil {
		return 0, e
	}
	if v == "" {
		return def, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 0 || n > max {
		return 0, ErrInvalid
	}
	return n, nil
}
func (h HTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, e := RequestScope(r, h.Identity, h.App)
	if e != nil {
		WriteError(w, e)
		return
	}
	if h.Repository == nil {
		WriteError(w, ErrUnavailable)
		return
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		WriteError(w, ErrInvalid)
		return
	}
	app, e := One(q, "app")
	if e != nil || app != "" && app != h.App {
		WriteError(w, ErrInvalid)
		return
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/metoer/") {
		p = "/api/" + strings.TrimPrefix(p, "/metoer/")
	}
	switch {
	case p == "/api/notifications":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		take, e := number(q, "take", 8, 100)
		if e != nil || take == 0 {
			WriteError(w, ErrInvalid)
			return
		}
		offset, e := number(q, "offset", 0, 100000)
		if e != nil {
			WriteError(w, e)
			return
		}
		list, total, e := h.Repository.List(r.Context(), actor, h.App, take, offset)
		if e != nil {
			WriteError(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		WriteJSON(w, 200, list)
	case p == "/api/notifications/count":
		if r.Method != "GET" {
			method(w, "GET")
			return
		}
		n, e := h.Repository.Unread(r.Context(), actor, h.App)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, n)
	case p == "/api/notifications/all/read":
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		if e = h.Repository.MarkRead(r.Context(), actor, h.App, ""); e != nil {
			WriteError(w, e)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
	default:
		parts := strings.Split(strings.TrimPrefix(p, "/api/notifications/"), "/")
		if !strings.HasPrefix(p, "/api/notifications/") || len(parts) != 2 || (parts[1] != "read" && parts[1] != "dismiss") {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			method(w, "POST")
			return
		}
		if !authority.ValidID(parts[0]) {
			WriteError(w, ErrInvalid)
			return
		}
		if parts[1] == "read" {
			e = h.Repository.MarkRead(r.Context(), actor, h.App, parts[0])
		} else {
			e = h.Repository.Dismiss(r.Context(), actor, h.App, parts[0])
		}
		if e != nil {
			WriteError(w, e)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
	}
}
func method(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	WriteJSON(w, 405, map[string]string{"code": "METHOD_NOT_ALLOWED"})
}
