// SPDX-License-Identifier: AGPL-3.0-only
// 启动边界回归：锁定首个配置错误、离线写出错误，以及失败后的私有资源回收。
// 复制运行：go test ./cmd/backend -run TestStartup -count=1
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startupArguments 使用不存在的远端主机，任何误入数据库连接的离线路径都会失败。
func startupArguments(t *testing.T) []string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trusted-public.pem")
	public := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	if err = os.WriteFile(path, public, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STARTUP_TEST_DSN", "postgres://synthetic@nonexistent.invalid/disposable")
	return []string{
		"--dsn-env=STARTUP_TEST_DSN", "--issuer=https://issuer.invalid", "--audience=synthetic",
		"--key-id=ephemeral", "--trusted-public-key-file=" + path,
		"--taxonomy-public-visibility=11", "--messager-none-encryption=7",
		"--messager-notify-all=1", "--messager-notify-mentions=2", "--messager-notify-none=3",
	}
}

func TestStartupValidationOrder(t *testing.T) {
	base := startupArguments(t)
	cases := []struct {
		name    string
		args    []string
		ambient bool
		want    string
	}{
		{name: "authority before renewal", args: []string{"--issuer=", "--bot-renewal-interval=0s"}, want: "explicit authority settings, loopback listen and valid settlement interval required"},
		{name: "renewal before ambient", args: []string{"--bot-renewal-interval=0s"}, ambient: true, want: errBotExecutionConfig.Error()},
		{name: "ambient before database", args: []string{"--dsn-env=STARTUP_EMPTY_DSN"}, ambient: true, want: "ambient PostgreSQL configuration is unsupported; use the explicit DSN environment variable"},
		{name: "database before public key", args: []string{"--dsn-env=STARTUP_EMPTY_DSN", "--trusted-public-key-file=/missing"}, want: "explicit PostgreSQL URL with user, host and database required"},
		{name: "public key before ticket", args: []string{"--trusted-public-key-file=/missing", "--exam-group-mappings=invalid"}, want: "trusted public key file unavailable"},
		{name: "ticket before verification", args: []string{"--exam-group-mappings=invalid", "--captcha-provider=invalid"}, want: "invalid exam group mapping"},
		{name: "verification before room enums", args: []string{"--captcha-provider=invalid", "--taxonomy-public-visibility=invalid"}, want: "unsupported explicit verification provider"},
		{name: "room enums before PIN", args: []string{"--taxonomy-public-visibility=invalid", "--pin-pepper-id=missing"}, want: "explicit signed-int32 taxonomy-public-visibility is required"},
		{name: "PIN before media", args: []string{"--pin-pepper-id=missing", "--media-max-bytes=0"}, want: "PIN pepper environment and ID must be supplied together"},
		{name: "media before catalogue", args: []string{"--media-max-bytes=0", "--catalog-config=/missing"}, want: "media maximum must be positive and at most one GiB"},
		{name: "catalogue before account features", args: []string{"--catalog-config=/missing", "--chat-search-perks=[]"}, want: "catalog configuration unavailable"},
		{name: "chat before OAuth", args: []string{"--chat-search-perks=[]", "--oauth-consent-url=https://consent.invalid"}, want: "chat search perks must be a JSON object"},
	}
	t.Setenv("STARTUP_EMPTY_DSN", "")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PGSERVICE", "")
			if c.ambient {
				t.Setenv("PGSERVICE", "unrelated")
			}
			var out bytes.Buffer
			err := run(append(append([]string{}, base...), c.args...), &out)
			if err == nil || err.Error() != c.want {
				t.Fatalf("first error = %v; want %s", err, c.want)
			}
			if out.Len() != 0 {
				t.Fatalf("invalid startup produced output: %s", out.String())
			}
		})
	}
}

func TestStartupFailedPolicyReleasesPrivateResources(t *testing.T) {
	args := startupArguments(t)
	mediaRoot := t.TempDir()
	if err := os.Chmod(mediaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STARTUP_TEST_PIN", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x7f}, 32)))
	args = append(args, "--pin-pepper-env=STARTUP_TEST_PIN", "--pin-pepper-id=test", "--media-root="+mediaRoot, "--chat-search-perks=[]")
	options, err := parseBackendOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	startup := backendStartup{options: options}
	defer startup.closeConfiguredResources()
	if err = startup.validateConfiguration(); err == nil || err.Error() != "chat search perks must be a JSON object" {
		t.Fatalf("policy error = %v", err)
	}
	if startup.mediaRoot == nil || startup.mediaRoot.Ready(context.Background()) != nil {
		t.Fatal("fixture did not open its private media root")
	}
	secret := startup.config.PINPeppers["test"]
	if len(secret) != 32 || secret[0] == 0 {
		t.Fatal("fixture did not retain its decoded pepper")
	}
	startup.closeConfiguredResources()
	if startup.mediaRoot.Ready(context.Background()) == nil {
		t.Fatal("private media root remained open after failed configuration")
	}
	if !bytes.Equal(secret, make([]byte, 32)) {
		t.Fatal("decoded PIN pepper was retained after failed configuration")
	}
}

type startupFailingWriter struct{ err error }

func (w startupFailingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStartupOfflineOutputErrorIsReturned(t *testing.T) {
	args := startupArguments(t)
	outputFailure := errors.New("synthetic output unavailable")
	if err := run(args, startupFailingWriter{err: outputFailure}); !errors.Is(err, outputFailure) {
		t.Fatalf("output error = %v", err)
	}
}

func TestStartupHelpPrecedesHostConfiguration(t *testing.T) {
	t.Setenv("PGSERVICE", "unrelated")
	var out bytes.Buffer
	err := run([]string{"--trusted-public-key-file=/missing", "--help"}, &out)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "Usage of backend:") {
		t.Fatalf("help error = %v; output = %s", err, out.String())
	}
}
