// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	"strconv"
	"strings"
	"time"
)

func query(r *http.Request, allowed ...string) (url.Values, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 {
			return nil, ErrInvalid
		}
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			return nil, ErrUnsupported
		}
	}
	return q, nil
}
func origin(q url.Values) (*Point, error) {
	a, b := q.Get("locationWkt"), q.Get("location_wkt")
	if a != "" && b != "" {
		return nil, ErrInvalid
	}
	if a == "" {
		a = b
	}
	if a == "" {
		return nil, nil
	}
	p, e := ParsePoint(a)
	return &p, e
}
func page(q url.Values) (Page, error) {
	p := Page{Take: 20, Radius: 1000}
	var e error
	for key, target := range map[string]*int{"take": &p.Take, "offset": &p.Offset} {
		if q.Has(key) {
			*target, e = strconv.Atoi(q.Get(key))
			if e != nil {
				return p, ErrInvalid
			}
		}
	}
	for key, target := range map[string]**int{"status": &p.Status, "visibility": &p.Visibility} {
		if q.Has(key) {
			v, e := strconv.Atoi(q.Get(key))
			if e != nil {
				return p, ErrInvalid
			}
			*target = &v
		}
	}
	if q.Has("host_only") {
		p.HostOnly, e = strconv.ParseBool(q.Get("host_only"))
		if e != nil {
			return p, ErrInvalid
		}
	}
	if q.Has("distanceMeters") {
		p.Radius, e = strconv.ParseFloat(q.Get("distanceMeters"), 64)
		if e != nil || math.IsNaN(p.Radius) || math.IsInf(p.Radius, 0) || p.Radius <= 0 || p.Radius > 100000 {
			return p, ErrInvalid
		}
	}
	p.Origin, e = origin(q)
	if e != nil {
		return p, e
	}
	if p.Take < 1 || p.Take > 100 || p.Offset < 0 || p.Offset > 100000 {
		return p, ErrInvalid
	}
	return p, nil
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
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "api" {
			http.NotFound(w, r)
			return
		}
		domain := parts[1]
		parts = parts[2:]
		if domain != "meets" && domain != "pins" && domain != "nfc" {
			http.NotFound(w, r)
			return
		}
		if domain != "nfc" || r.Method != "GET" || len(parts) == 1 && parts[0] == "tags" {
			if actor == "" {
				failure(w, ErrUnauthenticated)
				return
			}
		}
		var out any
		status := 200
		permission := ""
		if r.Method != "GET" {
			switch domain {
			case "meets":
				if len(parts) == 0 && r.Method == "POST" {
					permission = "meet.create"
				} else if len(parts) == 1 && r.Method == "DELETE" {
					permission = "meet.delete"
				} else if len(parts) == 2 {
					switch parts[1] {
					case "join":
						permission = "meet.join"
					case "complete":
						permission = "meet.complete"
					case "visibility":
						permission = "meet.visibility.update"
					case "pin":
						permission = "meet.pin.manage"
					}
				}
			case "pins":
				permission = "location.pins.update"
				if len(parts) == 0 {
					permission = "location.pins.create"
				} else if r.Method == "DELETE" || len(parts) == 2 && parts[1] == "disconnect" {
					permission = "location.pins.delete"
				}
			case "nfc":
				if len(parts) == 1 && parts[0] == "tags" {
					permission = "nfc.admin.manage"
				} else if r.Method == "PATCH" {
					permission = "nfc.tags.update"
				} else if r.Method == "DELETE" {
					permission = "nfc.tags.delete"
				} else {
					failure(w, ErrUnsupported)
					return
				}
			}
			if permission == "" {
				http.NotFound(w, r)
				return
			}
			if e = auth.Authorize(r, permission); e != nil {
				failure(w, e)
				return
			}
		}
		switch domain {
		case "meets":
			switch {
			case len(parts) == 0 && r.Method == "POST":
				var v CreateMeet
				_, e = query(r)
				if e == nil {
					e = decode(w, r, &v)
				}
				if e == nil {
					out, e = repo.CreateMeet(r.Context(), actor, v)
				}
			case r.Method == "GET" && (len(parts) == 0 || len(parts) == 1 && parts[0] == "nearby"):
				var q url.Values
				q, e = query(r, "status", "host_only", "offset", "take", "locationWkt", "location_wkt", "distanceMeters")
				var p Page
				if e == nil {
					p, e = page(q)
				}
				if e == nil {
					p.Nearby = len(parts) == 1
					var n int64
					out, n, e = repo.Meets(r.Context(), actor, p)
					w.Header().Set("X-Total", strconv.FormatInt(n, 10))
				}
			case len(parts) == 1 && authority.ValidID(parts[0]):
				switch r.Method {
				case "GET":
					var q url.Values
					q, e = query(r, "locationWkt", "location_wkt")
					var p *Point
					if e == nil {
						p, e = origin(q)
					}
					if e == nil {
						out, e = repo.Meet(r.Context(), actor, parts[0], p)
					}
				case "DELETE":
					e = empty(w, r)
					if e == nil {
						e = repo.DeleteMeet(r.Context(), actor, parts[0])
						status = 204
					}
				default:
					http.NotFound(w, r)
					return
				}
			case len(parts) == 2 && authority.ValidID(parts[0]):
				id, action := parts[0], parts[1]
				switch {
				case action == "join" && r.Method == "POST":
					var q url.Values
					q, e = query(r, "locationWkt", "location_wkt")
					var p *Point
					if e == nil {
						p, e = origin(q)
					}
					if e == nil {
						e = empty(w, r)
					}
					var m Meet
					if e == nil {
						m, e = repo.Join(r.Context(), actor, id, p)
						out = m
					}
					if e == nil && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
						streamMeet(w, r, repo, auth, actor, m, p)
						return
					}
				case action == "complete" && r.Method == "POST":
					e = empty(w, r)
					if e == nil {
						out, e = repo.Complete(r.Context(), actor, id)
					}
				case action == "visibility" && r.Method == "PATCH":
					var v struct {
						Visibility *int `json:"visibility"`
					}
					e = decode(w, r, &v)
					if e == nil && v.Visibility == nil {
						e = ErrInvalid
					}
					if e == nil {
						out, e = repo.Visibility(r.Context(), actor, id, *v.Visibility)
					}
				case action == "pin" && r.Method == "POST":
					var v PinRequest
					e = decode(w, r, &v)
					if e == nil {
						out, e = repo.PutPin(r.Context(), actor, id, "", v)
					}
				case action == "pin" && r.Method == "DELETE":
					e = empty(w, r)
					if e == nil {
						e = repo.DeleteMeetPin(r.Context(), actor, id)
						status = 204
					}
				case action == "pins" && r.Method == "GET":
					var q url.Values
					q, e = query(r, "take", "offset")
					var p Page
					if e == nil {
						p, e = page(q)
					}
					if e == nil {
						var n int64
						out, n, e = repo.Pins(r.Context(), actor, id, p)
						w.Header().Set("X-Total", strconv.FormatInt(n, 10))
					}
				default:
					http.NotFound(w, r)
					return
				}
			default:
				http.NotFound(w, r)
				return
			}
		case "pins":
			switch {
			case len(parts) == 0 && r.Method == "POST":
				var v PinRequest
				e = decode(w, r, &v)
				if e == nil {
					out, e = repo.PutPin(r.Context(), actor, "", "", v)
				}
			case len(parts) == 1 && (parts[0] == "nearby" || parts[0] == "me" || parts[0] == "my-pins") && r.Method == "GET":
				var q url.Values
				q, e = query(r, "take", "offset", "visibility", "locationWkt", "location_wkt", "distanceMeters")
				var p Page
				if e == nil {
					p, e = page(q)
				}
				if e == nil {
					p.HostOnly = parts[0] != "nearby"
					var n int64
					out, n, e = repo.Pins(r.Context(), actor, "", p)
					w.Header().Set("X-Total", strconv.FormatInt(n, 10))
				}
			case len(parts) == 1 && authority.ValidID(parts[0]):
				switch r.Method {
				case "GET":
					out, e = repo.Pin(r.Context(), actor, parts[0])
				case "DELETE":
					e = empty(w, r)
					if e == nil {
						e = repo.DeletePin(r.Context(), actor, parts[0])
						status = 204
					}
				default:
					http.NotFound(w, r)
					return
				}
			case len(parts) == 2 && authority.ValidID(parts[0]):
				id, action := parts[0], parts[1]
				switch {
				case action == "location" && r.Method == "PUT":
					var v PinRequest
					e = decode(w, r, &v)
					if e == nil {
						out, e = repo.PutPin(r.Context(), actor, "", id, v)
					}
				case action == "disconnect" && r.Method == "POST":
					var v struct {
						Keep *bool `json:"keep_on_disconnect"`
					}
					e = decode(w, r, &v)
					keep := true
					if v.Keep != nil {
						keep = *v.Keep
					}
					if e == nil {
						e = repo.Disconnect(r.Context(), actor, id, keep)
						status = 204
					}
				case action == "stream" && r.Method == "GET":
					var p Pin
					p, e = repo.Pin(r.Context(), actor, id)
					if e == nil {
						streamPin(w, r, repo, auth, actor, p)
						return
					}
				default:
					http.NotFound(w, r)
					return
				}
			default:
				http.NotFound(w, r)
				return
			}
		case "nfc":
			switch {
			case (len(parts) == 0 || len(parts) == 1 && parts[0] == "lookup") && r.Method == "GET":
				var q url.Values
				q, e = query(r, "uid", "picc_data", "e", "cmac")
				if e == nil && (q.Has("picc_data") || q.Has("e") || q.Has("cmac") || strings.HasPrefix(strings.ToLower(q.Get("uid")), "solian://")) {
					e = ErrUnsupported
				}
				if e == nil {
					out, e = repo.ResolveTag(r.Context(), actor, q.Get("uid"), false)
				}
			case len(parts) == 1 && parts[0] == "tags":
				switch r.Method {
				case "GET":
					out, e = repo.Tags(r.Context(), actor)
				case "POST":
					var v RegisterTag
					e = decode(w, r, &v)
					if e == nil {
						out, e = repo.RegisterTag(r.Context(), actor, v)
					}
				default:
					http.NotFound(w, r)
					return
				}
			case len(parts) == 2 && parts[0] == "tags" && authority.ValidID(parts[1]):
				switch r.Method {
				case "GET":
					out, e = repo.ResolveTag(r.Context(), actor, parts[1], true)
				case "PATCH":
					var v TagPatch
					e = decode(w, r, &v)
					if e == nil {
						out, e = repo.UpdateTag(r.Context(), actor, parts[1], v)
					}
				case "DELETE":
					e = empty(w, r)
					if e == nil {
						e = repo.DeleteTag(r.Context(), actor, parts[1])
						status = 204
					}
				default:
					http.NotFound(w, r)
					return
				}
			default:
				failure(w, ErrUnsupported)
				return
			}
		}
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, status, out)
	})
}
func event(w http.ResponseWriter, name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); e != nil {
		return e
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
func streamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
}
func streamMeet(w http.ResponseWriter, r *http.Request, repo Repository, auth Auth, actor string, current Meet, origin *Point) {
	streamHeaders(w)
	if event(w, "snapshot", map[string]any{"type": "snapshot", "sent_at": time.Now().UTC(), "meet": current}) != nil {
		return
	}
	last, _ := json.Marshal(current)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for current.Status == Active {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		id, e := auth.Authenticate(r)
		if e != nil || id != actor {
			return
		}
		next, e := repo.Meet(r.Context(), actor, current.ID, origin)
		if e != nil {
			return
		}
		raw, _ := json.Marshal(next)
		if string(raw) == string(last) {
			continue
		}
		name := "participant_joined"
		if next.Status == Completed {
			name = "completed"
		} else if next.Status == Expired {
			name = "expired"
		} else if next.Visibility != current.Visibility {
			name = "visibility_updated"
		} else if len(next.Participants) == len(current.Participants) {
			name = "pin_updated"
		}
		if event(w, name, map[string]any{"type": name, "sent_at": time.Now().UTC(), "meet": next}) != nil {
			return
		}
		current = next
		last = raw
	}
}
func streamPin(w http.ResponseWriter, r *http.Request, repo Repository, auth Auth, actor string, current Pin) {
	streamHeaders(w)
	if event(w, "snapshot", map[string]any{"type": "snapshot", "sent_at": time.Now().UTC(), "pin": current}) != nil {
		return
	}
	last, _ := json.Marshal(current)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		id, e := auth.Authenticate(r)
		if e != nil || id != actor {
			return
		}
		next, e := repo.Pin(r.Context(), actor, current.ID)
		if e != nil {
			return
		}
		raw, _ := json.Marshal(next)
		if string(raw) == string(last) {
			continue
		}
		name := "pin_updated"
		if next.Status == PinOffline {
			name = "pin_offline"
		}
		if event(w, name, map[string]any{"type": name, "sent_at": time.Now().UTC(), "pin": next}) != nil {
			return
		}
		current = next
		last = raw
	}
}
