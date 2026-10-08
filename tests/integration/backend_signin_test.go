// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"testing"
)

func TestPGBackendSigninHTTPProcessRestart(t *testing.T) {
	f := newSigninFixture(t)
	account, _ := f.register(t, true)
	cfg := authorityConfig(t, f.keys, f.now)
	pub := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if e := os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&cfg.SigningKey.PublicKey)}), 0600); e != nil {
		t.Fatal(e)
	}
	u := &url.URL{Scheme: "postgres", User: url.User("synthetic"), Host: "localhost:55432", Path: "/postgres"}
	u.RawQuery = url.Values{"host": {filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "socket")}, "sslmode": {"disable"}}.Encode()
	t.Setenv("SYNTHETIC_SIGNIN_PROCESS_DSN", u.String())
	t.Setenv("SYNTHETIC_SIGNIN_HASH", base64.StdEncoding.EncodeToString(cfg.RefreshHashKey))
	t.Setenv("SYNTHETIC_SIGNIN_REPLAY", base64.StdEncoding.EncodeToString(cfg.ReplaySealKey))
	t.Setenv("SYNTHETIC_SIGNIN_SEAL", base64.StdEncoding.EncodeToString(f.config.SealKey))
	args := []string{"--serve", "--dsn-env=SYNTHETIC_SIGNIN_PROCESS_DSN", "--issuer=" + cfg.Issuer, "--audience=" + cfg.Audience, "--key-id=synthetic", "--trusted-public-key-file=" + pub, "--taxonomy-public-visibility=0", "--messager-none-encryption=77", "--messager-notify-all=10", "--messager-notify-mentions=20", "--messager-notify-none=30", "--enable-signin", "--issuer-private-key-file=" + filepath.Join(f.keys, "synthetic-signing.pem"), "--issuer-refresh-hash-env=SYNTHETIC_SIGNIN_HASH", "--issuer-replay-seal-env=SYNTHETIC_SIGNIN_REPLAY", "--signin-seal-env=SYNTHETIC_SIGNIN_SEAL"}
	p := startDomainProcess(t, "BACKEND_BINARY", "Authenticated backend listening on ", args...)
	raw, _ := p.call(t, "POST", "/stargate/auth/challenge", fmt.Sprintf(`{"account":%q,"device_name":"process"}`, account.Name), 200, nil)
	var challenge signin.Challenge
	if e := json.Unmarshal(raw, &challenge); e != nil || challenge.ID == "" {
		t.Fatal("challenge response", e)
	}
	raw, _ = p.call(t, "GET", "/stargate/auth/challenge/"+challenge.ID+"/factors", "", 200, nil)
	var factors []signin.Factor
	if e := json.Unmarshal(raw, &factors); e != nil {
		t.Fatal(e)
	}
	factor := ""
	for _, v := range factors {
		if v.Type == 0 {
			factor = v.ID
		}
	}
	if factor == "" {
		t.Fatal("password factor missing")
	}
	p.call(t, "PATCH", "/stargate/auth/challenge/"+challenge.ID, fmt.Sprintf(`{"factor_id":%q,"password":%q}`, factor, syntheticPassword), 200, nil)
	body := fmt.Sprintf(`{"grant_type":"authorization_code","code":%q}`, challenge.ID)
	raw, _ = p.call(t, "POST", "/stargate/auth/token", body, 200, nil)
	var pair authority.Pair
	if e := json.Unmarshal(raw, &pair); e != nil || pair.Token == "" {
		t.Fatal("canonical exchange response", e)
	}
	headers := map[string]string{"Authorization": "Bearer " + pair.Token}
	p.call(t, "GET", "/stargate/accounts/me", "", 200, headers)
	p.call(t, "POST", "/stargate/auth/token", body, 401, nil)
	p.stop()
	p = startDomainProcess(t, "BACKEND_BINARY", "Authenticated backend listening on ", args...)
	p.call(t, "GET", "/stargate/accounts/me", "", 200, headers)
	p.call(t, "DELETE", "/stargate/sessions/current", "", 204, headers)
	p.call(t, "GET", "/stargate/accounts/me", "", 401, headers)
	p.stop()
}
