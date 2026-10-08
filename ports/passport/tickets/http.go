// SPDX-License-Identifier: AGPL-3.0-only
package tickets

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
	"strconv"
	"strings"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
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
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
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
		reply(w, f.Status, f)
	} else if errors.As(e, &a) {
		if a.Status == 401 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="passport"`)
		}
		reply(w, a.Status, a)
	} else {
		reply(w, 503, ErrUnavailable)
	}
}
func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.Repository == nil || s.Authority == nil {
		failure(w, ErrUnavailable)
		return
	}
	if r.URL.User != nil || r.URL.Path == "" || path.Clean(r.URL.Path) != r.URL.Path || strings.Contains(r.URL.Path, "\\") || r.URL.RawPath != "" && r.URL.EscapedPath() != r.URL.Path {
		failure(w, ErrInvalid)
		return
	}
	present := false
	for name := range r.Header {
		key := strings.ToLower(name)
		if key == "authorization" {
			present = true
		}
		if key == "x-account-id" || key == "x-user-id" || key == "x-role" || key == "proxy-authorization" || strings.HasPrefix(key, "x-test-") || strings.HasPrefix(key, "x-synthetic-") {
			failure(w, ErrForbidden)
			return
		}
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		failure(w, ErrInvalid)
		return
	}
	for key, values := range query {
		if len(values) != 1 || key == "token" || key == "tk" || key == "access_token" {
			failure(w, ErrInvalid)
			return
		}
	}
	c, status, e := route(w, r, query)
	if e != nil {
		failure(w, e)
		return
	}
	actor := ""
	principal, e := s.Authority.AuthenticateRequest(r)
	if e == nil {
		principal, e = s.Authority.AuthenticatePrincipal(r.Context(), principal)
		if e != nil {
			failure(w, e)
			return
		}
		actor = principal.Account.ID
		if binder, ok := s.Repository.(interface {
			BindAuthority(context.Context, authority.Principal) context.Context
		}); ok {
			r = r.WithContext(binder.BindAuthority(r.Context(), principal))
		}
	} else if present || c.Operation != "test.public.list" || !errors.Is(e, authority.ErrInvalid) {
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
	if export, ok := out.Value.(interface{ ExportBytes() ([]byte, string) }); ok {
		data, name := export.ExportBytes()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.WriteHeader(200)
		_, _ = w.Write(data)
		return
	}
	reply(w, status, out.Value)
}
func decode(w http.ResponseWriter, r *http.Request, v any, optional bool) error {
	if optional && r.ContentLength == 0 {
		return nil
	}
	typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || typ != "application/json" {
		return &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil || !utf8.Valid(b) {
		return ErrInvalid
	}
	if optional && bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	if len(bytes.TrimSpace(b)) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e = uniqueJSON(d, 0); e != nil {
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
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 24 {
		return ErrInvalid
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d, depth+1); e != nil {
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
func parseQuery(q url.Values, allowed ...string) (Query, error) {
	out := Query{Take: 20}
	for key := range q {
		found := false
		for _, a := range allowed {
			if key == a {
				found = true
				break
			}
		}
		if !found {
			return out, ErrUnsupported
		}
	}
	var e error
	if q.Has("take") {
		out.Take, e = strconv.Atoi(q.Get("take"))
		if e != nil {
			return out, ErrInvalid
		}
	}
	if q.Has("offset") {
		out.Offset, e = strconv.Atoi(q.Get("offset"))
		if e != nil {
			return out, ErrInvalid
		}
	}
	out.CreatorID = q.Get("creatorId")
	out.AssigneeID = q.Get("assigneeId")
	out.GroupKey = q.Get("groupKey")
	for key, dst := range map[string]**int{"type": &out.Type, "status": &out.Status, "priority": &out.Priority} {
		if q.Has(key) {
			value := q.Get(key)
			n, err := strconv.Atoi(value)
			if err != nil {
				return out, ErrInvalid
			}
			*dst = &n
		}
	}
	if !ValidQuery(out) {
		return out, ErrInvalid
	}
	return out, nil
}
func route(w http.ResponseWriter, r *http.Request, q url.Values) (c Command, status int, e error) {
	status = 200
	c.IdempotencyKey = r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) > 1 || !ValidIdempotency(c.IdempotencyKey) {
		return c, 0, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	method := r.Method
	if len(parts) < 2 || parts[0] != "api" {
		return c, 0, ErrNotFound
	}
	noQuery := func() error {
		if len(q) > 0 {
			return ErrUnsupported
		}
		return nil
	}
	switch {
	case parts[1] == "tickets":
		if len(parts) == 2 && method == "POST" {
			v := TicketCreate{Priority: 1}
			e = decode(w, r, &v, false)
			c.Operation = "ticket.create"
			c.TicketCreate = &v
			if e == nil {
				e = noQuery()
			}
			return
		}
		if len(parts) == 2 && method == "GET" {
			c.Operation = "ticket.list"
			c.Query, e = parseQuery(q, "creatorId", "assigneeId", "type", "status", "priority", "take", "offset")
			return
		}
		if len(parts) == 3 && (parts[2] == "me" || parts[2] == "count") && method == "GET" {
			c.Operation = "ticket." + parts[2]
			allowed := []string{"status", "take", "offset"}
			if parts[2] == "count" {
				allowed = append(allowed, "creatorId", "assigneeId")
			}
			c.Query, e = parseQuery(q, allowed...)
			return
		}
		if len(parts) >= 3 {
			c.ID = parts[2]
		}
		if len(parts) == 3 {
			switch method {
			case "GET":
				c.Operation = "ticket.get"
			case "DELETE":
				c.Operation = "ticket.delete"
				status = 204
			case "PUT":
				v := TicketUpdate{}
				e = decode(w, r, &v, false)
				c.Operation = "ticket.update"
				c.TicketUpdate = &v
			default:
				return c, 0, ErrNotFound
			}
			if e == nil {
				e = noQuery()
			}
			return
		}
		if len(parts) == 4 && method == "POST" {
			switch parts[3] {
			case "messages":
				v := MessageInput{}
				e = decode(w, r, &v, false)
				c.Operation = "ticket.message"
				c.Message = &v
			case "status":
				var v struct {
					Status *int `json:"status"`
				}
				e = decode(w, r, &v, false)
				c.Operation = "ticket.status"
				c.State = v.Status
			case "assign":
				var v struct {
					AssigneeID *string `json:"assignee_id"`
				}
				e = decode(w, r, &v, false)
				c.Operation = "ticket.assign"
				c.AccountID = v.AssigneeID
			default:
				return c, 0, ErrNotFound
			}
			if e == nil {
				e = noQuery()
			}
			return
		}
	case parts[1] == "tests":
		if len(parts) == 2 && method == "GET" {
			c.Operation = "test.public.list"
			c.Query, e = parseQuery(q, "take", "offset")
			return
		}
		if len(parts) == 3 && parts[2] == "activation" && method == "GET" {
			c.Operation = "activation.get"
			e = noQuery()
			return
		}
		if len(parts) == 4 && parts[2] == "activation" && parts[3] == "recheck" && method == "POST" {
			c.Operation = "activation.recheck"
			e = noQuery()
			return
		}
		if len(parts) >= 4 && parts[2] == "attempts" {
			c.ID = parts[3]
			if len(parts) == 4 && method == "GET" {
				c.Operation = "attempt.get"
				e = noQuery()
				return
			}
			if len(parts) == 5 && parts[4] == "submit" && method == "POST" {
				var v struct {
					Answers []AnswerInput `json:"answers"`
				}
				e = decode(w, r, &v, false)
				c.Operation = "attempt.submit"
				c.Answers = v.Answers
				if e == nil {
					e = noQuery()
				}
				return
			}
		}
		if len(parts) >= 4 && parts[2] == "trials" {
			c.Key = parts[3]
			if len(parts) == 4 && method == "GET" {
				c.Operation = "trial.get"
				e = noQuery()
				return
			}
			if len(parts) == 5 && parts[4] == "attempts" && method == "POST" {
				var v struct {
					Categories []string `json:"categories"`
				}
				e = decode(w, r, &v, true)
				c.Operation = "trial.start"
				c.Categories = v.Categories
				if e == nil {
					e = noQuery()
				}
				return
			}
		}
		if len(parts) == 3 && method == "GET" {
			c.Operation = "test.get"
			c.Key = parts[2]
			e = noQuery()
			return
		}
		if len(parts) == 4 && parts[3] == "attempts" && method == "POST" {
			var v struct {
				Categories []string `json:"categories"`
			}
			e = decode(w, r, &v, true)
			c.Operation = "attempt.start"
			c.Key = parts[2]
			c.Categories = v.Categories
			if e == nil {
				e = noQuery()
			}
			return
		}
	case parts[1] == "admin" && len(parts) >= 3:
		switch parts[2] {
		case "tickets":
			if len(parts) >= 4 && parts[3] == "on-call" {
				if len(parts) == 4 {
					if method == "GET" {
						c.Operation = "oncall.list"
					} else if method == "POST" {
						var v struct {
							AccountID *string `json:"account_id"`
						}
						e = decode(w, r, &v, false)
						c.Operation = "oncall.add"
						c.AccountID = v.AccountID
					}
				} else if len(parts) == 5 && method == "DELETE" {
					c.Operation = "oncall.remove"
					c.AccountID = &parts[4]
					status = 204
				}
				if c.Operation != "" {
					if e == nil {
						e = noQuery()
					}
					return
				}
			}
		case "test-question-groups":
			if len(parts) == 3 && method == "GET" {
				c.Operation = "group.list"
				c.Query, e = parseQuery(q, "take", "offset")
				return
			}
			if len(parts) == 3 && method == "POST" || len(parts) == 4 && method == "PUT" {
				v := GroupInput{}
				e = decode(w, r, &v, false)
				c.Operation = "group.save"
				c.Group = &v
				if len(parts) == 4 {
					c.Key = parts[3]
				}
				if e == nil {
					e = noQuery()
				}
				return
			}
			if len(parts) == 4 && method == "DELETE" {
				c.Operation = "group.delete"
				c.Key = parts[3]
				status = 204
				e = noQuery()
				return
			}
		case "test-questions":
			if len(parts) == 3 && method == "GET" {
				c.Operation = "question.list"
				c.Query, e = parseQuery(q, "groupKey", "take", "offset")
				return
			}
			if len(parts) == 4 && parts[3] == "import" && method == "POST" {
				var v struct {
					QuestionGroupKey string          `json:"question_group_key"`
					Questions        []QuestionInput `json:"questions"`
				}
				e = decode(w, r, &v, false)
				c.Operation = "question.import"
				c.Query.GroupKey = v.QuestionGroupKey
				c.Questions = v.Questions
				if e == nil {
					e = noQuery()
				}
				return
			}
			if len(parts) == 4 && parts[3] == "export" && method == "GET" {
				c.Operation = "question.export"
				c.Query, e = parseQuery(q, "groupKey")
				return
			}
			if len(parts) == 4 && parts[3] == "prune" && method == "DELETE" {
				c.Operation = "question.prune"
				c.Query, e = parseQuery(q, "groupKey", "confirm")
				if e != nil {
					return
				}
				v, err := strconv.ParseBool(q.Get("confirm"))
				if err != nil {
					return c, 0, ErrInvalid
				}
				c.Toggle = &v
				return
			}
			if len(parts) == 3 && method == "POST" || len(parts) == 4 && method == "PUT" {
				v := QuestionInput{Points: 1}
				e = decode(w, r, &v, false)
				c.Operation = "question.save"
				c.Question = &v
				if len(parts) == 4 {
					c.ID = parts[3]
				}
				if e == nil {
					e = noQuery()
				}
				return
			}
			if len(parts) == 4 && method == "DELETE" {
				c.Operation = "question.delete"
				c.ID = parts[3]
				status = 204
				e = noQuery()
				return
			}
		case "test-trials":
			if len(parts) == 3 && method == "GET" {
				c.Operation = "trial.list"
				c.Query, e = parseQuery(q, "take", "offset")
				return
			}
			if len(parts) == 3 && method == "POST" || len(parts) == 4 && method == "PUT" {
				v := TrialInput{IsPublished: true}
				e = decode(w, r, &v, false)
				c.Operation = "trial.save"
				c.Trial = &v
				if len(parts) == 4 {
					c.Key = parts[3]
				}
				if e == nil {
					e = noQuery()
				}
				return
			}
		case "tests":
			if len(parts) == 3 && method == "GET" {
				c.Operation = "test.admin.list"
				c.Query, e = parseQuery(q, "take", "offset")
				return
			}
			if len(parts) == 3 && method == "POST" || len(parts) == 4 && method == "PUT" {
				v := DefaultTestInput()
				e = decode(w, r, &v, false)
				c.Operation = "test.save"
				c.Test = &v
				if len(parts) == 4 {
					c.Key = parts[3]
				}
				if e == nil {
					e = noQuery()
				}
				return
			}
			if len(parts) == 6 && parts[3] == "answers" && parts[5] == "review" && method == "POST" {
				v := ReviewInput{}
				e = decode(w, r, &v, false)
				c.Operation = "answer.review"
				c.ID = parts[4]
				c.Review = &v
				if e == nil {
					e = noQuery()
				}
				return
			}
			if len(parts) >= 5 {
				c.Key = parts[3]
				switch parts[4] {
				case "publish", "archive":
					if len(parts) == 5 && method == "POST" {
						name := "published"
						if parts[4] == "archive" {
							name = "archived"
						}
						if _, err := parseQuery(q, name); err != nil {
							return c, 0, err
						}
						v := true
						if q.Has(name) {
							var err error
							v, err = strconv.ParseBool(q.Get(name))
							if err != nil {
								return c, 0, ErrInvalid
							}
						}
						c.Operation = "test." + parts[4]
						c.Toggle = &v
						return
					}
				case "attempts":
					if len(parts) == 5 && method == "GET" {
						c.Operation = "attempt.list"
						c.Query, e = parseExamQuery(q)
						return
					}
				case "trials":
					if len(parts) == 5 && method == "POST" {
						c.Operation = "test.trial.create"
						e = noQuery()
						return
					}
				case "trial":
					if len(parts) == 5 && method == "GET" {
						c.Operation = "test.trial.preview"
						e = noQuery()
						return
					}
					if len(parts) == 6 && method == "POST" {
						if parts[5] == "attempts" {
							c.Operation = "test.trial.start"
							e = noQuery()
							return
						}
						if parts[5] == "grade" {
							var v struct {
								Answers []AnswerInput `json:"answers"`
							}
							e = decode(w, r, &v, false)
							c.Operation = "test.trial.grade"
							c.Answers = v.Answers
							if e == nil {
								e = noQuery()
							}
							return
						}
					}
				}
			}
		}
	}
	return c, 0, ErrNotFound
}

func parseExamQuery(q url.Values) (Query, error) {
	values := url.Values{}
	for key, v := range q {
		values[key] = append([]string{}, v...)
	}
	states := map[string]int{"in_progress": 0, "pending_review": 1, "passed": 2, "failed": 3, "expired": 4}
	if state, ok := states[values.Get("status")]; ok {
		values.Set("status", strconv.Itoa(state))
	}
	return parseQuery(values, "status", "take", "offset")
}
