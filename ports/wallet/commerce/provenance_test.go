// SPDX-License-Identifier: AGPL-3.0-only
package commerce

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

func TestRetainedCommerceSourceBytes(t *testing.T) {
	var m struct {
		Files []struct {
			File, Blob, SHA256 string
			Bytes              int
		}
	}
	b, e := os.ReadFile("source/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &m); e != nil {
		t.Fatal(e)
	}
	for _, f := range m.Files {
		b, e := os.ReadFile(filepath.Join("../../..", f.File))
		if e != nil {
			t.Fatal(f.File, e)
		}
		sum := sha256.Sum256(b)
		if len(b) != f.Bytes || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatal("retained source drift", f.File)
		}
		if f.Blob != "" {
			blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
			if hex.EncodeToString(blob[:]) != f.Blob {
				t.Fatal("git blob mismatch", f.File)
			}
		}
	}
}
func TestAuthoredStatusValues(t *testing.T) {
	if Unpaid != 0 || Paid != 1 || Cancelled != 2 || Finished != 3 || Expired != 4 || SubscriptionActive != 1 || GiftCreated != 0 || GiftSent != 1 || GiftRedeemed != 2 || GiftCancelled != 3 || GiftExpired != 4 {
		t.Fatal("authored caller status values changed")
	}
}
