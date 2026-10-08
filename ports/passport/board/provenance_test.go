// SPDX-License-Identifier: AGPL-3.0-only
package board

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPBoardEvidenceIntegrity(t *testing.T) {
	raw, err := os.ReadFile("source/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Sources []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
			Bytes  int    `json:"bytes"`
		}
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Sources) < 15 {
		t.Fatal("owned board evidence incomplete")
	}
	for _, entry := range manifest.Sources {
		raw, err := os.ReadFile(filepath.Join("source", entry.File))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if len(raw) != entry.Bytes || hex.EncodeToString(hash[:]) != entry.SHA256 {
			t.Fatalf("board evidence mismatch: %s", entry.File)
		}
	}
}
