// SPDX-License-Identifier: AGPL-3.0-only
package blogs

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

func TestBlogLicensedAuthoredSourceIntegrity(t *testing.T) {
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != "3d1ed241a86d52f63026513e3f5b10d9ca4ffc347e20c14d69cc4278a2633bb3" {
		t.Fatal("blog source manifest changed")
	}
	var m struct {
		Files []struct {
			File, License string
			SHA256        string `json:"sha256"`
			Blob          string `json:"git_blob"`
			Bytes         int
		}
	}
	if e = json.Unmarshal(b, &m); e != nil || len(m.Files) != 13 {
		t.Fatal(e, len(m.Files))
	}
	for _, f := range m.Files {
		b, e = os.ReadFile(filepath.Join("../../..", f.File))
		if e != nil {
			t.Fatal(e)
		}
		h = sha256.Sum256(b)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
		if f.License != "AGPL-3.0-only" || hex.EncodeToString(h[:]) != f.SHA256 || hex.EncodeToString(blob[:]) != f.Blob || len(b) != f.Bytes {
			t.Fatal("source evidence mismatch", f.File)
		}
	}
}
