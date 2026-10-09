// SPDX-License-Identifier: AGPL-3.0-only
package accounts

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

func TestPublishingControlLicensedEvidence(t *testing.T) {
	root := "../../.."
	b, e := os.ReadFile(filepath.Join(root, "docs/publishing-controls-evidence.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Files []struct {
			File, Repository, Revision, License string
			Bytes                               int
			SHA256                              string `json:"sha256"`
			Blob                                string `json:"git_blob"`
		} `json:"files"`
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 7 {
		t.Fatal("incomplete lawful evidence")
	}
	for _, f := range manifest.Files {
		if f.License != "AGPL-3.0-only" || f.Repository != "Solsynth/DysonNetwork" && f.Repository != "Solsynth/Solian" {
			t.Fatal("unlicensed source", f.File)
		}
		b, e := os.ReadFile(filepath.Join(root, f.File))
		if e != nil {
			t.Fatal(e)
		}
		sha := sha256.Sum256(b)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
		if len(b) != f.Bytes || hex.EncodeToString(sha[:]) != f.SHA256 || hex.EncodeToString(blob[:]) != f.Blob {
			t.Fatal("source bytes changed", f.File)
		}
	}
}
