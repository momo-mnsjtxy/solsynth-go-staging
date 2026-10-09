// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/mediastore"
	"testing"
)

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
