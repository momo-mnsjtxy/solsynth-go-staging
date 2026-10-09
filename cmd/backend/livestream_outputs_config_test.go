// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/mediastore"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func TestLivestreamOutputConfigurationIsExplicitPrivateAndOffline(t *testing.T) {
	for _, args := range [][]string{{"--enable-livestream-outputs"}, {"--livestream-output-config=/synthetic/missing.json"}, {"--livestream-output-limit=2"}} {
		var c application.Config
		if e := parseExternal(t, args...).apply(&c); e == nil {
			t.Fatal("incomplete output policy enabled")
		}
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(dir, "streaming", "synthetic"), 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(dir, 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	t.Setenv("SYNTHETIC_OUTPUT_LIVE_KEY", "synthetic-only-key")
	t.Setenv("SYNTHETIC_OUTPUT_LIVE_SECRET", strings.Repeat("s", 64))
	t.Setenv("SYNTHETIC_OUTPUT_S3_KEY", "synthetic-storage-key")
	t.Setenv("SYNTHETIC_OUTPUT_S3_SECRET", strings.Repeat("x", 32))
	file := filepath.Join(dir, "output-policy.json")
	write := func(version int, private bool) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"version": version, "destinations": []any{map[string]any{"id": "synthetic", "kind": "hls", "storage": map[string]any{"private_namespace": private, "bucket": "synthetic-private", "access_key_env": "SYNTHETIC_OUTPUT_S3_KEY", "secret_env": "SYNTHETIC_OUTPUT_S3_SECRET"}}}})
		if e := os.WriteFile(file, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	args := []string{"--enable-livestreams", "--enable-livestream-outputs", "--livestream-output-config=" + file, "--livekit-endpoint=wss://synthetic.invalid", "--livekit-api-key-env=SYNTHETIC_OUTPUT_LIVE_KEY", "--livekit-api-secret-env=SYNTHETIC_OUTPUT_LIVE_SECRET", "--livekit-cloud-revocation"}
	write(1, true)
	c := application.Config{MediaStore: store, MaxMediaBytes: 16 << 20}
	if e = parseExternal(t, args...).apply(&c); e != nil || c.Livestreams == nil || c.Livestreams.Outputs == nil {
		t.Fatal(e)
	}
	provider, ok := c.Livestreams.Provider.(live.OutputProvider)
	if !ok || provider.OutputContract() != live.EgressContract {
		t.Fatal("missing supported output provider")
	}
	for _, bad := range []application.Config{{}, {MaxMediaBytes: 16 << 20}} {
		if e = parseExternal(t, args...).apply(&bad); e == nil {
			t.Fatal("HLS accepted without canonical mounted store")
		}
	}
	write(2, true)
	if e = parseExternal(t, args...).apply(&c); e == nil {
		t.Fatal("unknown config version accepted")
	}
	write(1, false)
	if e = parseExternal(t, args...).apply(&c); e == nil {
		t.Fatal("unconfirmed private storage accepted")
	}
	write(1, true)
	if e = os.Chmod(filepath.Join(dir, "streaming", "synthetic"), 0755); e != nil {
		t.Fatal(e)
	}
	if e = parseExternal(t, args...).apply(&c); e == nil {
		t.Fatal("public namespace accepted")
	}
	if e = os.Chmod(filepath.Join(dir, "streaming", "synthetic"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, []byte(`{"version":1,"version":2,"destinations":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = parseExternal(t, args...).apply(&c); e == nil {
		t.Fatal("duplicate JSON accepted")
	}
}
