// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/testpasskit"
)

func TestPasskitExplicitSyntheticConfiguration(t *testing.T) {
	f := testpasskit.New(t)
	root := t.TempDir()
	write := func(name string, b []byte) {
		if e := os.WriteFile(filepath.Join(root, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(f.Key)}))
	write("leaf.pem", testpasskit.PEM(f.Certificate))
	write("intermediate.pem", testpasskit.PEM(f.Intermediate))
	write("icon.png", f.Config.Assets["icon.png"])
	d := passkitDocument{PassType: "pass.example.synthetic.member", Team: "SYNTHETIC1", Organization: "Synthetic", Description: "Synthetic membership", Site: "https://example.test", WebService: "https://example.test/api/passkit", Key: "key.pem", Certificate: "leaf.pem", Intermediate: "intermediate.pem", Assets: map[string]string{"icon.png": "icon.png", "icon@2x.png": "icon.png"}}
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	write("config.json", b)
	file := filepath.Join(root, "config.json")
	for _, tc := range []struct {
		args           []string
		valid, enabled bool
	}{{nil, true, false}, {[]string{"--apple-wallet-config=" + file}, false, false}, {[]string{"--enable-apple-wallet"}, false, false}, {[]string{"--enable-apple-wallet", "--apple-wallet-config=" + file}, true, true}} {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		o := passkitFlags(fs)
		if e = fs.Parse(tc.args); e != nil {
			t.Fatal(e)
		}
		var c application.Config
		e = o.apply(&c)
		if (e == nil) != tc.valid || e == nil && (c.AppleWallet != nil) != tc.enabled {
			t.Fatal(tc.args, e)
		}
	}
	if e = os.Chmod(filepath.Join(root, "key.pem"), 0644); e != nil {
		t.Fatal(e)
	}
	enabled := true
	o := passkitOptions{&enabled, &file}
	if e = o.apply(&application.Config{}); e == nil {
		t.Fatal("non-private signing key accepted")
	}
}
