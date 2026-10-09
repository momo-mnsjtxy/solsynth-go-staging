// SPDX-License-Identifier: AGPL-3.0-only
package progression

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	Repository Repository
	Authority  Authority
	Bind       func(context.Context, authority.Principal) context.Context
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func RouteMatches(path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	return strings.HasPrefix(path, "/api/admin/progression") || strings.HasPrefix(path, "/api/accounts/me/progression") || len(p) >= 4 && p[0] == "api" && p[1] == "accounts" && (p[3] == "badges" || p[2] == "me" && (p[3] == "leveling" || p[3] == "check-in"))
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var a *authority.Failure
	if errors.As(e, &a) {
		send(w, a.Status, a)
		return
	}
	var f *Failure
	if errors.As(e, &f) {
		send(w, f.Status, f)
		return
	}
	send(w, 503, ErrUnavailable)
}
func decode(w http.ResponseWriter, r *http.Request, v any, allowNull bool) error {
	if r.Body == nil {
		return ErrInvalid
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	var raw json.RawMessage
	if d.Decode(&raw) != nil || !allowNull && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return ErrInvalid
	}
	return nil
}
func integer(r *http.Request, key string, def int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil {
		return 0, ErrInvalid
	}
	return n, nil
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if !RouteMatches(path) {
		w.WriteHeader(404)
		return
	}
	authPresent := false
	for k := range r.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			authPresent = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	for k := range r.URL.Query() {
		if k == "token" || k == "access_token" || k == "tk" {
			failure(w, ErrInvalid)
			return
		}
	}
	if r.URL.User != nil {
		failure(w, ErrInvalid)
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	public := len(parts) == 4 && parts[0] == "api" && parts[1] == "accounts" && parts[2] != "me" && parts[3] == "badges" && r.Method == "GET"
	actor := ""
	ctx := r.Context()
	if !public || authPresent {
		if s.Authority == nil || s.Bind == nil {
			failure(w, authority.ErrUnavailable)
			return
		}
		p, e := s.Authority.AuthenticateRequest(r)
		if e != nil {
			failure(w, e)
			return
		}
		actor = p.Account.ID
		ctx = s.Bind(ctx, p)
	}
	if len(parts) == 4 && parts[3] == "badges" && r.Method == "GET" {
		target := parts[2]
		if target == "me" {
			target = actor
		}
		out, e := s.Repository.Badges(ctx, actor, target)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
		return
	}
	if len(parts) == 6 && parts[2] == "me" && parts[3] == "badges" && parts[5] == "active" && r.Method == "POST" {
		if e := s.Repository.ActivateBadge(ctx, actor, parts[4]); e != nil {
			failure(w, e)
			return
		}
		send(w, 200, nil)
		return
	}
	if path == "/api/accounts/me/check-in" {
		values, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil {
			failure(w, ErrInvalid)
			return
		}
		for key, list := range values {
			if len(list) != 1 || (key != "version" && !(key == "backdated" && r.Method == "POST")) {
				failure(w, ErrInvalid)
				return
			}
		}
		if values.Has("version") {
			version, err := strconv.Atoi(values.Get("version"))
			if err != nil || version < 1 || version > 2 {
				failure(w, ErrInvalid)
				return
			}
		}
		switch r.Method {
		case "GET":
			v, e := s.Repository.Today(ctx, actor)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		case "POST":

			var captcha *string
			if e := decode(w, r, &captcha, true); e != nil {
				failure(w, e)
				return
			}
			if captcha != nil && strings.TrimSpace(*captcha) != "" {
				failure(w, ErrUnsupported)
				return
			}
			var v CheckIn
			var e error
			if raw := r.URL.Query().Get("backdated"); raw != "" {
				at, x := time.Parse(time.RFC3339Nano, raw)
				if x != nil {
					failure(w, ErrInvalid)
					return
				}
				ext, ok := s.Repository.(ExtendedRepository)
				if !ok {
					failure(w, ErrUnavailable)
					return
				}
				v, e = ext.BackdatedCheckIn(ctx, actor, at)
			} else {
				v, e = s.Repository.CheckIn(ctx, actor)
			}
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, v)
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(405)
		}
		return
	}
	if r.Method == "GET" {
		take, e := integer(r, "take", 20)
		if e != nil {
			failure(w, e)
			return
		}
		offset, e := integer(r, "offset", 0)
		if e != nil {
			failure(w, e)
			return
		}
		var out any
		switch path {
		case "/api/accounts/me/leveling":
			var total int64
			out, total, e = s.Repository.Experience(ctx, actor, offset, take)
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		case "/api/accounts/me/check-in/history":
			var total int64
			out, total, e = s.Repository.CheckInHistory(ctx, actor, offset, take)
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
		case "/api/accounts/me/progression/achievements":
			out, e = s.Repository.States(ctx, actor, "achievement", r.URL.Query().Get("query"))
		case "/api/accounts/me/progression/quests":
			out, e = s.Repository.States(ctx, actor, "quest", "")
		case "/api/accounts/me/progression/achievements/stats":
			out, e = s.Repository.AchievementStats(ctx, actor)
		case "/api/accounts/me/progression/grants":
			if r.URL.Query().Get("take") == "" {
				take = 50
			}
			out, e = s.Repository.Grants(ctx, actor, take)
		case "/api/accounts/me/progression/level":
			out, e = s.Repository.Summary(ctx, actor)
		case "/api/admin/progression/achievements":
			out, e = s.Repository.Definitions(ctx, actor, "achievement")
		case "/api/admin/progression/quests":
			out, e = s.Repository.Definitions(ctx, actor, "quest")
		default:
			w.WriteHeader(404)
			return
		}
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, out)
		return
	}
	if path == "/api/admin/progression/sync" && r.Method == "POST" {
		b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if e != nil || len(bytes.TrimSpace(b)) != 0 {
			failure(w, ErrInvalid)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrInvalid)
			return
		}
		ext, ok := s.Repository.(ExtendedRepository)
		if !ok {
			failure(w, ErrUnavailable)
			return
		}
		count, e := ext.SyncSeeds(ctx, actor)
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 200, map[string]int{"synchronized": count})
		return
	}
	if path == "/api/admin/progression/levels" && r.Method == "PUT" {
		var v struct {
			Thresholds []int64 `json:"thresholds"`
		}
		if e := decode(w, r, &v, false); e != nil {
			failure(w, e)
			return
		}
		if e := s.Repository.SetLevelPolicy(ctx, actor, v.Thresholds); e != nil {
			failure(w, e)
			return
		}
		send(w, 204, nil)
		return
	}
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "admin" && parts[2] == "progression" && (parts[3] == "achievements" || parts[3] == "quests") {
		kind := strings.TrimSuffix(parts[3], "s")
		if len(parts) == 6 && parts[5] == "enable" && r.Method == "POST" {
			enabled := true
			if raw := r.URL.Query().Get("enabled"); raw != "" {
				var e error
				enabled, e = strconv.ParseBool(raw)
				if e != nil {
					failure(w, ErrInvalid)
					return
				}
			}
			if e := s.Repository.EnableDefinition(ctx, actor, kind, parts[4], enabled); e != nil {
				failure(w, e)
				return
			}
			send(w, 200, nil)
			return
		}
		create := len(parts) == 4 && r.Method == "POST"
		update := len(parts) == 5 && r.Method == "PUT"
		if create || update {
			v := Definition{IsEnabled: true, IsProgressEnabled: true, TargetCount: 1, Trigger: Trigger{Mode: "count", Actions: []string{}, MetaEquals: map[string]string{}}}
			if kind == "quest" {
				v.Schedule = &Schedule{Repeatability: "daily"}
			}
			if e := decode(w, r, &v, false); e != nil {
				failure(w, e)
				return
			}
			id := ""
			if update {
				id = parts[4]
			}
			out, e := s.Repository.SaveDefinition(ctx, actor, kind, id, v, create)
			if e != nil {
				failure(w, e)
				return
			}
			send(w, 200, out)
			return
		}
	}
	w.WriteHeader(405)
}
