// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoImplicitServing(t *testing.T) {
	c, e := parse(nil)
	if e != nil || c.serve {
		t.Fatal(c, e)
	}
	if e = run(nil); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"--serve"}, {"--listen", "0.0.0.0:8000"}, {"--listen", "localhost:8000"}, {"--listen", "127.0.0.1:0"}, {"--listen", "127.0.0.1:65536"}, {"--bogus"}, {"positional"}} {
		if _, e = parse(args); e == nil {
			t.Fatal(args)
		}
	}
}

func TestExplicitPublicVerifierConfiguration(t *testing.T) {
	c, e := parse([]string{"--serve", "--listen", "[::1]:8088", "--postgres-dsn-env", "SYNTHETIC_TEST_DSN", "--public-key-file", "public.pem", "--key-id", "local-fixture", "--issuer", "https://authority.example.test", "--audience", "accounts-test"})
	if e != nil || !c.serve || c.keyID != "local-fixture" || c.dsnEnv != "SYNTHETIC_TEST_DSN" {
		t.Fatal(c, e)
	}
	// This is deliberately only a public-number fixture. No signing credential
	// is generated, read or persisted by this command or this test.
	k := &rsa.PublicKey{N: new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(159)), E: 65537}
	pkix, e := x509.MarshalPKIXPublicKey(k)
	if e != nil {
		t.Fatal(e)
	}
	for _, block := range []*pem.Block{{Type: "PUBLIC KEY", Bytes: pkix}, {Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(k)}} {
		p := filepath.Join(t.TempDir(), "synthetic-public.pem")
		if e = os.WriteFile(p, pem.EncodeToMemory(block), 0600); e != nil {
			t.Fatal(e)
		}
		got, e := publicKey(p)
		if e != nil || got.E != k.E || got.N.Cmp(k.N) != 0 {
			t.Fatal("public key parsing failed", e)
		}
	}
}
func TestRejectUnboundedOrPrivateKeyInputs(t *testing.T) {
	for _, content := range []string{strings.Repeat("x", 32769), "-----BEGIN PRIVATE KEY-----\nYQ==\n-----END PRIVATE KEY-----\n", "not pem"} {
		p := filepath.Join(t.TempDir(), "synthetic.pem")
		if e := os.WriteFile(p, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := publicKey(p); e == nil {
			t.Fatal("bad public key accepted")
		}
	}
}
