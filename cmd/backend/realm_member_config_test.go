// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/realmboostpg"
	"strings"
	"testing"
)

func memberConfig(t *testing.T, body string, boosts bool, args ...string) (application.Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
	s := realmMemberFlags(fs)
	p := filepath.Join(t.TempDir(), "member.json")
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	if args == nil {
		args = []string{"--enable-realm-members", "--realm-member-config=" + p}
	}
	if e := fs.Parse(args); e != nil {
		t.Fatal(e)
	}
	cfg := application.Config{}
	if boosts {
		cfg.RealmBoosts = &realmboostpg.Config{}
	}
	e := s.apply(&cfg)
	return cfg, e
}
func TestRealmMemberExplicitConfig(t *testing.T) {
	good := `{"version":1,"level_thresholds":[],"enable_daily_tenure":false}`
	cfg, e := memberConfig(t, good, true)
	if e != nil || cfg.RealmMembers == nil || cfg.RealmMembers.LevelThresholds == nil || cfg.RealmMembers.EnableDailyTenure {
		t.Fatal(cfg, e)
	}
	if _, e = memberConfig(t, good, false); e == nil {
		t.Fatal("enabled without canonical boosts")
	}
	for _, body := range []string{`null`, `{}`, good + `{}`, strings.Replace(good, `"version":1`, `"Version":1`, 1), strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(good, `[]`, `null`, 1), strings.Replace(good, `[]`, `[null]`, 1), strings.Replace(good, `[]`, `[1,10]`, 1), strings.Replace(good, `[]`, `[0,10,10]`, 1), strings.Replace(good, `false`, `null`, 1), strings.Replace(good, `false`, `0`, 1), strings.Replace(good, `"enable_daily_tenure":false`, `"daily_xp":999`, 1), strings.Repeat(" ", 65537)} {
		if c, e := memberConfig(t, body, true); e == nil || c.RealmMembers != nil {
			t.Fatal("invalid policy accepted", body)
		}
	}
	for _, args := range [][]string{{"--enable-realm-members"}, {"--realm-member-config=/tmp/unrequested"}, {"--enable-realm-members", "--realm-member-config=relative"}} {
		if _, e := memberConfig(t, good, true, args...); e == nil {
			t.Fatal(args)
		}
	}
}
func TestRealmMemberConfigRejectsLinkedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if e := os.WriteFile(path, []byte(`{"version":1,"level_thresholds":[],"enable_daily_tenure":false}`), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{link, t.TempDir()} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		s := realmMemberFlags(fs)
		if e := fs.Parse([]string{"--enable-realm-members", "--realm-member-config=" + p}); e != nil {
			t.Fatal(e)
		}
		cfg := application.Config{RealmBoosts: &realmboostpg.Config{}}
		if e := s.apply(&cfg); e == nil || cfg.RealmMembers != nil {
			t.Fatal("nonregular policy accepted")
		}
	}
}
func TestRealmMemberCLIValidatesWithoutIO(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	public := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	keyFile := filepath.Join(t.TempDir(), "synthetic-public.pem")
	if e = os.WriteFile(keyFile, public, 0600); e != nil {
		t.Fatal(e)
	}
	boost := realmBoostConfigFile(t, syntheticRealmBoostJSON)
	member := realmBoostConfigFile(t, `{"version":1,"level_thresholds":[],"enable_daily_tenure":false}`)
	t.Setenv("REALM_MEMBER_CONFIG_TEST_DSN", "postgres://synthetic@nonexistent.invalid/disposable")
	args := []string{"--dsn-env=REALM_MEMBER_CONFIG_TEST_DSN", "--issuer=https://issuer.invalid", "--audience=synthetic", "--key-id=ephemeral", "--trusted-public-key-file=" + keyFile, "--taxonomy-public-visibility=11", "--messager-none-encryption=7", "--messager-notify-all=1", "--messager-notify-mentions=2", "--messager-notify-none=3", "--enable-realm-boosts", "--realm-boost-config=" + boost, "--enable-realm-members", "--realm-member-config=" + member}
	var output bytes.Buffer
	if e = run(args, &output); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), "No database connection") || strings.Contains(output.String(), "11111111") || strings.Contains(output.String(), "synthetic@") {
		t.Fatal("offline configuration boundary", output.String())
	}
	if e = os.WriteFile(member, []byte(`{"version":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	output.Reset()
	if e = run(args, &output); e == nil || output.Len() != 0 {
		t.Fatal("main skipped member configuration validation", e)
	}
}
