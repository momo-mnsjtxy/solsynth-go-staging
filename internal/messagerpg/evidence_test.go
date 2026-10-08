// SPDX-License-Identifier: AGPL-3.0-only
package messagerpg

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

func TestPinnedOwnedSourceIntegrity(t *testing.T) {
	raw, err := os.ReadFile("source-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Revision string `json:"revision"`
		Files    []struct {
			File  string `json:"file"`
			SHA   string `json:"sha256"`
			Blob  string `json:"git_blob_sha1"`
			Bytes int    `json:"bytes"`
		} `json:"files"`
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Revision != "4ee2d0cebb9da8284a92483fee6dce795bf6947c" || len(manifest.Files) != 2 {
		t.Fatal("evidence manifest changed")
	}
	for _, f := range manifest.Files {
		data, err := os.ReadFile(filepath.Clean(f.File))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		if len(data) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA || hex.EncodeToString(blob[:]) != f.Blob {
			t.Fatal("source evidence drift", f.File)
		}
	}
	raw, err = os.ReadFile("schema-evidence.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Blob string `json:"git_blob_sha1"`
		SHA  string `json:"sha256"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Blob != manifest.Files[0].Blob || schema.SHA != manifest.Files[0].SHA {
		t.Fatal("schema evidence drift")
	}
}
