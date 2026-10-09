// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type publishingTestRepo struct {
	Repository
	calls int
	actor string
	patch PublishingSettingsPatch
}

func (r *publishingTestRepo) PublishingSettings(_ context.Context, actor string) (PublishingSettings, error) {
	r.calls++
	r.actor = actor
	return PublishingSettings{ID: actor, AccountID: actor, CreatedAt: time.Unix(1, 0)}, nil
}
func (r *publishingTestRepo) UpdatePublishingSettings(ctx context.Context, actor string, p PublishingSettingsPatch) (PublishingSettings, error) {
	r.patch = p
	return r.PublishingSettings(ctx, actor)
}
func TestPublishingSettingsAuthenticatedScalarWireAndNullIntent(t *testing.T) {
	r := &publishingTestRepo{}
	a := &testAuth{}
	s := &Service{Repository: r, Authority: a}
	w := request(s, "GET", "/api/account/publishing", "", true)
	if w.Code != 200 || r.actor != actorID || len(a.permissions) != 0 {
		t.Fatal(w.Code, r, a)
	}
	var out map[string]json.RawMessage
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if len(out) != 8 {
		t.Fatal(out)
	}
	for _, key := range []string{"default_posting_publisher_id", "default_reply_publisher_id", "default_fediverse_publisher_id", "updated_at", "deleted_at"} {
		if string(out[key]) != "null" {
			t.Fatal(key, out)
		}
	}
	w = request(s, "PATCH", "/api/account/publishing", `{"default_posting_publisher_id":null,"default_reply_publisher_id":"`+actorID+`"}`, true)
	if w.Code != 200 || r.patch.DefaultPostingPublisherID != nil || r.patch.DefaultReplyPublisherID == nil || *r.patch.DefaultReplyPublisherID != actorID {
		t.Fatal(w.Code, r.patch)
	}
	for _, body := range []string{`null`, `[]`, `{"account_id":"` + actorID + `"}`, `{"default_reply_publisher_id":"bad"}`, `{"default_reply_publisher_id":null,"default_reply_publisher_id":null}`} {
		before := r.calls
		w = request(s, "PATCH", "/api/account/publishing", body, true)
		if w.Code != 400 || r.calls != before {
			t.Fatal(body, w.Code)
		}
	}
	before := r.calls
	w = request(s, "GET", "/api/account/publishing", "", false)
	if w.Code != 401 || r.calls != before {
		t.Fatal(w.Code)
	}
	a.denied = true
	w = request(s, "GET", "/api/account/publishing", "", true)
	if w.Code != 403 || r.calls != before {
		t.Fatal(w.Code)
	}
}
