// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"strings"
	"testing"
)

func TestFederationServerCLIAndDiscoveryAreOfflineOptIn(t *testing.T) {
	public := &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(31337)), E: 65537}
	der, e := x509.MarshalPKIXPublicKey(public)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "synthetic-public.pem")
	if e = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	input := map[string]any{"origin": "https://local.example.test", "allowed_origins": []string{"https://remote.example.test"}, "max_attempts": 3, "delivery_timeout_seconds": 1, "server_actor": map[string]string{"username": "synthetic-server", "name": "Synthetic server", "summary": "Synthetic fixture", "public_key_file": keyFile}, "remote_keys": []map[string]string{{"actor": "https://remote.example.test/actor", "inbox": "https://remote.example.test/inbox", "key_id": "https://remote.example.test/actor#main-key", "incarnation": "ca110000-0000-4000-8000-000000000099", "public_key_file": keyFile}}, "discovery_targets": []map[string]string{{"resource": "acct:alice@remote.example.test", "actor": "https://remote.example.test/actor"}}}
	path := filepath.Join(dir, "federation.json")
	write := func() {
		t.Helper()
		b, _ := json.Marshal(input)
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	cfg, e := loadFederationConfig(path, false)
	if e != nil || cfg.Discovery.Server == nil || cfg.Fetcher != nil || len(cfg.Discovery.Targets) != 1 {
		t.Fatal(cfg, e)
	}
	cfg, e = loadFederationConfigWithDiscovery(path, false, true)
	if e != nil || cfg.Fetcher == nil || cfg.Transport != nil {
		t.Fatal(cfg, e)
	}
	var output bytes.Buffer
	if e = run([]string{"initialize-federation-server", "--federation-config", path}, &output); e != nil || !strings.Contains(output.String(), "no database connection") {
		t.Fatal(output.String(), e)
	}
	for _, args := range [][]string{{"--federation-discover"}, {"--enable-federation", "--federation-discover"}} {
		var c application.Config
		if e = parseExternal(t, args...).apply(&c); e == nil {
			t.Fatal("implicit discovery enabled", args)
		}
	}
	var c application.Config
	if e = parseExternal(t, "--enable-federation", "--federation-config", path, "--federation-discover").apply(&c); e != nil || c.Federation.Fetcher == nil {
		t.Fatal(e)
	}
	input["discovery_targets"] = []map[string]string{{"resource": "acct:alice@evil.example.test", "actor": "https://remote.example.test/actor"}}
	write()
	if _, e = loadFederationConfig(path, false); e == nil {
		t.Fatal("cross-origin handle accepted")
	}
	delete(input, "discovery_targets")
	write()
	if _, e = loadFederationConfigWithDiscovery(path, false, true); e == nil {
		t.Fatal("network enabled without bounded targets")
	}
	input["server_actor"].(map[string]string)["public_key_file"] = "relative.pem"
	write()
	if _, e = loadFederationConfig(path, false); e == nil {
		t.Fatal("relative public key path")
	}
}

func TestFederationConfigRejectsCaseShadowedOptions(t *testing.T) {
	for _, raw := range []string{`{"Origin":"https://local.example.test"}`, `{"origin":"https://local.example.test","ORIGIN":"https://other.example.test"}`, `{"server_actor":{"Username":"synthetic"}}`, `{"discovery_targets":[{"Resource":"acct:alice@remote.example.test"}]}`} {
		if canonicalFederationFields([]byte(raw)) {
			t.Fatal("case shadow accepted", raw)
		}
	}
	if !canonicalFederationFields([]byte(`{"origin":"https://local.example.test","server_actor":{"username":"synthetic"}}`)) {
		t.Fatal("canonical options rejected")
	}
}
