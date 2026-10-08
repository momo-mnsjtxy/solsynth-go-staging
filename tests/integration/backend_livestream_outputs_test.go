// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func TestPGBackendLivestreamOutputPlaybackComposition(t *testing.T) {
	f := newBackendFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.actor)
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	namespace := filepath.Join(root, "streaming", "synthetic")
	if e := os.MkdirAll(namespace, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(root, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	provider := &streamOutputProvider{streamProvider: &streamProvider{endpoint: "wss://synthetic-composed-output-" + f.actor + ".invalid"}, outputs: map[string]live.ProviderOutput{}, policy: "synthetic-v1"}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	outputs := outputConfig()
	cfg := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy, MediaStore: store, MaxMediaBytes: 4 << 20, Livestreams: &application.LivestreamConfig{Provider: provider, Policy: streamConfig(), Outputs: &outputs}}
	app, e := application.NewPostgres(f.db, nil, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	s := continuityBroadcast(t, f.db, app, f.actor, f.token)
	base := "/sphere/livestreams/" + s.ID
	v := backendDecode[live.OutputResponse](t, backendJSONCall(t, app, "POST", base+"/hls", f.token, map[string]string{"destination_id": "synthetic"}, 200))
	if v.PlaylistURL == nil || v.State != "active" {
		t.Fatal(v)
	}
	outputFiles(t, outputFixture{root: root}, v.OutputID)
	w := backendCall(t, app, "GET", *v.PlaylistURL, f.otherToken, "", 200)
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cache policy bypass")
	}
	backendCall(t, app, "GET", *v.PlaylistURL, "", "", 401)
	backendCall(t, app, "GET", "/api/livestreams/"+s.ID+"/hls/"+v.OutputID+"/segment_0.ts", f.otherToken, "", 200)
	backendCall(t, app, "POST", base+"/hls/stop", f.token, "", 200)
	backendCall(t, app, "GET", *v.PlaylistURL, f.otherToken, "", 404)
	cfg.MediaStore = nil
	if _, e = application.NewPostgres(f.db, nil, cfg); e == nil {
		t.Fatal("HLS accepted a missing canonical artifact store")
	}
	if e = os.Chmod(namespace, 0755); e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e == nil {
		t.Fatal("readiness ignored public mounted namespace")
	}
	if e = os.Chmod(namespace, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = app.ProcessExternalDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
}
