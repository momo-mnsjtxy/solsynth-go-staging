// SPDX-License-Identifier: AGPL-3.0-only
package notifications

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedAuthoredNotificationEvidence(t *testing.T) {
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Files []struct {
			File  string `json:"file"`
			Bytes int    `json:"bytes"`
			SHA   string `json:"sha256"`
			Blob  string `json:"git_blob_sha"`
		}
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 8 {
		t.Fatal("unexpected evidence count")
	}
	for _, entry := range manifest.Files {
		b, e := os.ReadFile(filepath.Join("source", entry.File))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if len(b) != entry.Bytes || hex.EncodeToString(sum[:]) != entry.SHA {
			t.Fatal("changed evidence", entry.File)
		}
		if entry.Blob != "" {
			h := sha1.New()
			fmt.Fprintf(h, "blob %d%c", len(b), 0)
			h.Write(b)
			if hex.EncodeToString(h.Sum(nil)) != entry.Blob {
				t.Fatal("upstream blob mismatch", entry.File)
			}
		}
	}
}
