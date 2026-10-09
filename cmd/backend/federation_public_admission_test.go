// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/ports/federation"
	"strings"
	"testing"
)

func TestPublicAdmissionStandaloneStrictOptIn(t *testing.T) {
	raw := `{"origin":"https://local.example.test","allowed_origins":["https://public.example.test"],"max_attempts":3,"delivery_timeout_seconds":2,"public_admission":[{"origin":"https://public.example.test","incarnation":"10000000-0000-4000-8000-000000000001"}],"retired_remote_actors":["https://public.example.test/retired"],"retired_remote_key_ids":["https://public.example.test/retired#main-key"]}`
	path := filepath.Join(t.TempDir(), "admission.json")
	if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	cfg, e := loadFederationConfig(path, false)
	if e != nil || len(cfg.PublicAdmission) != 1 || cfg.AdmissionFetcher != nil || cfg.Transport != nil {
		t.Fatal(cfg, e)
	}
	cfg, e = loadFederationConfigWithAdmission(path, false, false, true)
	if e != nil || cfg.AdmissionFetcher == nil || cfg.Fetcher != nil || cfg.Transport != nil {
		t.Fatal(cfg, e)
	}
	keys := cfg.Keys.(configuredFederationKeys)
	if yes, e := keys.FirstContact(context.Background(), federation.RemoteKey{Actor: "https://public.example.test/retired", KeyID: "https://public.example.test/retired#main-key"}); yes || e != nil {
		t.Fatal("retired pin permitted", yes, e)
	}
	var output bytes.Buffer
	if e = run([]string{"initialize-federation-public-admission", "--federation-config", path}, &output); e != nil || !strings.Contains(output.String(), "no database connection") {
		t.Fatal(output.String(), e)
	}
	for _, args := range [][]string{{"--federation-admit"}, {"--enable-federation", "--federation-admit"}} {
		var c application.Config
		if e = parseExternal(t, args...).apply(&c); e == nil {
			t.Fatal("implicit config", args)
		}
	}
	var c application.Config
	if e = parseExternal(t, "--enable-federation", "--federation-config", path, "--federation-admit").apply(&c); e != nil || c.Federation.AdmissionFetcher == nil {
		t.Fatal(e)
	}
	for _, invalid := range []string{strings.Replace(raw, `"public_admission"`, `"Public_Admission"`, 1), strings.Replace(raw, `"incarnation"`, `"Incarnation"`, 1), strings.Replace(raw, "10000000-0000-4000-8000-000000000001", "not-an-incarnation", 1), strings.Replace(raw, `"origin":"https://public.example.test"`, `"origin":"https://public.example.test:443"`, 1)} {
		if e = os.WriteFile(path, []byte(invalid), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = loadFederationConfig(path, false); e == nil {
			t.Fatal("noncanonical policy accepted", invalid)
		}
	}
}

func TestPublicAdmissionRetiredKeyMaterialCannotReappearUnderNewIdentity(t *testing.T) {
	public := &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(31337)), E: 65537}
	fingerprint, e := federation.KeyFingerprint(public)
	if e != nil {
		t.Fatal(e)
	}
	input := map[string]any{
		"origin": "https://local.example.test", "allowed_origins": []string{"https://public.example.test"},
		"max_attempts": 3, "delivery_timeout_seconds": 2,
		"public_admission":                []federation.PublicAdmissionPolicy{{Origin: "https://public.example.test", Incarnation: "10000000-0000-4000-8000-000000000001"}},
		"retired_remote_key_fingerprints": []string{fingerprint},
	}
	path := filepath.Join(t.TempDir(), "admission.json")
	write := func() {
		t.Helper()
		body, e := json.Marshal(input)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, body, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	cfg, e := loadFederationConfig(path, false)
	if e != nil {
		t.Fatal(e)
	}
	keys := cfg.Keys.(configuredFederationKeys)
	candidate := federation.RemoteKey{Actor: "https://public.example.test/new-identity", KeyID: "https://public.example.test/new-identity#main-key"}
	if allowed, e := keys.FirstContact(context.Background(), candidate); e != nil || !allowed {
		t.Fatal("unknown preliminary identity", allowed, e)
	}
	candidate.PublicKey = public
	if allowed, e := keys.FirstContact(context.Background(), candidate); e != nil || allowed {
		t.Fatal("retired material reappeared under an unknown actor/key ID", allowed, e)
	}
	candidate.PublicKey = &rsa.PublicKey{N: new(big.Int).Add(public.N, big.NewInt(2)), E: public.E}
	if allowed, e := keys.FirstContact(context.Background(), candidate); e != nil || !allowed {
		t.Fatal("unreserved material rejected", allowed, e)
	}
	for _, values := range [][]string{{"bad"}, {strings.ToUpper(fingerprint)}, {fingerprint, fingerprint}, {strings.Repeat("g", 64)}, make([]string, 1001)} {
		input["retired_remote_key_fingerprints"] = values
		write()
		if _, e = loadFederationConfig(path, false); e == nil {
			t.Fatal("invalid retirement history accepted", len(values))
		}
	}
	delete(input, "retired_remote_key_fingerprints")
	input["Retired_Remote_Key_Fingerprints"] = []string{fingerprint}
	write()
	if _, e = loadFederationConfig(path, false); e == nil {
		t.Fatal("case-shadowed retirement history accepted")
	}
	delete(input, "Retired_Remote_Key_Fingerprints")
	input["retired_remote_key_fingerprints"] = []string{fingerprint}
	pem, e := federation.PublicPEM(public)
	if e != nil {
		t.Fatal(e)
	}
	keyPath := filepath.Join(filepath.Dir(path), "synthetic-public.pem")
	if e = os.WriteFile(keyPath, []byte(pem), 0600); e != nil {
		t.Fatal(e)
	}
	input["remote_keys"] = []map[string]string{{"actor": candidate.Actor, "inbox": "https://public.example.test/inbox", "key_id": candidate.KeyID, "public_key_file": keyPath}}
	write()
	if _, e = loadFederationConfig(path, false); e == nil {
		t.Fatal("retired material simultaneously configured as trusted")
	}
}
