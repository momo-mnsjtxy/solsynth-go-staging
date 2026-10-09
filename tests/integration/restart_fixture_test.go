// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixtureRestartPhase uses the runner's single phase contract. Ordinary local
// runs still skip through database(t); configured PG runs reject missing phases.
func fixtureRestartPhase(t *testing.T) string {
	t.Helper()
	phase := os.Getenv("PG_TEST_PHASE")
	if phase != "before" && phase != "after" {
		_ = database(t)
		t.Fatal("restart fixture requires PG_TEST_PHASE=before or after")
	}
	return phase
}

func restartFixturePath(t *testing.T, name string) string {
	t.Helper()
	root := os.Getenv("SOLSYNTH_DISPOSABLE_PG")
	if root == "" || !filepath.IsAbs(root) || filepath.Base(name) != name || name == "." || name == ".." {
		t.Fatal("restart marker requires a disposable directory and plain filename")
	}
	return filepath.Join(root, name)
}

func saveRestartFixture(t *testing.T, name string, value any) {
	t.Helper()
	if fixtureRestartPhase(t) != "before" {
		t.Fatal("after phase cannot create restart markers")
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	// Exclusive creation also catches accidental double seeding in one run.
	f, err := os.OpenFile(restartFixturePath(t, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func loadRestartFixture(t *testing.T, name string, value any) {
	t.Helper()
	if fixtureRestartPhase(t) != "after" {
		t.Fatal("before phase cannot read restart markers")
	}
	b, err := os.ReadFile(restartFixturePath(t, name))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, value); err != nil {
		t.Fatal(err)
	}
}
