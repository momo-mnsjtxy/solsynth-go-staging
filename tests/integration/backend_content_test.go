// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/surveys"
	"strings"
	"testing"
)

func TestPGBackendContentComposition(t *testing.T) {
	f := newMediaFixture(t)
	for _, id := range []string{f.actor, f.other} {
		if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), id, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: f.store, MaxMediaBytes: 65536})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	pub := backendDecode[profiles.Publisher](t, backendCall(t, app, "POST", "/sphere/publishers/individual", f.token, fmt.Sprintf(`{"name":%q,"nick":"Content fixture"}`, "content_"+strings.ReplaceAll(f.actor, "-", "")), 200))
	raw := httptest.NewRequest("POST", "/api/media/files", strings.NewReader("composed bytes"))
	raw.Header.Set("Authorization", "Bearer "+f.token)
	raw.Header.Set("Content-Type", "text/plain")
	raw.Header.Set("X-File-Name", "content.txt")
	raw.Header.Set("X-Content-SHA256", mediaDigest("composed bytes"))
	raw.Header.Set("Idempotency-Key", "composed-upload")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	file := backendDecode[media.File](t, w)
	post := backendDecode[posts.Post](t, backendCall(t, app, "POST", "/sphere/posts?pub="+pub.Name, f.token, fmt.Sprintf(`{"content":"","visibility":0,"attachments":[%q]}`, file.ID), 200))
	feed := backendCall(t, app, "GET", "/sphere/posts?pub="+pub.Name, "", "", 200)
	if !bytes.Contains(feed.Body.Bytes(), []byte(file.ID)) {
		t.Fatal("timeline omitted composed attachment", feed.Body.String())
	}
	backendCall(t, app, "GET", "/api/files/"+file.ID, "", "", 200)
	reply := backendDecode[posts.Post](t, backendCall(t, app, "POST", "/api/posts?pub="+pub.Name, f.token, fmt.Sprintf(`{"content":"reply","visibility":0,"replied_post_id":%q}`, post.ID), 200))
	thread := backendCall(t, app, "GET", "/sphere/posts/"+post.ID+"/replies", "", "", 200)
	if !bytes.Contains(thread.Body.Bytes(), []byte(reply.ID)) {
		t.Fatal("reply route composition", thread.Body.String())
	}
	q := surveyQuestions()
	body, _ := json.Marshal(surveys.Patch{Questions: &q, PostID: &post.ID})
	survey := backendDecode[surveys.Survey](t, backendCall(t, app, "POST", "/sphere/surveys?pub="+pub.Name, f.token, string(body), 200))
	backendCall(t, app, "POST", "/api/surveys/"+survey.ID+"/publish", f.token, "", 200)
	backendCall(t, app, "GET", "/sphere/surveys/"+survey.ID, "", "", 200)
	backendCall(t, app, "GET", "/sphere/stickers", "", "", 200)
	backendCall(t, app, "PATCH", "/api/posts/"+post.ID, f.token, `{"visibility":3}`, 200)
	backendCall(t, app, "GET", "/api/files/"+file.ID, "", "", 404)
	backendCall(t, app, "GET", "/api/surveys/"+survey.ID, "", "", 404)
	backendCall(t, app, "GET", "/api/files/"+file.ID, f.token, "", 200)
}
