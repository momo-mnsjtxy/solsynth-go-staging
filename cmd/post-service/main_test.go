// SPDX-License-Identifier: AGPL-3.0-only
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

func TestConfigurationDoesNotOpenDatabaseOrSocket(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "public.pem")
	if e = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	e = run([]string{"-dsn", "postgres://synthetic@never-connect.invalid/db?sslmode=disable", "-issuer", "https://fixture.invalid", "-audience", "fixture", "-key-id", "fixture", "-trusted-public-key-file", path}, &out)
	if e != nil || !strings.Contains(out.String(), "no connection") {
		t.Fatal(out.String(), e)
	}
}
func TestMissingConfigurationAndAmbientConnectionOptionsRejected(t *testing.T) {
	if e := run(nil, &bytes.Buffer{}); e == nil {
		t.Fatal("implicit config")
	}
	for _, s := range []string{"", "host=localhost", "postgres://localhost/db", "postgres://user@host/db?service=ambient", "postgres://user@host/db?sslmode=require", "postgres://user@host/db?sslmode=disable&sslmode=verify-full"} {
		if _, e := explicitDSN(s); e == nil {
			t.Fatal("accepted", s)
		}
	}
	s, e := explicitDSN("postgres://user@host/db")
	if e != nil || !strings.Contains(s, "passfile=%2Fdev%2Fnull") || !strings.Contains(s, "sslmode=verify-full") {
		t.Fatal(s, e)
	}
}
