// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/realmboostpg"
)

const syntheticRealmBoostJSON = `{"version":1,"treasury_account_id":"11111111-1111-4111-8111-111111111111","treasury_wallet_id":"22222222-2222-4222-8222-222222222222","label_caps":[0,8,16,32],"perk_levels":{"synthetic-plan":2},"order_ttl_seconds":3600}`

func realmBoostConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-realm-boost.json")
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

func realmBoostCLIConfig(t *testing.T, args ...string) (application.Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
	s := realmBoostFlags(fs)
	if e := fs.Parse(args); e != nil {
		t.Fatal(e)
	}
	var cfg application.Config
	e := s.apply(&cfg)
	return cfg, e
}

func TestRealmBoostConfigurationExplicitAndBounded(t *testing.T) {
	path := realmBoostConfigFile(t, syntheticRealmBoostJSON)
	for _, args := range [][]string{nil, {"--enable-realm-boosts=false"}} {
		cfg, e := realmBoostCLIConfig(t, args...)
		if e != nil || cfg.RealmBoosts != nil {
			t.Fatal("implicit activation", e)
		}
	}
	for _, args := range [][]string{{"--realm-boost-config=" + path}, {"--enable-realm-boosts"}, {"--enable-realm-boosts", "--realm-boost-config=relative.json"}, {"--enable-realm-boosts", "--realm-boost-config=" + t.TempDir()}} {
		cfg, e := realmBoostCLIConfig(t, args...)
		if e == nil || cfg.RealmBoosts != nil {
			t.Fatal("partial configuration accepted", args)
		}
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := realmBoostCLIConfig(t, "--enable-realm-boosts", "--realm-boost-config="+link); e == nil {
		t.Fatal("symlink accepted")
	}
	for _, body := range []string{syntheticRealmBoostJSON, strings.Replace(syntheticRealmBoostJSON, `,"order_ttl_seconds":3600`, "", 1), strings.Replace(syntheticRealmBoostJSON, `{"synthetic-plan":2}`, `{}`, 1), strings.Replace(syntheticRealmBoostJSON, `:3600`, `:60`, 1), strings.Replace(syntheticRealmBoostJSON, `:3600`, `:86400`, 1)} {
		cfg, e := realmBoostCLIConfig(t, "--enable-realm-boosts", "--realm-boost-config="+realmBoostConfigFile(t, body))
		if e != nil || cfg.RealmBoosts == nil || realmboostpg.ValidateConfig(*cfg.RealmBoosts) != nil {
			t.Fatal("CLI/server configuration mismatch", e)
		}
		if cfg.RealmBoosts.Policy.LabelCaps != [4]int{0, 8, 16, 32} || cfg.RealmBoosts.Policy.PerkLevels == nil || cfg.RealmBoosts.OrderTTL < time.Minute || cfg.RealmBoosts.OrderTTL > 24*time.Hour {
			t.Fatal("changed explicit policy", cfg.RealmBoosts)
		}
	}
}

func TestRealmBoostConfigurationRejectsAmbiguousJSONAndPolicy(t *testing.T) {
	bad := []string{"", "null", "[]", syntheticRealmBoostJSON + "{}", strings.Repeat(" ", 65537), string([]byte{0xff})}
	for _, replacement := range [][2]string{
		{`"version":1`, `"version":2`}, {`"version":1`, `"Version":1`}, {`"version":1`, `"version":1,"VERSION":1`}, {`"version":1`, `"version":1,"version":1`},
		{`"version":1`, `"version":1,"price":0`}, {`"version":1,`, ``},
		{`11111111-1111-4111-8111-111111111111`, `invalid`},
		{`"treasury_wallet_id":"22222222-2222-4222-8222-222222222222"`, `"treasury_wallet_id":null`},
		{`[0,8,16,32]`, `[0,8,16]`}, {`[0,8,16,32]`, `[0,8,16,32,64]`}, {`[0,8,16,32]`, `[null,8,16,32]`},
		{`[0,8,16,32]`, `[1,8,16,32]`}, {`[0,8,16,32]`, `[0,-1,16,32]`}, {`[0,8,16,32]`, `[0,16,8,32]`}, {`[0,8,16,32]`, `[0,8,16,1000001]`}, {`[0,8,16,32]`, `[0,8.5,16,32]`},
		{`"perk_levels":{"synthetic-plan":2}`, `"perk_levels":null`}, {`"perk_levels":{"synthetic-plan":2},`, ``},
		{`{"synthetic-plan":2}`, `{"synthetic-plan":null}`}, {`{"synthetic-plan":2}`, `{"synthetic-plan":0}`}, {`{"synthetic-plan":2}`, `{"synthetic-plan":4}`}, {`{"synthetic-plan":2}`, `{"synthetic-plan":1.5}`},
		{`{"synthetic-plan":2}`, `{" ":1}`}, {`{"synthetic-plan":2}`, `{"synthetic-plan":1,"synthetic-plan":3}`}, {`{"synthetic-plan":2}`, `{"plan":1,"\u0070lan":3}`},
		{`{"synthetic-plan":2}`, `{"` + strings.Repeat("x", 257) + `":1}`},
		{`:3600`, `:0`}, {`:3600`, `:59`}, {`:3600`, `:86401`}, {`:3600`, `:null`}, {`:3600`, `:9223372036854775808`},
	} {
		bad = append(bad, strings.Replace(syntheticRealmBoostJSON, replacement[0], replacement[1], 1))
	}
	for i, body := range bad {
		cfg, e := realmBoostCLIConfig(t, "--enable-realm-boosts", "--realm-boost-config="+realmBoostConfigFile(t, body))
		if e == nil || cfg.RealmBoosts != nil {
			t.Fatalf("invalid input %d accepted", i)
		}
		if e.Error() != errRealmBoostConfig.Error() {
			t.Fatalf("configuration detail leaked at %d", i)
		}
	}
	perks := map[string]int{}
	for i := 0; i < 1001; i++ {
		perks[strings.Repeat("a", i/26+1)+string(rune('a'+i%26))] = 1
	}
	raw, e := json.Marshal(perks)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = realmBoostCLIConfig(t, "--enable-realm-boosts", "--realm-boost-config="+realmBoostConfigFile(t, strings.Replace(syntheticRealmBoostJSON, `{"synthetic-plan":2}`, string(raw), 1))); e == nil {
		t.Fatal("unbounded perk map accepted")
	}
}

func TestRealmBoostCLIValidatesOffline(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	keyFile := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if e = os.WriteFile(keyFile, public, 0600); e != nil {
		t.Fatal(e)
	}
	config := realmBoostConfigFile(t, syntheticRealmBoostJSON)
	t.Setenv("REALM_BOOST_CONFIG_TEST_DSN", "postgres://synthetic@nonexistent.invalid/disposable")
	args := []string{"--dsn-env=REALM_BOOST_CONFIG_TEST_DSN", "--issuer=https://issuer.invalid", "--audience=synthetic", "--key-id=ephemeral", "--trusted-public-key-file=" + keyFile, "--taxonomy-public-visibility=11", "--messager-none-encryption=7", "--messager-notify-all=1", "--messager-notify-mentions=2", "--messager-notify-none=3", "--enable-realm-boosts", "--realm-boost-config=" + config}
	var output bytes.Buffer
	if e = run(args, &output); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), "No database connection") || strings.Contains(output.String(), "11111111") || strings.Contains(output.String(), "synthetic@") {
		t.Fatal("offline validation or output boundary", output.String())
	}
	if e = os.WriteFile(config, []byte(`{"version":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	output.Reset()
	if e = run(args, &output); e == nil || output.Len() != 0 {
		t.Fatal("main skipped policy validation", e, output.String())
	}
}
