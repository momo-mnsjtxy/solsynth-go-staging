// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCalendarSourceOmissionAndProvenance(t *testing.T) {
	type entry struct {
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
		Reason string `json:"omission_reason"`
	}
	var manifest struct {
		Files   []entry `json:"files"`
		Omitted []entry `json:"omitted_sources"`
	}
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 12 || len(manifest.Omitted) != 1 {
		t.Fatal("unexpected provenance count")
	}
	for _, row := range manifest.Files {
		b, e := os.ReadFile(filepath.Join("../../..", row.File))
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256(b)
		if hex.EncodeToString(hash[:]) != row.SHA256 {
			t.Fatal("source hash mismatch", row.File)
		}
	}
	for _, row := range manifest.Omitted {
		if row.Reason == "" || row.SHA256 != "6eb374c45737866df19eccd84814b96b5540e3b0cb2da9a3741502779569799c" {
			t.Fatal("omission evidence missing")
		}
		if _, e := os.Stat(filepath.Join("../../..", row.File)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("omitted source is present", row.File, e)
		}
	}
}
