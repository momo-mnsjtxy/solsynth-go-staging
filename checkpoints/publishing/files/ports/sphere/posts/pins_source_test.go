// SPDX-License-Identifier: AGPL-3.0-only
package posts

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

func TestPostPinExactLicensedEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	b, e := os.ReadFile(filepath.Join(root, "docs/post-pin-source-evidence.json"))
	if e != nil {
		t.Fatal(e)
	}
	var manifest struct {
		Files []struct {
			File, Repository, Revision, URL, License string
			GitBlob                                  string `json:"git_blob"`
			SHA256                                   string `json:"sha256"`
			Bytes                                    int
		}
	}
	if e = json.Unmarshal(b, &manifest); e != nil || len(manifest.Files) != 8 {
		t.Fatal(e, len(manifest.Files))
	}
	for _, f := range manifest.Files {
		raw, e := os.ReadFile(filepath.Join(root, f.File))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(raw)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(raw))), raw...))
		if len(raw) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 || hex.EncodeToString(blob[:]) != f.GitBlob || f.License != "AGPL-3.0-only" {
			t.Fatal("evidence mismatch", f.File)
		}
	}
	caller, e := os.ReadFile("pin-evidence/Solian__post_pin_sheet.dart")
	if e != nil {
		t.Fatal(e)
	}
	active := []string{}
	for _, line := range strings.Split(string(caller), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			active = append(active, line)
		}
	}
	s := strings.Join(active, "\n")
	for _, v := range []string{"value: 0", "value: 1", "data: {'mode': mode.value}", "'/sphere/posts/${post.id}/pin'"} {
		if !strings.Contains(s, v) {
			t.Fatal("active caller contract lost", v)
		}
	}
	if strings.Contains(s, "value: 2") || !strings.Contains(string(caller), "//     value: 2") {
		t.Fatal("commented reply mode must remain outside active contract")
	}
}
