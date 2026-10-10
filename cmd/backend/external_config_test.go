// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/ports/federation"
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
	body := map[string]any{"origin": "https://local.example.test", "allowed_origins": []string{"https://remote.example.test"}, "max_attempts": 3, "delivery_timeout_seconds": 2, "local_keys": []map[string]string{{"publisher_id": publisher, "private_key_file": private}}, "remote_keys": []map[string]string{{"actor": "https://remote.example.test/actor", "inbox": "https://remote.example.test/inbox", "key_id": "https://remote.example.test/actor#key", "incarnation": "ca110000-0000-4000-8000-000000000099", "public_key_file": public}}}
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
	if _, ok := cfg.Keys.(federation.IdempotentKeyAdministrator); ok {
		t.Fatal("static key configuration exposed mutation custody")
	}
	if _, e = cfg.Keys.RemoteActor(context.Background(), "https://unlisted.example.test/actor"); e == nil {
		t.Fatal("unlisted key accepted")
	}
	if remote, remoteErr := cfg.Keys.RemoteActor(context.Background(), "https://remote.example.test/actor"); remoteErr != nil || remote.Incarnation != "ca110000-0000-4000-8000-000000000099" {
		t.Fatal("configured remote incarnation was not retained", remote.Incarnation, remoteErr)
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

func TestFederationLocalCustodyRotationIsCASAndOperationIdempotent(t *testing.T) {
	initial, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	keyDir, operationDir := filepath.Join(root, "keys"), filepath.Join(root, "operations")
	if e = os.Mkdir(keyDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(operationDir, 0700); e != nil {
		t.Fatal(e)
	}
	private := filepath.Join(keyDir, "publisher.pem")
	public := filepath.Join(root, "remote.pem")
	if e = os.WriteFile(private, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(initial)}), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&initial.PublicKey)}), 0600); e != nil {
		t.Fatal(e)
	}
	publisher := "ca110000-0000-4000-8000-000000000001"
	body := map[string]any{
		"origin": "https://local.example.test", "allowed_origins": []string{"https://remote.example.test"}, "max_attempts": 3, "delivery_timeout_seconds": 2,
		"local_key_custody": map[string]string{"key_directory": keyDir, "operation_directory": operationDir},
		"local_keys":        []map[string]string{{"publisher_id": publisher, "private_key_file": private}},
		"remote_keys":       []map[string]string{{"actor": "https://remote.example.test/actor", "inbox": "https://remote.example.test/inbox", "key_id": "https://remote.example.test/actor#key", "incarnation": "ca110000-0000-4000-8000-000000000099", "public_key_file": public}},
	}
	raw, _ := json.Marshal(body)
	configPath := filepath.Join(root, "config.json")
	if e = os.WriteFile(configPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := loadFederationConfig(configPath, false)
	if e != nil {
		t.Fatal(e)
	}
	admin, ok := cfg.Keys.(federation.IdempotentKeyAdministrator)
	if !ok {
		t.Fatal("explicit custody did not expose durable key administration")
	}
	oldFingerprint, _ := federation.KeyFingerprint(&initial.PublicKey)
	operation := "ca110000-0000-4000-8000-000000000002"
	rotated, e := admin.ExecuteLocalKeyCommand(context.Background(), operation, publisher, "rotate", oldFingerprint)
	if e != nil || rotated == oldFingerprint || len(rotated) != 64 {
		t.Fatal("rotation failed", rotated, e)
	}
	current, e := cfg.Keys.LocalKey(context.Background(), publisher)
	if e != nil {
		t.Fatal(e)
	}
	pemBody, _ := federation.PEM(current)
	publicKey, e := federation.ParsePublicPEM(pemBody)
	if e != nil {
		t.Fatal(e)
	}
	currentFingerprint, _ := federation.KeyFingerprint(publicKey)
	if currentFingerprint != rotated {
		t.Fatal("atomic key replacement did not become current", currentFingerprint, rotated)
	}
	if replay, replayErr := admin.ExecuteLocalKeyCommand(context.Background(), operation, publisher, "rotate", oldFingerprint); replayErr != nil || replay != rotated {
		t.Fatal("operation replay was not idempotent", replay, replayErr)
	}
	ensureOperation := "ca110000-0000-4000-8000-000000000003"
	if ensured, ensureErr := admin.ExecuteLocalKeyCommand(context.Background(), ensureOperation, publisher, "ensure", rotated); ensureErr != nil || ensured != rotated {
		t.Fatal("ensure changed a current durable key", ensured, ensureErr)
	}
	if _, staleErr := admin.ExecuteLocalKeyCommand(context.Background(), "ca110000-0000-4000-8000-000000000004", publisher, "rotate", oldFingerprint); !errors.Is(staleErr, federation.ErrForbidden) {
		t.Fatal("stale expected fingerprint rotated key", staleErr)
	}
	reloaded, e := loadFederationConfig(configPath, false)
	if e != nil {
		t.Fatal(e)
	}
	reloadedAdmin := reloaded.Keys.(federation.IdempotentKeyAdministrator)
	if replay, replayErr := reloadedAdmin.ExecuteLocalKeyCommand(context.Background(), operation, publisher, "rotate", oldFingerprint); replayErr != nil || replay != rotated {
		t.Fatal("receipt did not survive provider restart", replay, replayErr)
	}
}

func TestFederationLocalCustodyCanEnsureMissingKeyAndRejectsAliasedFiles(t *testing.T) {
	initial, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	keyDir, operationDir := filepath.Join(root, "keys"), filepath.Join(root, "operations")
	if err = os.Mkdir(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(operationDir, 0700); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "remote.pem")
	if err = os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&initial.PublicKey)}), 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(keyDir, "generated.pem")
	secondMissing := filepath.Join(keyDir, "second.pem")
	publisher := "ca110000-0000-4000-8000-000000000001"
	secondPublisher := "ca110000-0000-4000-8000-000000000006"
	body := map[string]any{
		"origin": "https://local.example.test", "allowed_origins": []string{"https://remote.example.test"}, "max_attempts": 3, "delivery_timeout_seconds": 2,
		"local_key_custody": map[string]string{"key_directory": keyDir, "operation_directory": operationDir},
		"local_keys":        []map[string]string{{"publisher_id": publisher, "private_key_file": missing}, {"publisher_id": secondPublisher, "private_key_file": secondMissing}},
		"remote_keys":       []map[string]string{{"actor": "https://remote.example.test/actor", "inbox": "https://remote.example.test/inbox", "key_id": "https://remote.example.test/actor#key", "incarnation": "ca110000-0000-4000-8000-000000000099", "public_key_file": public}},
	}
	raw, _ := json.Marshal(body)
	configPath := filepath.Join(root, "config.json")
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFederationConfig(configPath, false)
	if err != nil {
		t.Fatal(err)
	}
	admin := cfg.Keys.(federation.IdempotentKeyAdministrator)
	op := "ca110000-0000-4000-8000-000000000002"
	got, err := admin.ExecuteLocalKeyCommand(context.Background(), op, publisher, "ensure", "")
	if err != nil || len(got) != 64 {
		t.Fatal("missing-key ensure", got, err)
	}
	if replay, replayErr := admin.ExecuteLocalKeyCommand(context.Background(), op, publisher, "ensure", ""); replayErr != nil || replay != got {
		t.Fatal("ensure replay", replay, replayErr)
	}
	if _, err = cfg.Keys.LocalKey(context.Background(), publisher); err != nil {
		t.Fatal("generated key unavailable", err)
	}
	// Simulate a crash after the staged key and pending receipt were synced but
	// before the missing configured key path was replaced.
	crashOperation := "ca110000-0000-4000-8000-000000000004"
	crashTarget := "ca110000-0000-4000-8000-000000000005"
	crashPath := filepath.Join(keyDir, "crash-replay.pem")
	staged, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	stagedFingerprint, _ := federation.KeyFingerprint(&staged.PublicKey)
	stageBody := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(staged)})
	receiptBody, _ := json.Marshal(custodyReceipt{Operation: crashOperation, Target: crashTarget, Action: "ensure", Result: stagedFingerprint})
	if err = atomicPrivateFile(filepath.Join(operationDir, crashOperation+".key.pem"), stageBody); err != nil {
		t.Fatal(err)
	}
	if err = atomicPrivateFile(filepath.Join(operationDir, crashOperation+".pending.json"), receiptBody); err != nil {
		t.Fatal(err)
	}
	custody := cfg.Keys.(configuredFederationCustodyKeys).custody
	if replay, replayErr := custody.Execute(context.Background(), crashOperation, crashTarget, crashPath, "ensure", "", map[string]string{crashTarget: crashPath}); replayErr != nil || replay != stagedFingerprint {
		t.Fatal("pending missing-key operation did not recover", replay, replayErr)
	}
	if current, currentErr := keyFingerprintAt(crashPath); currentErr != nil || current != stagedFingerprint {
		t.Fatal("crash replay did not install staged key", current, currentErr)
	}
	if _, secondErr := admin.ExecuteLocalKeyCommand(context.Background(), "ca110000-0000-4000-8000-000000000009", secondPublisher, "ensure", ""); secondErr != nil {
		t.Fatal("second isolated actor key", secondErr)
	}
	secondSigner, secondErr := cfg.Keys.LocalKey(context.Background(), secondPublisher)
	if secondErr != nil {
		t.Fatal("second isolated actor signing key", secondErr)
	}
	request, _ := http.NewRequest(http.MethodPost, "https://remote.example.test/inbox", strings.NewReader("{}"))
	if signErr := federation.Sign(request, []byte("{}"), "https://local.example.test/actors/second#main-key", secondSigner, time.Now()); signErr != nil || request.Header.Get("Signature") == "" {
		t.Fatal("custody key did not execute the production signature path", signErr)
	}
	copyBody, _ := os.ReadFile(missing)
	if err = os.WriteFile(secondMissing, copyBody, 0600); err != nil {
		t.Fatal(err)
	}
	if _, duplicateErr := cfg.Keys.LocalKey(context.Background(), secondPublisher); !errors.Is(duplicateErr, federation.ErrForbidden) {
		t.Fatal("ordinary signing accepted a copied cross-actor key", duplicateErr)
	}
	if _, duplicateErr := admin.ExecuteLocalKeyCommand(context.Background(), "ca110000-0000-4000-8000-000000000007", secondPublisher, "ensure", ""); !errors.Is(duplicateErr, federation.ErrForbidden) {
		t.Fatal("copied private key accepted for a second actor", duplicateErr)
	}
	if err = os.Remove(secondMissing); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(missing, secondMissing); err != nil {
		t.Fatal(err)
	}
	if _, linkedErr := cfg.Keys.LocalKey(context.Background(), secondPublisher); !errors.Is(linkedErr, federation.ErrForbidden) {
		t.Fatal("ordinary signing accepted a hard-linked cross-actor key", linkedErr)
	}
	if _, linkedErr := admin.ExecuteLocalKeyCommand(context.Background(), "ca110000-0000-4000-8000-000000000008", secondPublisher, "ensure", ""); !errors.Is(linkedErr, federation.ErrForbidden) {
		t.Fatal("post-load hard link accepted for a second actor", linkedErr)
	}

	alias := filepath.Join(keyDir, "alias.pem")
	if err = os.WriteFile(alias, copyBody, 0600); err != nil {
		t.Fatal(err)
	}
	body["local_keys"] = []map[string]string{{"publisher_id": publisher, "private_key_file": missing}, {"publisher_id": "ca110000-0000-4000-8000-000000000003", "private_key_file": alias}}
	raw, _ = json.Marshal(body)
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadFederationConfig(configPath, false); err == nil {
		t.Fatal("distinct files containing the same private key accepted")
	}
	if err = os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(missing, alias); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(body)
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadFederationConfig(configPath, false); err == nil {
		t.Fatal("hard-linked private keys accepted")
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

func TestLivestreamChatConfigurationRequiresExplicitStreamProvider(t *testing.T) {
	for _, args := range [][]string{{"--enable-livestream-chat"}, {"--livestream-chat-attempts=2"}} {
		var c application.Config
		if e := parseExternal(t, args...).apply(&c); e == nil {
			t.Fatal("unconfigured chat accepted")
		}
	}
	t.Setenv("SYNTHETIC_STREAM_CHAT_KEY", "synthetic-only-key")
	t.Setenv("SYNTHETIC_STREAM_CHAT_SECRET", strings.Repeat("s", 64))
	args := []string{"--enable-livestreams", "--enable-livestream-chat", "--livekit-endpoint=wss://nonexistent.invalid", "--livekit-api-key-env=SYNTHETIC_STREAM_CHAT_KEY", "--livekit-api-secret-env=SYNTHETIC_STREAM_CHAT_SECRET", "--livekit-cloud-revocation"}
	var c application.Config
	if e := parseExternal(t, args...).apply(&c); e != nil || c.Livestreams == nil || c.Livestreams.Chat == nil || c.Calls != nil {
		t.Fatal("explicit chat config", e)
	}
	var bad application.Config
	if e := parseExternal(t, append(args, "--livestream-chat-send-timeout=6s")...).apply(&bad); e == nil {
		t.Fatal("unbounded provider send")
	}
}
