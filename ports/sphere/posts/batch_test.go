// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"context"
	"testing"
)

type batchTestRepo struct {
	fakeRepo
	batch    BatchRequest
	deleting bool
}

func (r *batchTestRepo) Batch(_ context.Context, actor string, b BatchRequest, deleting bool) error {
	r.calls++
	r.batch = b
	r.deleting = deleting
	return nil
}
func TestBatchAuthoredEnvelopePermissionAndAtomicDispatch(t *testing.T) {
	r := &batchTestRepo{}
	a := &fakeAuth{actor: testActor}
	h := NewHandler(r, a)
	for _, op := range []string{"delete", "visibility"} {
		body := `{"post_ids":["` + testActor + `"]}`
		if op == "visibility" {
			body = `{"post_ids":["` + testActor + `"],"visibility":3,"drafted_at":null,"published_at":null}`
		}
		w := request(h, "POST", "/api/posts/batch/"+op, body)
		if w.Code != 204 || w.Body.Len() != 0 || a.permission != "posts.batch.delete" || r.deleting != (op == "delete") || len(r.batch.PostIDs) != 1 {
			t.Fatal(op, w.Code, w.Body.String(), r)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"post_ids":[]}`, `{"post_ids":["bad"]}`, `{"post_ids":["` + testActor + `","` + testActor + `"]}`, `{"post_ids":["` + testActor + `"],"visibility":0,"visibility":3}`, `{"post_ids":["` + testActor + `"],"account_id":"` + testActor + `"}`, `{"post_ids":["` + testActor + `"],"published_at":"2026-01-01T00:00:00Z","drafted_at":"2026-01-01T00:00:00Z"}`, `{"post_ids":["` + testActor + `"],"visibility":6}`} {
		before := r.calls
		w := request(h, "POST", "/api/posts/batch/visibility", body)
		if w.Code != 400 || r.calls != before {
			t.Fatal(body, w.Code, r.calls)
		}
	}
	before := r.calls
	a.actor = ""
	if w := request(h, "POST", "/api/posts/batch/delete", `{"post_ids":["`+testActor+`"]}`); w.Code != 401 || r.calls != before {
		t.Fatal(w.Code)
	}
	ids := make([]string, MaxBatchPosts+1)
	if _, e := (BatchRequest{PostIDs: ids}).Validate(false); e == nil {
		t.Fatal("unbounded batch")
	}
}
