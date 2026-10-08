// SPDX-License-Identifier: AGPL-3.0-only
package accountsettings

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

func TestAccountSettingsLicensedAuthoredProvenance(t *testing.T) {
	b, err := os.ReadFile("source/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files []struct {
			File    string `json:"file"`
			GitBlob string `json:"git_blob"`
			SHA256  string `json:"sha256"`
			Bytes   int    `json:"bytes"`
			License string `json:"license"`
		} `json:"files"`
	}
	if err = json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 9 {
		t.Fatal("incomplete authored source manifest")
	}
	for _, f := range manifest.Files {
		b, err := os.ReadFile(filepath.Join("../../..", f.File))
		if err != nil {
			t.Fatal(err)
		}
		sha := sha256.Sum256(b)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
		if len(b) != f.Bytes || hex.EncodeToString(sha[:]) != f.SHA256 || hex.EncodeToString(blob[:]) != f.GitBlob || f.License != "AGPL-3.0-only" {
			t.Errorf("provenance mismatch: %s", f.File)
		}
	}
}
