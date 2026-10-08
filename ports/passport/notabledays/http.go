// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthenticatePrincipal(context.Context, authority.Principal) (authority.Principal, error)
}
type Service struct {
	Repository Repository
	Authority  Authority
}

func (s *Service) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != 204 {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, e error) {
	e = Normalize(e)
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) {
		respond(w, f.Status, f)
	} else if errors.As(e, &a) {
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="calendar"`)
		}
		respond(w, a.Status, a)
	} else {
		respond(w, 503, ErrUnavailable)
	}
}
func public(c Command) bool {
	return c.Operation == "day.list" || c.Operation == "day.get" || ((c.Operation == "event.list" || c.Operation == "event.get" || strings.HasPrefix(c.Operation, "calendar.")) && c.Query.Owner != "" && c.Query.Owner != "me" && c.Operation != "calendar.search")
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.Method == http.MethodHead {
		r = r.Clone(r.Context())
		r.Method = http.MethodGet
		w = headWriter{w}
	}
	escaped := strings.ToLower(r.URL.EscapedPath())
	if r.URL.User != nil || r.URL.Path == "" || path.Clean(r.URL.Path) != r.URL.Path || strings.ContainsAny(r.URL.Path, "\\\x00") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		failure(w, ErrInvalid)
		return
	}
	present := false
	for name := range r.Header {
		k := strings.ToLower(name)
		if k == "authorization" {
			present = true
		}
		if k == "x-account-id" || k == "x-user-id" || k == "x-role" || k == "proxy-authorization" || strings.HasPrefix(k, "x-test-") || strings.HasPrefix(k, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	for key, v := range q {
		if key == "token" || key == "tk" || key == "access_token" || key != "tags" && len(v) != 1 || key == "tags" && len(v) > 16 {
			failure(w, ErrInvalid)
			return
		}
	}
	c, status, e := route(w, r, q)
	if e != nil {
		failure(w, e)
		return
	}
	actor := ""
	p, e := s.Authority.AuthenticateRequest(r)
	if e == nil {
		p, e = s.Authority.AuthenticatePrincipal(r.Context(), p)
		if e != nil {
			failure(w, e)
			return
		}
		actor = p.Account.ID
		if binder, ok := s.Repository.(interface {
			BindAuthority(context.Context, authority.Principal) context.Context
		}); ok {
			r = r.WithContext(binder.BindAuthority(r.Context(), p))
		}
	} else if present || !public(c) || !errors.Is(e, authority.ErrInvalid) {
		failure(w, e)
		return
	}
	out, e := s.Repository.Execute(r.Context(), actor, c)
	if e != nil {
		failure(w, e)
		return
	}
	if out.Total != nil {
		w.Header().Set("X-Total", strconv.FormatInt(*out.Total, 10))
	}
	if status == 201 {
		switch v := out.Value.(type) {
		case Event:
			w.Header().Set("Location", "/api/accounts/me/calendar/events/"+v.ID)
		case Subscription:
			w.Header().Set("Location", "/api/accounts/me/calendar/subscriptions/"+v.TargetAccountID)
		}
	}
	respond(w, status, out.Value)
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(b []byte) (int, error) { return len(b), nil }
func decode(w http.ResponseWriter, r *http.Request, v any) ([]byte, error) {
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return nil, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if e != nil || !utf8.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = unique(d, 0); e != nil {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return nil, ErrInvalid
	}
	return b, nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delimiter, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func parseTag(value string) (int, error) {
	names := map[string]int{"holiday": Holiday, "event": EventTag, "anniversary": Anniversary, "memorial": Memorial, "festival": Festival}
	if n, ok := names[strings.ToLower(value)]; ok {
		return n, nil
	}
	n, e := strconv.Atoi(value)
	if e != nil || n < 0 || n > 4 {
		return 0, ErrInvalid
	}
	return n, nil
}
func query(values url.Values, defaults Query, allowed ...string) (Query, error) {
	q := defaults
	for key := range values {
		found := false
		for _, name := range allowed {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return q, ErrUnsupported
		}
	}
	for name, dst := range map[string]*int{"year": &q.Year, "month": &q.Month, "offset": &q.Offset, "take": &q.Take} {
		if values.Has(name) {
			v, e := strconv.Atoi(values.Get(name))
			if e != nil {
				return q, ErrInvalid
			}
			*dst = v
		}
	}
	if values.Has("region") {
		q.Region = values.Get("region")
	}
	if q.Region != "" && !regionPattern.MatchString(q.Region) {
		return q, ErrInvalid
	}
	if values.Has("accountId") {
		q.AccountID = values.Get("accountId")
	}
	q.Text = values.Get("query")
	q.Tags = values["tags"]
	if values.Has("timeZone") {
		q.TimeZone = values.Get("timeZone")
		if _, e := Zone(q.TimeZone); e != nil {
			return q, e
		}
	}
	for name, dst := range map[string]**time.Time{"startTime": &q.StartTime, "endTime": &q.EndTime} {
		if values.Has(name) {
			v, e := time.Parse(time.RFC3339Nano, values.Get(name))
			if e != nil || !ValidInstant(v) {
				return q, ErrInvalid
			}
			*dst = &v
		}
	}
	if values.Has("tag") && values.Has("notableDayTag") {
		return q, ErrInvalid
	}
	for _, name := range []string{"tag", "notableDayTag"} {
		if values.Has(name) {
			v, e := parseTag(values.Get(name))
			if e != nil {
				return q, e
			}
			q.Tag = &v
		}
	}
	if values.Has("layer") {
		v, e := strconv.Atoi(values.Get("layer"))
		if e != nil {
			return q, ErrInvalid
		}
		q.Layer = &v
	}
	if values.Has("includeNotableDays") {
		v, e := strconv.ParseBool(values.Get("includeNotableDays"))
		if e != nil {
			return q, ErrInvalid
		}
		q.IncludeNotable = v
	}
	if !ValidQuery(q) {
		return q, ErrInvalid
	}
	return q, nil
}
func route(w http.ResponseWriter, r *http.Request, values url.Values) (c Command, status int, e error) {
	status = 200
	c.IdempotencyKey = r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || !ValidKey(c.IdempotencyKey) {
		return c, 0, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	method := r.Method
	none := func() error {
		if len(values) > 0 {
			return ErrUnsupported
		}
		return nil
	}
	if len(parts) < 2 || parts[0] != "api" {
		return c, 0, ErrNotFound
	}
	if parts[1] == "notable-days" {
		if len(parts) == 2 && method == "GET" {
			c.Operation = "day.list"
			c.Query, e = query(values, Query{Take: 50, Region: "CN"}, "year", "region", "tag", "layer", "offset", "take")
			return
		}
		if len(parts) == 3 {
			c.ID = parts[2]
			if method == "GET" {
				c.Operation = "day.get"
				e = none()
				return
			}
			if method == "DELETE" {
				c.Operation = "day.delete"
				status = 204
				e = none()
				return
			}
		}
		if len(parts) == 2 && method == "POST" || len(parts) == 3 && method == "PUT" {
			v := DayInput{Region: "CN", IsAllDay: true, Tags: []int{}}
			_, e = decode(w, r, &v)
			c.Day = &v
			c.Operation = "day.create"
			if len(parts) == 3 {
				c.Operation = "day.update"
				c.ID = parts[2]
			}
			if e == nil {
				e = none()
			}
			return
		}
		return c, 0, ErrNotFound
	}
	if len(parts) < 4 || parts[1] != "accounts" || parts[3] != "calendar" {
		return c, 0, ErrNotFound
	}
	owner := parts[2]
	c.Query = Query{Owner: owner, Take: 50}
	if len(parts) == 4 && method == "GET" {
		c.Operation = "calendar.month"
		c.Query, e = query(values, c.Query, "year", "month", "includeNotableDays", "layer", "timeZone")
		return
	}
	if len(parts) < 5 {
		return c, 0, ErrNotFound
	}
	if len(parts) == 5 && method == "GET" {
		switch parts[4] {
		case "merged":
			c.Operation = "calendar.merged"
			c.Query.IncludeNotable = true
			c.Query, e = query(values, c.Query, "year", "month", "layer", "timeZone")
			return
		case "countdown":
			c.Operation = "calendar.countdown"
			c.Query.Take = 5
			c.Query.IncludeNotable = true
			c.Query, e = query(values, c.Query, "take", "offset", "includeNotableDays", "tag", "layer")
			return
		case "search":
			if owner != "me" {
				return c, 0, ErrNotFound
			}
			c.Operation = "calendar.search"
			c.Query, e = query(values, c.Query, "query", "accountId", "tags", "startTime", "endTime", "notableDayTag", "region", "take", "offset")
			return
		case "tags":
			if owner != "me" {
				return c, 0, ErrNotFound
			}
			c.Operation = "event.tags"
			e = none()
			return
		case "subscriptions":
			if owner != "me" {
				return c, 0, ErrNotFound
			}
			c.Operation = "subscription.list"
			e = none()
			return
		}
	}
	if len(parts) == 6 && parts[4] == "notable-days" && owner == "me" && method == "GET" {
		c.Operation = "day.generated"
		c.OccurrenceKey = parts[5]
		e = none()
		return
	}
	if len(parts) == 6 && parts[4] == "subscriptions" && owner == "me" {
		if parts[5] == "subscribers" && method == "GET" {
			c.Operation = "subscription.subscribers"
			e = none()
			return
		}
		c.TargetID = parts[5]
		if method == "POST" {
			c.Operation = "subscription.add"
			status = 201
			e = none()
			return
		}
		if method == "DELETE" {
			c.Operation = "subscription.remove"
			status = 204
			e = none()
			return
		}
	}
	if parts[4] == "events" {
		if len(parts) == 5 && method == "GET" {
			c.Operation = "event.list"
			allowed := []string{"startTime", "endTime", "take", "offset"}
			if owner == "me" {
				allowed = append(allowed, "accountId", "query", "tags")
			}
			c.Query, e = query(values, c.Query, allowed...)
			return
		}
		if len(parts) == 6 {
			c.ID = parts[5]
			if method == "GET" {
				c.Operation = "event.get"
				e = none()
				return
			}
			if method == "DELETE" && owner == "me" {
				c.Operation = "event.delete"
				status = 204
				e = none()
				return
			}
		}
		if owner == "me" && (len(parts) == 5 && method == "POST" || len(parts) == 6 && method == "PUT") {
			var v EventPatch
			var b []byte
			b, e = decode(w, r, &v)
			if e != nil {
				return
			}
			var keys map[string]json.RawMessage
			if e = json.Unmarshal(b, &keys); e != nil {
				return c, 0, ErrInvalid
			}
			_, v.RecurrencePresent = keys["recurrence"]
			c.Patch = &v
			c.Operation = "event.create"
			status = 201
			if len(parts) == 6 {
				c.Operation = "event.update"
				status = 200
				c.ID = parts[5]
			}
			e = none()
			return
		}
	}
	return c, 0, ErrNotFound
}
