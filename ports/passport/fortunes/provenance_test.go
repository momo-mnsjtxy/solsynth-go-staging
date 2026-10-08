// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFortuneProvenanceOmitsOriginalCorpus(t *testing.T) {
	var m struct {
		Files []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		}
		Omitted []any `json:"omitted_full_sources"`
	}
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	if len(m.Files) != 8 || len(m.Omitted) != 2 {
		t.Fatal("incomplete provenance")
	}
	for _, row := range m.Files {
		b, e := os.ReadFile(filepath.Join("../../..", row.File))
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256(b)
		if hex.EncodeToString(hash[:]) != row.SHA256 {
			t.Fatal("source hash mismatch", row.File)
		}
	}
	for _, name := range []string{"FortuneSayingController.cs", "AccountEventService.cs", "corpus.json", "fallback.json"} {
		if _, e := os.Stat(filepath.Join("source", name)); !os.IsNotExist(e) {
			t.Fatal("excluded source/content present", name, e)
		}
	}
}
