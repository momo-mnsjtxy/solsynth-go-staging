// SPDX-License-Identifier: AGPL-3.0-only
package locations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func durableLocationStream(w http.ResponseWriter, r *http.Request, base Repository, auth Auth, actor, id string, pin bool, origin *Point) bool {
	repo, ok := base.(ReplayRepository)
	if !ok {
		return false
	}
	values := r.Header.Values("Last-Event-ID")
	if len(values) > 1 {
		failure(w, ErrInvalid)
		return true
	}
	cursor := uint64(0)
	limit := 0
	if len(values) == 1 && values[0] != "" {
		prefix, raw, found := strings.Cut(values[0], ":")
		if !found || prefix != id {
			failure(w, ErrInvalid)
			return true
		}
		n, e := strconv.ParseUint(raw, 10, 63)
		if e != nil || strconv.FormatUint(n, 10) != raw {
			failure(w, ErrInvalid)
			return true
		}
		cursor = n
		limit = 100
	}
	load := func() (any, error) {
		if pin {
			return base.Pin(r.Context(), actor, id)
		}
		return base.Meet(r.Context(), actor, id, origin)
	}
	events, next, e := repo.LocationEvents(r.Context(), actor, id, pin, origin, cursor, limit)
	if e != nil {
		failure(w, e)
		return true
	}
	state, e := load()
	if e != nil {
		failure(w, e)
		return true
	}
	streamHeaders(w)
	emit := func(sequence uint64, name string, state any, at time.Time) error {
		key := "meet"
		if pin {
			key = "pin"
		}
		payload := map[string]any{"type": name, "sent_at": at, key: state}
		b, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		if _, e = fmt.Fprintf(w, "id: %s:%d\nevent: %s\ndata: %s\n\n", id, sequence, name, b); e != nil {
			return e
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}
	if len(events) == 0 {
		if e = emit(next, "snapshot", state, time.Now().UTC()); e != nil {
			return true
		}
	} else {
		for _, event := range events {
			if e = emit(event.Sequence, event.Type, state, event.OccurredAt); e != nil {
				return true
			}
		}
	}
	cursor = next
	last, _ := json.Marshal(state)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if !pin {
			if m, ok := state.(Meet); ok && m.Status != Active {
				return true
			}
		}
		select {
		case <-r.Context().Done():
			return true
		case <-ticker.C:
		}
		fresh, e := auth.Authenticate(r)
		if e != nil || fresh != actor {
			return true
		}
		events, next, e = repo.LocationEvents(r.Context(), actor, id, pin, origin, cursor, 100)
		if e != nil {
			return true
		}
		state, e = load()
		if e != nil {
			return true
		}
		raw, _ := json.Marshal(state)
		if len(events) > 0 {
			for _, event := range events {
				if e = emit(event.Sequence, event.Type, state, event.OccurredAt); e != nil {
					return true
				}
			}
			cursor = next
		} else if string(raw) != string(last) {
			name := "pin_updated"
			if pin {
				if p, ok := state.(Pin); ok && p.Status == PinOffline {
					name = "pin_offline"
				}
			} else if m, ok := state.(Meet); ok {
				if m.Status == Expired {
					name = "expired"
				} else if m.Status == Completed {
					name = "completed"
				} else {
					name = "snapshot"
				}
			}
			if e = emit(cursor, name, state, time.Now().UTC()); e != nil {
				return true
			}
		}
		last = raw
	}
}
