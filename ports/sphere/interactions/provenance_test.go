// SPDX-License-Identifier: AGPL-3.0-only
package interactions

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
	var m struct {
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Blob   string `json:"git_blob_sha"`
			Bytes  int    `json:"bytes"`
		}
	}
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	if len(m.Files) != 11 {
		t.Fatal("incomplete evidence", len(m.Files))
	}
	for _, f := range m.Files {
		b, e = os.ReadFile(filepath.Join("..", "..", "..", f.Path))
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256(b)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d%c", len(b), 0)), b...))
		if len(b) != f.Bytes || hex.EncodeToString(digest[:]) != f.SHA256 || hex.EncodeToString(blob[:]) != f.Blob {
			t.Fatal("source changed", f.Path)
		}
	}
}
