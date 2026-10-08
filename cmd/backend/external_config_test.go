// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"strings"
	"testing"
	"time"
)

func parseExternal(t *testing.T, args ...string) externalFlagSet {
	t.Helper()
	fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
	v := externalFlags(fs)
	if e := fs.Parse(args); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestExternalConfigurationIsOptInAndOffline(t *testing.T) {
	var c application.Config
	v := parseExternal(t)
	if e := v.apply(&c); e != nil || c.Calls != nil || c.Federation != nil {
		t.Fatal("optional provider was activated", e)
	}
	for _, args := range [][]string{{"--livekit-endpoint=wss://synthetic.invalid"}, {"--federation-send"}, {"--enable-calls"}, {"--external-maintenance-interval=31s"}} {
		v = parseExternal(t, args...)
		if e := v.apply(&c); e == nil {
			t.Fatal("incomplete config accepted", args)
		}
	}
	t.Setenv("SYNTHETIC_CALL_KEY", "synthetic-only-key")
	t.Setenv("SYNTHETIC_CALL_SECRET", strings.Repeat("s", 64))
	v = parseExternal(t, "--enable-calls", "--livekit-endpoint=wss://nonexistent.invalid", "--livekit-api-key-env=SYNTHETIC_CALL_KEY", "--livekit-api-secret-env=SYNTHETIC_CALL_SECRET", "--livekit-cloud-revocation")
	if e := v.apply(&c); e != nil || c.Calls == nil {
		t.Fatal(e)
	}
	if *v.maintenance != 5*time.Second {
		t.Fatal("call cleanup kept wallet interval")
	}
}
func TestFederationConfigurationPinsExistingKeys(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	private := filepath.Join(dir, "synthetic-private.pem")
	public := filepath.Join(dir, "synthetic-public.pem")
	if e = os.WriteFile(private, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}), 0600); e != nil {
		t.Fatal(e)
	}
	publisher := "ca110000-0000-4000-8000-000000000001"
	body := map[string]any{"origin": "https://local.example.test", "allowed_origins": []string{"https://remote.example.test"}, "max_attempts": 3, "delivery_timeout_seconds": 2, "local_keys": []map[string]string{{"publisher_id": publisher, "private_key_file": private}}, "remote_keys": []map[string]string{{"actor": "https://remote.example.test/actor", "inbox": "https://remote.example.test/inbox", "key_id": "https://remote.example.test/actor#key", "public_key_file": public}}}
	raw, _ := json.Marshal(body)
	path := filepath.Join(dir, "config.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := loadFederationConfig(path, false)
	if e != nil || cfg.Transport != nil {
		t.Fatal(e)
	}
	if _, e = cfg.Keys.LocalKey(context.Background(), publisher); e != nil {
		t.Fatal(e)
	}
	if _, e = cfg.Keys.RemoteActor(context.Background(), "https://unlisted.example.test/actor"); e == nil {
		t.Fatal("unlisted key accepted")
	}
	cfg, e = loadFederationConfig(path, true)
	if e != nil || cfg.Transport == nil {
		t.Fatal("explicit outbound transport", e)
	}
	if e = os.Chmod(private, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = loadFederationConfig(path, false); e == nil {
		t.Fatal("public-readable signing key accepted")
	}
	if _, e = loadFederationConfig("relative-config.json", false); e == nil {
		t.Fatal("implicit config location")
	}
}

func TestLivestreamProviderConfigurationIndependentOfCalls(t *testing.T) {
	t.Setenv("SYNTHETIC_STREAM_KEY", "synthetic-only-key")
	t.Setenv("SYNTHETIC_STREAM_SECRET", strings.Repeat("s", 64))
	args := []string{"--enable-livestreams", "--livekit-endpoint=wss://nonexistent.invalid", "--livekit-api-key-env=SYNTHETIC_STREAM_KEY", "--livekit-api-secret-env=SYNTHETIC_STREAM_SECRET", "--livekit-cloud-revocation"}
	var c application.Config
	v := parseExternal(t, args...)
	if e := v.apply(&c); e != nil || c.Livestreams == nil || c.Calls != nil {
		t.Fatal("stream-only binding", e)
	}
	if *v.maintenance != 5*time.Second {
		t.Fatal("cleanup cadence")
	}
	c = application.Config{}
	v = parseExternal(t, append(args, "--enable-calls")...)
	if e := v.apply(&c); e != nil || c.Calls == nil || any(c.Calls.Provider) != any(c.Livestreams.Provider) {
		t.Fatal("provider instance not shared", e)
	}
	for _, bad := range [][]string{{"--livestream-publisher-limit=1"}, {"--enable-livestreams"}, append(args, "--livestream-idle-grace=1s"), append(args, "--livestream-token-ttl=10m")} {
		var c application.Config
		if e := parseExternal(t, bad...).apply(&c); e == nil {
			t.Fatal("unsupported stream config", bad)
		}
	}
}
