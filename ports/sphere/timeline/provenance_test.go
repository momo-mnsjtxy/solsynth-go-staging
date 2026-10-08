// SPDX-License-Identifier: AGPL-3.0-only
package timeline

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

func TestPinnedAuthoredEvidence(t *testing.T) {
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Files []struct {
			File, Blob, SHA256 string
			Bytes              int
		}
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 10 {
		t.Fatal("evidence omitted", len(manifest.Files))
	}
	for _, f := range manifest.Files {
		b, e = os.ReadFile(filepath.Join("source", f.File))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if len(b) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatal("changed source evidence", f.File)
		}
		if f.Blob != "" {
			data := append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...)
			blob := sha1.Sum(data)
			if hex.EncodeToString(blob[:]) != f.Blob {
				t.Fatal("pinned Git blob mismatch", f.File)
			}
		}
	}
}
