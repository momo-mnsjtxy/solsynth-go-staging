// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedTaxonomySourceIntegrity(t *testing.T) {
	raw, err := os.ReadFile("source/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var files []struct {
		File   string `json:"file"`
		Blob   string `json:"blob"`
		SHA256 string `json:"sha256"`
	}
	if err = json.Unmarshal(raw, &files); err != nil || len(files) != 4 {
		t.Fatal("incomplete source evidence manifest", err)
	}
	seen := map[string]bool{}
	for _, entry := range files {
		prefix := "ports/sphere/taxonomy/source/"
		if !strings.HasPrefix(entry.File, prefix) {
			t.Fatal("source outside evidence directory")
		}
		relative := strings.TrimPrefix(entry.File, prefix)
		if relative != filepath.Base(relative) || seen[relative] {
			t.Fatal("invalid or duplicate source path")
		}
		seen[relative] = true
		data, err := os.ReadFile(filepath.Join("source", relative))
		if err != nil {
			t.Fatal(err)
		}
		sha := sha256.Sum256(data)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		if hex.EncodeToString(sha[:]) != entry.SHA256 || hex.EncodeToString(blob[:]) != entry.Blob {
			t.Fatal("pinned source evidence drift", relative)
		}
	}
}
