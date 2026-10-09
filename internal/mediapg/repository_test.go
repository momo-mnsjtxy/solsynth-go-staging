// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"testing"
)

func TestUploadValidationDoesNotUseFilenameAsPath(t *testing.T) {
	good := media.Upload{Name: "sample.txt", MIMEType: "text/plain", SHA256: strings.Repeat("a", 64), IdempotencyKey: "request-1", Size: 5}
	if e := validUpload(good, 10); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"", "..", "../sample.txt", "/absolute", "folder\\name", "bad\nheader"} {
		u := good
		u.Name = name
		if e := validUpload(u, 10); e == nil {
			t.Fatal("invalid name", name)
		}
	}
	for _, mime := range []string{"", "text/plain\r\nExtra: injected", "text/html; charset=utf-8", "TEXT/PLAIN"} {
		u := good
		u.MIMEType = mime
		if e := validUpload(u, 10); e == nil {
			t.Fatal("invalid type", mime)
		}
	}
	for _, key := range []string{"", "with space", "with\nnewline", strings.Repeat("a", 129)} {
		u := good
		u.IdempotencyKey = key
		if e := validUpload(u, 10); e == nil {
			t.Fatal("invalid key")
		}
	}
	for _, size := range []int64{-1, 11} {
		u := good
		u.Size = size
		if e := validUpload(u, 10); e == nil {
			t.Fatal("invalid bound")
		}
	}
	for _, sha := range []string{"", strings.Repeat("z", 64), strings.Repeat("A", 64)} {
		u := good
		u.SHA256 = sha
		if e := validUpload(u, 10); e == nil {
			t.Fatal("invalid digest")
		}
	}
}
func TestObjectIdentifiersAreCanonical(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, e := uuid()
		if e != nil || !authority.ValidID(id) || id != strings.ToLower(id) || seen[id] {
			t.Fatal(id, e)
		}
		seen[id] = true
	}
}
