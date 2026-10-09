// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/ports/sphere/reader"
	"sync/atomic"
	"testing"
)

func TestPGBackendReaderComposition(t *testing.T) {
	f := newBackendFixture(t)
	raw := "https://synthetic-reader.example/post/" + f.actor
	path := "/sphere/scrap/link?url=" + url.QueryEscape(raw)
	backendCall(t, f.app, "GET", path, "", "", 503)
	policy, e := reader.NewPolicy([]string{"https://synthetic-reader.example"})
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	fetch := readerFetchFunc(func(_ context.Context, u string) (reader.Link, error) {
		calls.Add(1)
		return reader.Link{URL: u, Title: "Synthetic preview"}, nil
	})
	visibility := int32(0)
	chat := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	app, e := application.NewPostgres(f.db, nil, application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &chat, Reader: &readerpg.Config{Policy: policy, Fetcher: fetch}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	got := backendDecode[reader.Link](t, backendCall(t, app, "GET", path, "", "", 200))
	if got.URL != raw || got.Title != "Synthetic preview" {
		t.Fatal("preview projection")
	}
	backendCall(t, app, "GET", "/api/scrap/link?url="+url.QueryEscape(raw), "", "", 200)
	if calls.Load() != 1 {
		t.Fatal("gateway cache identity")
	}
	req := fixtureNoBodyRequest("GET", path, "")
	req.Header.Set("X-Account-ID", f.actor)
	w := httptest.NewRecorder()
	app.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("unsupported identity channel", w.Code)
	}
	purge := "/sphere/scrap/link/cache?url=" + url.QueryEscape(raw)
	backendCall(t, app, "DELETE", purge, "", "", 401)
	backendCall(t, app, "DELETE", purge, f.token, "", 200)
	backendCall(t, app, "GET", path, "", "", 200)
	if calls.Load() != 2 {
		t.Fatal("invalidation did not evict cache")
	}
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, nil, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, app, "DELETE", purge, f.token, "", 403)
	backendCall(t, app, "GET", "/passport/scrap/link?url="+url.QueryEscape(raw), "", "", 404)
}
