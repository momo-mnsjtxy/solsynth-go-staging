// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"errors"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/media"
)

func TestUsageChangesIdempotencyAndCannotSelectAProvider(t *testing.T) {
	req := media.CreateUploadRequest{Hash: strings.Repeat("a", 32), FileName: "sample.bin", FileSize: 5, ContentType: "application/octet-stream", ChunkSize: 3}
	_, base, e := normalizeTask(req, 100)
	if e != nil {
		t.Fatal(e)
	}
	req.Usage = "post"
	normalized, labeled, e := normalizeTask(req, 100)
	if e != nil || labeled == base || normalized.Usage != "post" {
		t.Fatal(normalized, labeled, e)
	}
	req.PoolID = "pool"
	if _, _, e = normalizeTask(req, 100); !errors.Is(e, media.ErrUnsupported) {
		t.Fatal(e)
	}
	req.PoolID = ""
	req.Usage = "arbitrary"
	if _, _, e = normalizeTask(req, 100); !errors.Is(e, media.ErrUnsupported) {
		t.Fatal(e)
	}
	u := media.Upload{Name: "f.txt", MIMEType: "text/plain", Size: 1, SHA256: strings.Repeat("b", 64), IdempotencyKey: "key", Usage: "post"}
	if e = validUpload(u, 100); e != nil {
		t.Fatal(e)
	}
	f := media.File{Name: u.Name, MIMEType: u.MIMEType, Size: u.Size, SHA256: u.SHA256, Public: u.Public}
	if same(f, u) {
		t.Fatal("usage absent from upload identity")
	}
	f.Usage = "post"
	if !same(f, u) {
		t.Fatal("matching metadata rejected")
	}
}
