// SPDX-License-Identifier: AGPL-3.0-only
package notabledays

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

func TestCalendarMediaRetainedSourceEvidence(t *testing.T) {
	var manifest struct {
		Sources []struct {
			File, License string
			Bytes         int
			SHA256        string `json:"sha256"`
			Blob          string `json:"git_blob"`
		}
	}
	b, e := os.ReadFile("../../../docs/calendar-media-source-evidence.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Sources) != 10 {
		t.Fatal("source inventory changed", len(manifest.Sources))
	}
	for _, row := range manifest.Sources {
		b, e := os.ReadFile(filepath.Join("../../..", row.File))
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256(b)
		blob := sha1.New()
		fmt.Fprintf(blob, "blob %d%c", len(b), 0)
		blob.Write(b)
		if row.License != "AGPL-3.0-only" || len(b) != row.Bytes || hex.EncodeToString(h[:]) != row.SHA256 || hex.EncodeToString(blob.Sum(nil)) != row.Blob {
			t.Fatal("retained source provenance mismatch", row.File)
		}
	}
}
