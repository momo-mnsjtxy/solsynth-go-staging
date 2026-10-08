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

func TestListenIsNumericLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.0.0.2:8090", "[::1]:65535"} {
		if !loopback(address) {
			t.Fatal(address)
		}
	}
	for _, address := range []string{"localhost:8090", "0.0.0.0:8090", "[::]:8090", "127.0.0.1:http", "127.0.0.1:65536", "127.0.0.1:-1", "127.0.0.1"} {
		if loopback(address) {
			t.Fatal(address)
		}
	}
}

func TestExplicitDSN(t *testing.T) {
	for _, raw := range []string{"postgres://synthetic@localhost/disposable?sslmode=disable", "postgresql://synthetic:ephemeral@127.0.0.1:5432/disposable?sslmode=disable", "postgres://synthetic@localhost/disposable?host=%2Ftmp%2Fsocket&sslmode=disable", "postgres://synthetic@db.example.invalid/disposable"} {
		got, err := explicitDSN(raw)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if q.Get("passfile") != os.DevNull || q.Get("servicefile") != os.DevNull || q.Get("sslkey") != "" || q.Get("sslcert") != "" || q.Get("connect_timeout") != "5" {
			t.Fatal("implicit file or timeout configuration")
		}
		if strings.Contains(raw, "example.invalid") && q.Get("sslmode") != "verify-full" {
			t.Fatal("TLS identity verification absent")
		}
	}
	for _, raw := range []string{"", "host=localhost user=synthetic dbname=disposable", "postgres://localhost/disposable", "postgres://synthetic@localhost/", "postgres://synthetic@localhost/disposable#fragment", "postgres://synthetic@localhost,remote/disposable", "postgres://synthetic@localhost:0/disposable", "postgres://synthetic@localhost:65536/disposable", "postgres://synthetic@remote.invalid/disposable?sslmode=disable", "postgres://synthetic@localhost/disposable?sslmode=require", "postgres://synthetic@localhost/disposable?host=remote.invalid", "postgres://synthetic@localhost/disposable?service=other", "postgres://synthetic@localhost/disposable?sslkey=/tmp/key", "postgres://synthetic@localhost/disposable?sslmode=disable&sslmode=disable", "postgres://synthetic@localhost/disposable?connect_timeout=0", "postgres://synthetic@localhost/disposable?connect_timeout=61", "postgres://synthetic@localhost/disposable?sslmode=disable;service=other"} {
		if _, err := explicitDSN(raw); err == nil {
			t.Fatal("invalid database configuration accepted")
		}
	}
}

func TestPublicKeyFileAndOfflineConfiguration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	path := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if err = os.WriteFile(path, public, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := publicKey(path)
	if err != nil || got.N.Cmp(key.N) != 0 {
		t.Fatal("public key rejected", err)
	}
	for _, body := range [][]byte{pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), append(append([]byte{}, public...), public...), bytes.Repeat([]byte("x"), 32769), []byte("not a PEM")} {
		bad := filepath.Join(t.TempDir(), "invalid.pem")
		if err = os.WriteFile(bad, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = publicKey(bad); err == nil {
			t.Fatal("invalid public trust file accepted")
		}
	}
	// No --serve: this deliberately nonexistent host must not be resolved or
	// connected. The configuration path cannot enroll an account or open a socket.
	t.Setenv("BACKEND_CONFIG_TEST_DSN", "postgres://synthetic@nonexistent.invalid/disposable")
	args := []string{"--dsn-env=BACKEND_CONFIG_TEST_DSN", "--issuer=https://issuer.invalid", "--audience=synthetic", "--key-id=ephemeral", "--trusted-public-key-file=" + path, "--taxonomy-public-visibility=11", "--messager-none-encryption=7", "--messager-notify-all=1", "--messager-notify-mentions=2", "--messager-notify-none=3"}
	var output bytes.Buffer
	if err = run(args, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No database connection") {
		t.Fatal(output.String())
	}
	for _, override := range []string{"--listen=0.0.0.0:8090", "--listen=127.0.0.1:http", "--max-access-lifetime=2h", "--messager-notify-none=2", "--taxonomy-public-visibility=2147483648", "--settlement-interval=0s", "--pin-pepper-id=missing", "--issuer="} {
		if err = run(append(append([]string{}, args...), override), &output); err == nil {
			t.Fatal(override)
		}
	}
	t.Setenv("PGSERVICE", "unrelated")
	if err = run(args, &output); err == nil || !strings.Contains(err.Error(), "ambient PostgreSQL") {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "synthetic@") {
		t.Fatal("database URL leaked")
	}
}
