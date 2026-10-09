// SPDX-License-Identifier: AGPL-3.0-only
package bots

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

func TestMiniappUploadRetainedEvidenceIntegrity(t *testing.T) {
	raw, e := os.ReadFile("miniapp-upload-source-evidence.json")
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != "c6edced02f2222dbda68eecac09ee328e7748a3612150b235d17452c5ebdfcf8" {
		t.Fatal("miniapp upload evidence manifest changed")
	}
	var manifest struct {
		Files []struct {
			LocalFile string `json:"local_file"`
			SHA256    string `json:"sha256"`
			Blob      string `json:"blob"`
			Revision  string `json:"revision"`
		}
	}
	if e = json.Unmarshal(raw, &manifest); e != nil {
		t.Fatal(e)
	}
	if len(manifest.Files) != 6 {
		t.Fatal("incomplete reviewed source set")
	}
	for _, file := range manifest.Files {
		if file.Revision != "4ee2d0cebb9da8284a92483fee6dce795bf6947c" && file.Revision != "ffd0838e5f04cb40bea311a39118ebeedad210c1" {
			t.Fatal("unpinned source")
		}
		content, e := os.ReadFile(filepath.Join("../../..", file.LocalFile))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(content)
		git := sha1.New()
		fmt.Fprintf(git, "blob %d%c", len(content), 0)
		git.Write(content)
		if hex.EncodeToString(sum[:]) != file.SHA256 || hex.EncodeToString(git.Sum(nil)) != file.Blob {
			t.Fatal("retained source changed", file.LocalFile)
		}
	}
}
