// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/mediastore"
	"strings"
	"testing"
)

func TestPhysicalNFCConfigurationAndReadiness(t *testing.T) {
	settings := mediaLocationFlags(flag.NewFlagSet("nfc-test", flag.ContinueOnError))
	c := application.Config{}
	*settings.nfcKeyEnv = "SYNTHETIC_WRAPPING_KEY"
	t.Setenv(*settings.nfcKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	*settings.nfcConfig = filepath.Join(t.TempDir(), "nfc.json")
	document := `{"version":1,"tags":[{"record_id":"11111111-1111-4111-8111-111111111111","uid":"04958CAA5C5E80","reader":"Synthetic explicitly configured reader","application_key_env":"SYNTHETIC_APPLICATION_KEY","key_number":0,"ndef_uri_template":"https://nfc.example/sun?record_id=11111111-1111-4111-8111-111111111111&picc=00000000000000000000000000000000&cmac=0000000000000000"}]}`
	if e := os.WriteFile(*settings.nfcConfig, []byte(document), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SYNTHETIC_APPLICATION_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 16))))
	if close, e := settings.apply(&c); e == nil {
		close()
		t.Fatal("partial physical provider configuration accepted")
	}
	// These trusted executables produce no PC/SC-ready response. Construction
	// validates configuration; actual readiness must fail, without a fake device.
	*settings.nfcHelper = "/usr/bin/true"
	*settings.nfcLimiter = "/usr/bin/prlimit"
	if _, e := os.Stat(*settings.nfcHelper); e != nil {
		t.Skip("explicit Linux executable fixture unavailable")
	}
	if _, e := os.Stat(*settings.nfcLimiter); e != nil {
		t.Skip("explicit Linux limiter fixture unavailable")
	}
	close, e := settings.apply(&c)
	if e != nil {
		t.Fatal(e)
	}
	defer close()
	if c.NFCLocker == nil {
		t.Fatal("PCSC provider not wired")
	}
	if e = c.NFCLocker.Ready(context.Background()); e == nil {
		t.Fatal("missing reader reported ready")
	}
	for _, bad := range []string{
		strings.Replace(document, "https://nfc.example", "http://nfc.example", 1),
		strings.Replace(document, "record_id=11111111-1111-4111-8111-111111111111", "record_id=22222222-2222-4222-8222-222222222222", 1),
		strings.Replace(document, "&picc=", "&extra=1&picc=", 1),
	} {
		if e = os.WriteFile(*settings.nfcConfig, []byte(bad), 0600); e != nil {
			t.Fatal(e)
		}
		if closer, e := settings.apply(&c); e == nil {
			closer()
			t.Fatal("unbound NDEF template accepted")
		}
	}
	if e = os.Chmod(*settings.nfcConfig, 0644); e != nil {
		t.Fatal(e)
	}
	if close, e := settings.apply(&c); e == nil {
		close()
		t.Fatal("public physical binding file accepted")
	}
}

func TestMediaLocationExplicitProviderConfiguration(t *testing.T) {
	s := mediaLocationFlags(flag.NewFlagSet("test", flag.ContinueOnError))
	c := application.Config{MaxMediaBytes: 1 << 20}
	*s.remoteHosts = "media.example"
	if close, e := s.apply(&c); e == nil {
		close()
		t.Fatal("remote fetching enabled without storage")
	}
	*s.remoteHosts = ""
	*s.nfcKeyEnv = "SYNTHETIC_NFC_KEY_TEST"
	t.Setenv(*s.nfcKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if close, e := s.apply(&c); e == nil {
		close()
		t.Fatal("short wrapping key accepted")
	}
	t.Setenv(*s.nfcKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := mediastore.NewFilesystem(root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	c.MediaStore = store
	*s.remoteHosts = "media.example"
	*s.geocoder = "https://geo.example/lookup"
	close, e := s.apply(&c)
	if e != nil {
		t.Fatal(e)
	}
	if c.MediaRemoteFetcher == nil || c.LocationGeocoder == nil || len(c.NFCKeyEncryptionKey) != 32 {
		t.Fatal("explicit providers were not wired")
	}
	close()
	*s.pools = filepath.Join(t.TempDir(), "pools.json")
	if e = os.WriteFile(*s.pools, []byte(`{"version":1,"pools":[{"id":"d5100000-0000-4000-8000-000000009999","root":"`+root+`"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	close, e = s.apply(&c)
	if e != nil {
		t.Fatal(e)
	}
	if len(c.MediaPools) != 1 {
		t.Fatal("pool root missing")
	}
	close()
	if e = os.WriteFile(*s.pools, []byte(`{"version":1,"version":1,"pools":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if close, e = s.apply(&c); e == nil {
		close()
		t.Fatal("ambiguous pool configuration accepted")
	}
}
