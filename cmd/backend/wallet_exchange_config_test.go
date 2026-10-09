// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"io"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	a "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"testing"
)

const syntheticExchangeConfig = `{"version":"synthetic-v1","treasury_account_id":"11000000-0000-0000-0000-000000000002","treasury_wallet_id":"21000000-0000-0000-0000-000000000002","rates":[{"source_currency":"points","source_amount":"3","target_currency":"golds","target_amount":"1"}]}`

func TestWalletExchangeCLIExplicitPolicyOnly(t *testing.T) {
	apply := func(args ...string) (application.Config, error) {
		f := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		s := walletExchangeFlags(f)
		c := application.Config{}
		if e := f.Parse(args); e != nil {
			return c, e
		}
		e := s.apply(&c)
		return c, e
	}
	c, e := apply()
	if e != nil || c.WalletExchange != nil {
		t.Fatal(c.WalletExchange, e)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.json")
	if e = os.WriteFile(path, []byte(syntheticExchangeConfig), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"--enable-wallet-exchange"}, {"--wallet-exchange-config=" + path}, {"--enable-wallet-exchange", "--wallet-exchange-config=relative.json"}} {
		if _, e = apply(args...); e == nil {
			t.Fatal(args)
		}
	}
	c, e = apply("--enable-wallet-exchange", "--wallet-exchange-config="+path)
	if e != nil || c.WalletExchange == nil || c.WalletExchange.Rates[0].SourceAmount != "3" {
		t.Fatal(c.WalletExchange, e)
	}
	for _, raw := range []string{strings.Replace(syntheticExchangeConfig, `"version":"synthetic-v1"`, `"version":"synthetic-v1","version":"synthetic-v2"`, 1), strings.Replace(syntheticExchangeConfig, `"source_amount":"3"`, `"source_amount":"3","source_amount":"2"`, 1), strings.Replace(syntheticExchangeConfig, `"source_amount":"3"`, `"source_amount":3`, 1), strings.Replace(syntheticExchangeConfig, `"source_amount":"3"`, `"fee":"1","source_amount":"3"`, 1), syntheticExchangeConfig + "{}"} {
		if e = os.WriteFile(path, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = apply("--enable-wallet-exchange", "--wallet-exchange-config="+path); e == nil {
			t.Fatal(raw)
		}
	}
	link := filepath.Join(dir, "symlink.json")
	if e = os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e = apply("--enable-wallet-exchange", "--wallet-exchange-config="+link); e == nil {
		t.Fatal("symlink accepted")
	}
}

func TestWalletExchangeCLIPrintsOfflinePolicyCommitment(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	keyFile := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if e = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}), 0600); e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(t.TempDir(), "synthetic-exchange.json")
	if e = os.WriteFile(config, []byte(syntheticExchangeConfig), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("WALLET_EXCHANGE_TEST_DSN", "postgres://synthetic@nonexistent.invalid/disposable")
	args := []string{"--dsn-env=WALLET_EXCHANGE_TEST_DSN", "--issuer=https://issuer.invalid", "--audience=synthetic", "--key-id=ephemeral", "--trusted-public-key-file=" + keyFile, "--taxonomy-public-visibility=11", "--messager-none-encryption=7", "--messager-notify-all=1", "--messager-notify-mentions=2", "--messager-notify-none=3", "--enable-wallet-exchange", "--wallet-exchange-config=" + config}
	var out bytes.Buffer
	if e = run(args, &out); e != nil {
		t.Fatal(e)
	}
	policy, e := a.NewExchangePolicy(a.ExchangeConfig{Version: "synthetic-v1", TreasuryAccountID: "11000000-0000-0000-0000-000000000002", TreasuryWalletID: "21000000-0000-0000-0000-000000000002", Rates: []a.ExchangeRate{{SourceCurrency: "points", SourceAmount: "3", TargetCurrency: "golds", TargetAmount: "1"}}})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), "sha256="+policy.Digest()) || !strings.Contains(out.String(), "No database connection") || strings.Contains(out.String(), "synthetic@") || strings.Contains(out.String(), "21000000") {
		t.Fatal(out.String())
	}
}
