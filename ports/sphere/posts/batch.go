// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/authority"
)

const MaxBatchPosts = 100

// Both source operations use PostsBatchDelete. The separately documented
// visibility permission is not silently substituted for that controller rule.
const BatchPermission = "posts.batch.delete"

type BatchRequest struct {
	PostIDs     []string   `json:"post_ids"`
	Visibility  *int       `json:"visibility"`
	DraftedAt   *time.Time `json:"drafted_at"`
	PublishedAt *time.Time `json:"published_at"`
}
type BatchRepository interface {
	Batch(context.Context, string, BatchRequest, bool) error
}

func (b BatchRequest) Validate(deleting bool) ([]string, error) {
	if len(b.PostIDs) == 0 {
		return nil, &Failure{"POST_BATCH_EMPTY", "PostIds list is empty.", 400}
	}
	if len(b.PostIDs) > MaxBatchPosts {
		return nil, ErrInvalid
	}
	if deleting && (b.Visibility != nil || b.DraftedAt != nil || b.PublishedAt != nil) {
		return nil, ErrInvalid
	}
	if b.Visibility != nil && (*b.Visibility < Public || *b.Visibility > QuietPublic) {
		return nil, ErrInvalid
	}
	if b.DraftedAt != nil && b.PublishedAt != nil {
		return nil, &Failure{"POST_DRAFT_PUBLISH_CONFLICT", "Cannot set both draftedAt and publishedAt.", 400}
	}
	ids := append([]string(nil), b.PostIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if !authority.ValidID(id) || i > 0 && ids[i-1] == id {
			return nil, ErrInvalid
		}
	}
	return ids, nil
}
func decodeBatch(w http.ResponseWriter, r *http.Request, deleting bool) (out BatchRequest, e error) {
	m, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || m != "application/json" {
		return out, &Failure{"UNSUPPORTED_MEDIA_TYPE", "JSON is required.", 415}
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 32768))
	if e != nil || !utf8.Valid(raw) || !uniqueEmbedObject(raw) {
		return out, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, ErrInvalid
	}
	for key := range fields {
		if key != "post_ids" && (deleting || key != "visibility" && key != "drafted_at" && key != "published_at") {
			return out, ErrInvalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil {
		return out, ErrInvalid
	}
	_, e = out.Validate(deleting)
	return
}
func registerBatch(m *http.ServeMux, repo Repository, identity func(http.ResponseWriter, *http.Request, string) (string, bool)) {
	for _, operation := range []string{"delete", "visibility"} {
		deleting := operation == "delete"
		m.HandleFunc("POST /api/posts/batch/"+operation, func(w http.ResponseWriter, r *http.Request) {
			actor, ok := identity(w, r, BatchPermission)
			if !ok {
				return
			}
			if r.URL.RawQuery != "" {
				failure(w, ErrInvalid)
				return
			}
			b, e := decodeBatch(w, r, deleting)
			if e != nil {
				failure(w, e)
				return
			}
			batch, ok := repo.(BatchRepository)
			if !ok {
				failure(w, ErrUnsupported)
				return
			}
			if e = batch.Batch(r.Context(), actor, b, deleting); e != nil {
				failure(w, e)
				return
			}
			respond(w, 204, nil)
		})
	}
}
