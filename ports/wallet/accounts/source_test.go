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

func TestRetainedOwnedWalletSourceHashes(t *testing.T) {
	manifest, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(manifest)
	if hex.EncodeToString(digest[:]) != "08e39f4fa3cf7983e3f08213264a1852b2105e87d7ab4b08c5aa70fc2eeff1e7" {
		t.Fatal("manifest changed; independently review and pin updated provenance")
	}
	var entries []struct {
		File, Blob, SHA256 string
		Bytes              int
	}
	if e = json.Unmarshal(manifest, &entries); e != nil {
		t.Fatal(e)
	}
	if len(entries) != 16 {
		t.Fatal("unexpected evidence set", len(entries))
	}
	for _, entry := range entries {
		if filepath.Base(entry.File) != entry.File {
			t.Fatal(entry.File)
		}
		b, e := os.ReadFile(filepath.Join("source", entry.File))
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		if len(b) != entry.Bytes || hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Fatal("source changed", entry.File)
		}
		if entry.Blob != "" {
			git := sha1.New()
			fmt.Fprintf(git, "blob %d\x00", len(b))
			git.Write(b)
			if hex.EncodeToString(git.Sum(nil)) != entry.Blob {
				t.Fatal("git blob changed", entry.File)
			}
		}
	}
}
