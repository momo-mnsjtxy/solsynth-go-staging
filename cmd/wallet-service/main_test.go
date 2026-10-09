package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurationUsesOnlyPublicTrustAndNoNetwork(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	b, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if e = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SYNTHETIC_WALLET_DSN", "postgres://synthetic@127.0.0.1:5432/disposable?sslmode=disable")
	var out bytes.Buffer
	e = run([]string{"--dsn-env", "SYNTHETIC_WALLET_DSN", "--issuer", "synthetic-test", "--audience", "wallet", "--key-id", "ephemeral", "--trusted-public-key-file", path}, &out)
	if e != nil || !strings.Contains(out.String(), "No database connection") {
		t.Fatal(e, out.String())
	}
	private := filepath.Join(t.TempDir(), "synthetic-private.pem")
	if e = os.WriteFile(private, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = publicKey(private); e == nil {
		t.Fatal("private signing key accepted")
	}
}
func TestCommandRejectsUnsafeDefaults(t *testing.T) {
	if e := run(nil, &bytes.Buffer{}); e == nil {
		t.Fatal("missing configuration accepted")
	}
	for _, addr := range []string{"0.0.0.0:80", ":80", "example.test:80", "127.0.0.1"} {
		if loopback(addr) {
			t.Fatal(addr)
		}
	}
	for _, dsn := range []string{"", "host=prod password=secret", "postgres://u@example.test/db?sslmode=disable", "postgres://u@localhost/db?sslmode=require", "postgres://u@localhost/db?servicefile=/tmp/config"} {
		if _, e := explicitDSN(dsn); e == nil {
			t.Fatal(dsn)
		}
	}
	dsn, e := explicitDSN("postgres://synthetic@localhost/db?sslmode=disable")
	if e != nil || !strings.Contains(dsn, "passfile=%2Fdev%2Fnull") {
		t.Fatal(dsn, e)
	}
}
