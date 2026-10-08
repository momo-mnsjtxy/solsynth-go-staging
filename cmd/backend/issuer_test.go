// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitIssuerKeyAndSecretValidation(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "synthetic-private.pem")
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if e = os.WriteFile(file, private, 0600); e != nil {
		t.Fatal(e)
	}
	parsed, e := issuerKey(file)
	if e != nil || parsed.N.Cmp(key.N) != 0 {
		t.Fatal("explicit private key", e)
	}
	for _, bad := range [][]byte{append(append([]byte{}, private...), private...), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("synthetic-invalid")}), pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}), []byte("invalid")} {
		if e = os.WriteFile(file, bad, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = issuerKey(file); e == nil {
			t.Fatal("invalid issuer material accepted")
		}
	}
	t.Setenv("SYNTHETIC_ISSUER_SECRET", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if b, e := issuerSecret("SYNTHETIC_ISSUER_SECRET"); e != nil || len(b) != 32 {
		t.Fatal(e)
	}
	for _, value := range []string{"", "invalid", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 33))} {
		t.Setenv("SYNTHETIC_ISSUER_SECRET", value)
		if _, e := issuerSecret("SYNTHETIC_ISSUER_SECRET"); e == nil {
			t.Fatal("invalid issuer secret accepted")
		}
	}
}
