// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
)

// Active numeric controls are authored in the retained Solian pin sheet.
const PublisherPin = 0
const RealmPin = 1
const ReplyPin = 2

var ErrPinnedScope = &Failure{"POST_PINNED_FILTER_REQUIRES_PARAM", "A realm or publisher is required to filter pinned posts.", 400}
var ErrPinFailed = &Failure{"POST_PIN_FAILED", "Only unchained original posts can be pinned in this scope.", 400}
var ErrUnpinFailed = &Failure{"POST_UNPIN_FAILED", "This pin mode is unsupported.", 400}

type PinRepository interface {
	Pin(context.Context, string, string, int) (Post, error)
	Unpin(context.Context, string, string) (Post, error)
}

func ValidatePin(p Post, mode int) error {
	if mode != PublisherPin && mode != RealmPin && mode != ReplyPin {
		return ErrUnsupported
	}
	if p.ChainedPostID != nil || p.RepliedPostID != nil && mode != ReplyPin || p.RepliedPostID == nil && mode == ReplyPin || mode == RealmPin && p.RealmID == nil {
		return ErrPinFailed
	}
	return nil
}
func decodePin(w http.ResponseWriter, r *http.Request) (int, error) {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return 0, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if e != nil || !utf8.Valid(raw) || !uniqueEmbedObject(raw) {
		return 0, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["mode"] == nil {
		return 0, ErrInvalid
	}
	var q struct {
		Mode *int `json:"mode"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || q.Mode == nil {
		return 0, ErrInvalid
	}
	if *q.Mode != PublisherPin && *q.Mode != RealmPin && *q.Mode != ReplyPin {
		return 0, ErrUnsupported
	}
	return *q.Mode, nil
}
func registerPins(m *http.ServeMux, repo Repository, identity func(http.ResponseWriter, *http.Request, string) (string, bool)) {
	for _, method := range []string{"POST", "DELETE"} {
		m.HandleFunc(method+" /api/posts/{id}/pin", func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, "posts.pin")
			if !ok {
				return
			}
			if !authority.ValidID(r.PathValue("id")) {
				failure(w, ErrNotFound)
				return
			}
			if r.URL.RawQuery != "" {
				failure(w, ErrInvalid)
				return
			}
			var mode int
			var e error
			if r.Method == "POST" {
				mode, e = decodePin(w, r)
			} else {
				var b []byte
				b, e = io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
				if e != nil || len(bytes.TrimSpace(b)) > 0 {
					e = ErrInvalid
				}
			}
			if e != nil {
				failure(w, e)
				return
			}
			pins, ok := repo.(PinRepository)
			if !ok {
				failure(w, ErrUnsupported)
				return
			}
			var p Post
			if r.Method == "POST" {
				p, e = pins.Pin(r.Context(), actor, r.PathValue("id"), mode)
			} else {
				p, e = pins.Unpin(r.Context(), actor, r.PathValue("id"))
			}
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, p)
		})
	}
}
