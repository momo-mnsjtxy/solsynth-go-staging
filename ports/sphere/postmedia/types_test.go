// SPDX-License-Identifier: AGPL-3.0-only
package postmedia

import (
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/media"
)

func TestComposerIDsAreBoundedDistinctAndOrdered(t *testing.T) {
	a := "12345678-1234-4123-8123-123456789abc"
	b := "22345678-1234-4123-8123-123456789abc"
	got, ok := NormalizeIDs([]string{strings.ToUpper(b), a})
	if !ok || len(got) != 2 || got[0] != b || got[1] != a {
		t.Fatal(got, ok)
	}
	for _, ids := range [][]string{{a, a}, {a, strings.ToUpper(a)}, {"../file"}, {a + ".jpg"}, {"https://example.invalid/file"}, make([]string, 33)} {
		if _, ok := NormalizeIDs(ids); ok {
			t.Fatal("accepted invalid attachment list", ids)
		}
	}
	if got, ok := NormalizeIDs(nil); !ok || got == nil || len(got) != 0 {
		t.Fatal(got, ok)
	}
}
func TestProjectionPreservesPrivateMeasuredMetadata(t *testing.T) {
	f := media.File{ID: "12345678-1234-4123-8123-123456789abc", Name: "photo.jpg", MIMEType: "image/jpeg", Size: 23, SHA256: strings.Repeat("a", 64), State: "ready", CreatedAt: time.Now()}
	p := Project(f)
	for _, key := range []string{"hash", "uploaded_at", "storage_id", "storage_url"} {
		if p[key] != nil {
			t.Fatal("fabricated provider metadata", key, p[key])
		}
	}
	if p["is_public"] != false || p["has_thumbnail"] != false || p["has_compression"] != false || p["size"] != int64(23) || p["sha256"] != f.SHA256 || p["id"] != f.ID {
		t.Fatal(p)
	}
}
