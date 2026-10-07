// SPDX-License-Identifier: AGPL-3.0-only
package watchstore

import (
	"context"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/ports/sphere"
	"testing"
)

func TestDurableAccountIsolation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "preferences.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := sphere.DefaultWatchPreference("a", sphere.WatchBookmark)
	a.NotifyEdits = false
	b := sphere.DefaultWatchPreference("b", sphere.WatchReply)
	for _, v := range []sphere.WatchPreference{a, b} {
		if err = s.SavePreferences(ctx, v.AccountID, []sphere.WatchPreference{v}); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.LoadPreferences(ctx, []string{"a", "a"})
	if err != nil || len(rows) != 1 || rows[0].AccountID != "a" || rows[0].NotifyEdits {
		t.Fatalf("%+v %v", rows, err)
	}
	if err = reopened.SavePreferences(ctx, "a", []sphere.WatchPreference{b}); err == nil {
		t.Fatal("cross-account write accepted")
	}
	rows, err = reopened.LoadPreferences(ctx, []string{"b"})
	if err != nil || len(rows) != 1 || rows[0].AccountID != "b" {
		t.Fatalf("%+v %v", rows, err)
	}
}
func TestRejectInvalidAndCancelledWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "store.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	v := sphere.DefaultWatchPreference("a", sphere.WatchReply)
	if err = s.SavePreferences(context.Background(), "a", []sphere.WatchPreference{v, v}); err == nil {
		t.Fatal("duplicate accepted")
	}
	v.Source = "unknown"
	if err = s.SavePreferences(context.Background(), "a", []sphere.WatchPreference{v}); err == nil {
		t.Fatal("invalid source accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.SavePreferences(ctx, "a", nil); err == nil {
		t.Fatal("cancelled write accepted")
	}
	if _, err = s.LoadPreferences(ctx, nil); err == nil {
		t.Fatal("cancelled read accepted")
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("failed writes created store")
	}
}
func TestCorruptionFailsClosed(t *testing.T) {
	for _, data := range []string{"{", `{"version":2,"accounts":{}}`, `{"version":1,"accounts":null}`, `{"version":1,"accounts":{"a":[{"source":"reply"},{"source":"reply"}]}}`, `{"version":1,"accounts":{"a":[{"source":"other"}]}}`} {
		t.Run(data, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "store.json")
			if err := os.WriteFile(p, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(p); err == nil {
				t.Fatal("corruption accepted")
			}
			b, _ := os.ReadFile(p)
			if string(b) != data {
				t.Fatal("corrupt file overwritten")
			}
		})
	}
}
