// SPDX-License-Identifier: AGPL-3.0-only
package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strconv"
	"strings"
	"unicode/utf8"
)

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	var f *Failure
	var a *authority.Failure
	var p *posts.Failure
	switch {
	case errors.As(e, &f):
		respond(w, f.Status, f)
	case errors.As(e, &a):
		respond(w, a.Status, a)
	case errors.As(e, &p):
		respond(w, p.Status, p)
	default:
		respond(w, 503, ErrUnavailable)
	}
}
func checkJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return ErrInvalid
	}
	v, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return ErrInvalid
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = checkJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, e = d.Token()
	return e
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		return ErrInvalid
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if e != nil || !utf8.Valid(b) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = checkJSON(d, 0); e != nil {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return ErrInvalid
	}
	return nil
}
func empty(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil {
		return ErrInvalid
	}
	s := strings.TrimSpace(string(b))
	if s != "" && s != "{}" {
		return ErrInvalid
	}
	return nil
}
func visibility(raw string) (int, error) {
	switch strings.ToLower(raw) {
	case "public":
		return posts.Public, nil
	case "private":
		return posts.Private, nil
	case "unlisted":
		return posts.Unlisted, nil
	case "friends":
		return posts.Friends, nil
	case "close_friends":
		return posts.CloseFriends, nil
	}
	v, e := strconv.Atoi(raw)
	if e != nil || v < posts.Public || v > posts.CloseFriends {
		return 0, ErrInvalid
	}
	return v, nil
}
func query(r *http.Request, k Kind, members bool) (Query, error) {
	p := Query{Take: 50}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil || len(r.URL.RawQuery) > 4096 {
		return p, ErrInvalid
	}
	for key, v := range q {
		if len(v) != 1 {
			return p, ErrInvalid
		}
		s := v[0]
		switch key {
		case "query":
			p.Text = s
		case "take":
			p.Take, e = strconv.Atoi(s)
		case "offset":
			p.Offset, e = strconv.Atoi(s)
		case "publisher_id":
			if k != PostKind {
				return p, ErrInvalid
			}
			p.PublisherID = s
		case "realm_id":
			if k != PostKind {
				return p, ErrInvalid
			}
			p.RealmID = s
		case "account_id":
			if k == PostKind {
				return p, ErrInvalid
			}
			p.AccountID = s
		case "visibility":
			if k != PostKind {
				return p, ErrInvalid
			}
			v, x := visibility(s)
			e = x
			p.Visibility = &v
		case "shadowban_reason":
			if k == RealmKind || !ValidReason(s, true) {
				return p, ErrInvalid
			}
			p.Reason = &s
		case "type":
			if k != PublisherKind {
				return p, ErrInvalid
			}
			v := 0
			switch s {
			case "individual", "0":
				v = 0
			case "organizational", "1":
				v = 1
			default:
				return p, ErrInvalid
			}
			p.Type = &v
		case "role":
			if !members {
				return p, ErrInvalid
			}
			v, x := strconv.Atoi(s)
			e = x
			p.Role = &v
		case "locked", "drafted", "shadowbanned", "is_public", "is_community", "pending_only":
			b, x := strconv.ParseBool(s)
			e = x
			switch key {
			case "locked":
				if k != PostKind {
					return p, ErrInvalid
				}
				p.Locked = &b
			case "drafted":
				if k != PostKind {
					return p, ErrInvalid
				}
				p.Drafted = &b
			case "shadowbanned":
				if k == RealmKind {
					return p, ErrInvalid
				}
				p.Banned = &b
			case "is_public":
				if k != RealmKind {
					return p, ErrInvalid
				}
				p.Public = &b
			case "is_community":
				if k != RealmKind {
					return p, ErrInvalid
				}
				p.Community = &b
			case "pending_only":
				if !members {
					return p, ErrInvalid
				}
				p.Pending = &b
			}
		case "verified":
			if k == PostKind {
				return p, ErrInvalid
			}
			b, x := strconv.ParseBool(s)
			e = x
			p.Verified = &b
		case "gatekept":
			return p, ErrUnsupported
		default:
			return p, ErrInvalid
		}
		if e != nil {
			return p, ErrInvalid
		}
	}
	if p.Take < 1 || p.Take > 200 || p.Offset < 0 || p.Offset > 100000 || len(p.Text) > 1024 {
		return p, ErrInvalid
	}
	for _, id := range []string{p.PublisherID, p.RealmID, p.AccountID} {
		if id != "" && !authority.ValidID(id) {
			return p, ErrInvalid
		}
	}
	if p.Role != nil && (*p.Role < 0 || *p.Role > 100) {
		return p, ErrInvalid
	}
	return p, nil
}
func Handles(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "posts" && parts[3] == "realm" && parts[4] == "moderate" {
		return true
	}
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "admin" {
		return false
	}
	switch parts[2] {
	case "publishers", "realms":
		return true
	case "posts":
		return len(parts) == 3 || authority.ValidID(parts[3]) || (len(parts) == 5 && parts[3] == "lock" && parts[4] == "batch")
	}
	return false
}
func NewHandler(repo Repository, auth Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo == nil || auth == nil {
			failure(w, ErrUnavailable)
			return
		}
		actor, e := auth.Authenticate(r)
		if e != nil {
			failure(w, e)
			return
		}
		if actor == "" {
			failure(w, authority.ErrInvalid)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) == 5 && parts[0] == "api" && parts[1] == "posts" && parts[3] == "realm" && parts[4] == "moderate" {
			if r.Method != "POST" {
				respond(w, 405, ErrInvalid)
				return
			}
			if !authority.ValidID(parts[2]) || r.URL.RawQuery != "" {
				failure(w, ErrInvalid)
				return
			}
			var body struct {
				Reason *string `json:"reason"`
			}
			if e = decode(w, r, &body); e != nil {
				failure(w, e)
				return
			}
			v, e := repo.ModerateRealmPost(r.Context(), actor, parts[2], body.Reason)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]any{"success": true, "message": "Post removed from realm", "post": v})
			return
		}
		if len(parts) < 3 || parts[0] != "api" || parts[1] != "admin" {
			http.NotFound(w, r)
			return
		}
		var kind Kind
		switch parts[2] {
		case "posts":
			kind = PostKind
		case "publishers":
			kind = PublisherKind
		case "realms":
			kind = RealmKind
		default:
			http.NotFound(w, r)
			return
		}
		tail := parts[3:]
		if len(tail) == 0 && r.Method == "GET" {
			q, e := query(r, kind, false)
			if e != nil {
				failure(w, e)
				return
			}
			v, total, e := repo.List(r.Context(), actor, kind, q)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			respond(w, 200, v)
			return
		}
		if kind == RealmKind && len(tail) == 2 && tail[1] == "members" && r.Method == "GET" {
			q, e := query(r, kind, true)
			if e != nil {
				failure(w, e)
				return
			}
			v, total, e := repo.Members(r.Context(), actor, tail[0], q)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			respond(w, 200, v)
			return
		}
		if r.URL.RawQuery != "" {
			failure(w, ErrInvalid)
			return
		}
		if len(tail) == 0 {
			respond(w, 405, ErrInvalid)
			return
		}
		if kind == PostKind && ((len(tail) == 3 && tail[1] == "lock" && tail[2] == "batch" && r.Method == "POST") || (len(tail) == 2 && tail[0] == "lock" && tail[1] == "batch" && r.Method == "DELETE")) {
			if r.Method == "POST" && !authority.ValidID(tail[0]) {
				failure(w, ErrInvalid)
				return
			}
			var ids []string
			if e = decode(w, r, &ids); e != nil {
				failure(w, e)
				return
			}
			action := "lock"
			if r.Method == "DELETE" {
				action = "unlock"
			}
			v, e := repo.Mutate(r.Context(), actor, kind, "", Command{Action: action, IDs: ids})
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
			return
		}
		ref := tail[0]
		if ref == "" || len(ref) > 256 || kind == PostKind && !authority.ValidID(ref) {
			failure(w, ErrInvalid)
			return
		}
		if len(tail) == 1 && r.Method == "GET" {
			v, e := repo.Get(r.Context(), actor, kind, ref)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
			return
		}
		if kind == PostKind && len(tail) == 2 && tail[1] == "lock" && r.Method == "GET" {
			v, e := repo.Get(r.Context(), actor, kind, ref)
			if e != nil {
				failure(w, e)
				return
			}
			p, ok := v.(Post)
			if !ok {
				failure(w, ErrUnavailable)
				return
			}
			respond(w, 200, map[string]any{"locked": p.LockedAt != nil, "locked_at": p.LockedAt})
			return
		}
		c := Command{}
		status := 200
		switch {
		case kind == PostKind && len(tail) == 2 && tail[1] == "automod-review" && r.Method == "POST":
			c.Action = "automod_review"
			c.Automod = &AutomodReview{}
			e = decode(w, r, c.Automod)
		case len(tail) == 1 && r.Method == "DELETE":
			c.Action = "delete"
			status = 204
			e = empty(w, r)
		case len(tail) == 1 && r.Method == "PATCH" && kind == PublisherKind:
			c.Action = "update"
			c.Publisher = &PublisherPatch{}
			e = decode(w, r, c.Publisher)
		case len(tail) == 1 && r.Method == "PATCH" && kind == RealmKind:
			c.Action = "update"
			c.Realm = &RealmPatch{}
			e = decode(w, r, c.Realm)
		case kind == PostKind && len(tail) == 2 && tail[1] == "lock" && (r.Method == "POST" || r.Method == "DELETE"):
			c.Action = "lock"
			if r.Method == "DELETE" {
				c.Action = "unlock"
			}
			e = empty(w, r)
		case kind == PostKind && len(tail) == 2 && tail[1] == "visibility" && r.Method == "POST":
			var body struct {
				Visibility json.RawMessage `json:"visibility"`
			}
			e = decode(w, r, &body)
			if e == nil {
				raw := string(body.Visibility)
				if len(raw) > 0 && raw[0] == '"' {
					e = json.Unmarshal(body.Visibility, &raw)
				}
				if e == nil {
					v, x := visibility(raw)
					e = x
					c.Visibility = &v
				}
			}
			c.Action = "visibility"
		case kind != RealmKind && len(tail) == 2 && tail[1] == "shadowban" && r.Method == "POST":
			var body struct {
				Reason *string `json:"reason"`
			}
			e = decode(w, r, &body)
			c.Action, c.Reason = "shadowban", body.Reason
		case kind != RealmKind && len(tail) == 2 && tail[1] == "shadowban" && r.Method == "DELETE":
			c.Action = "unshadowban"
			e = empty(w, r)
		case kind == PostKind && len(tail) == 3 && tail[1] == "realm" && tail[2] == "remove" && r.Method == "POST":
			var body struct {
				Reason *string `json:"reason"`
			}
			e = decode(w, r, &body)
			c.Action, c.Reason = "remove_realm", body.Reason
		case kind == RealmKind && len(tail) == 4 && tail[1] == "members" && tail[3] == "role" && r.Method == "PATCH":
			var body struct {
				Role *int `json:"role"`
			}
			e = decode(w, r, &body)
			c.Action, c.AccountID, c.Role = "role", tail[2], body.Role
		case kind == RealmKind && len(tail) == 3 && tail[1] == "members" && r.Method == "DELETE":
			c.Action, c.AccountID = "remove_member", tail[2]
			status = 204
			e = empty(w, r)
		case len(tail) == 2 && tail[1] == "verification":
			failure(w, ErrUnsupported)
			return
		default:
			http.NotFound(w, r)
			return
		}
		if e != nil {
			failure(w, e)
			return
		}
		v, e := repo.Mutate(r.Context(), actor, kind, ref, c)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, status, v)
	})
}
