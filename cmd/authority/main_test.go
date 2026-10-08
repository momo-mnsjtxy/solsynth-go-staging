// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorityCommandRequiresExplicitConfiguration(t *testing.T) {
	var out bytes.Buffer
	if e := run(nil, &out); e == nil {
		t.Fatal("unconfigured command accepted")
	}
}
func TestAuthorityCommandValidatesEphemeralKeysWithoutDBOrSocket(t *testing.T) {
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	files := map[string][]byte{"key.pem": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), "public.pem": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), "hash.bin": bytes.Repeat([]byte{1}, 32), "seal.bin": bytes.Repeat([]byte{2}, 32)}
	for name, b := range files {
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	var out bytes.Buffer
	args := []string{"-dsn", "postgresql://synthetic@not-resolvable.invalid/synthetic", "-issuer", "https://synthetic.invalid", "-audience", "synthetic", "-key-id", "synthetic", "-signing-key-file", filepath.Join(dir, "key.pem"), "-trusted-public-key-file", filepath.Join(dir, "public.pem"), "-refresh-hash-key-file", filepath.Join(dir, "hash.bin"), "-replay-seal-key-file", filepath.Join(dir, "seal.bin")}
	if e = run(args, &out); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "no database connection, schema writes or listener") {
		t.Fatal(out.String())
	}
}
func TestAuthorityCommandDisallowsAmbientCredentialFiles(t *testing.T) {
	s, e := explicitDSN("postgresql://synthetic@db.invalid/synthetic")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(s)
	q := u.Query()
	if q.Get("passfile") != os.DevNull || q.Get("servicefile") != os.DevNull || q.Get("password") != "" || q.Get("sslkey") != "" || q.Get("sslcert") != "" || q.Get("sslrootcert") != "" || q.Get("sslmode") != "verify-full" {
		t.Fatal("ambient credential configuration")
	}
	for _, raw := range []string{"postgresql://db.invalid/synthetic", "postgresql://synthetic@db.invalid/synthetic?sslkey=/tmp/any", "postgresql://synthetic@db.invalid/synthetic?sslmode=require", "user=synthetic dbname=synthetic"} {
		if _, e = explicitDSN(raw); e == nil {
			t.Fatal("accepted implicit or unsafe DSN")
		}
	}
}
func TestAuthorityCommandRejectsCertificateAsTrustKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic.pem")
	if e := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("synthetic-not-a-certificate")}), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := loadPublicKey(path); e == nil {
		t.Fatal("certificate accepted as signing trust key")
	}
}
