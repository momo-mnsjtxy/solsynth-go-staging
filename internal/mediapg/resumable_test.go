// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"errors"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/media"
)

func TestResumableRequestBoundsAndFullIdempotencyFingerprint(t *testing.T) {
	good := media.CreateUploadRequest{Hash: strings.Repeat("a", 32), FileName: "sample.bin", FileSize: 5, ContentType: "application/octet-stream", ChunkSize: 3}
	_, base, e := normalizeTask(good, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*media.CreateUploadRequest){func(r *media.CreateUploadRequest) { r.FileName = "other.bin" }, func(r *media.CreateUploadRequest) { r.ContentType = "text/plain" }, func(r *media.CreateUploadRequest) { r.FileSize = 6 }, func(r *media.CreateUploadRequest) { r.ChunkSize = 4 }, func(r *media.CreateUploadRequest) { r.Hash = strings.Repeat("b", 32) }} {
		r := good
		change(&r)
		_, fingerprint, e := normalizeTask(r, 100)
		if e != nil || fingerprint == base {
			t.Fatal("request meaning not bound", r, e)
		}
	}
	for _, change := range []func(*media.CreateUploadRequest){func(r *media.CreateUploadRequest) { r.FileName = "../file" }, func(r *media.CreateUploadRequest) { r.FileSize = 101 }, func(r *media.CreateUploadRequest) { r.FileSize = 0 }, func(r *media.CreateUploadRequest) { r.ChunkSize = -1 }, func(r *media.CreateUploadRequest) { r.ChunkSize = media.MaxChunkBytes + 1 }, func(r *media.CreateUploadRequest) { r.Hash = "not-md5" }} {
		r := good
		change(&r)
		if _, _, e := normalizeTask(r, 100); e == nil {
			t.Fatal("invalid request", r)
		}
	}
	r := good
	r.FileSize = media.MaxUploadChunks + 1
	r.ChunkSize = 1
	if _, _, e = normalizeTask(r, 1<<20); !errors.Is(e, media.ErrInvalid) {
		t.Fatal("unbounded chunks", e)
	}
	r = good
	r.EncryptionScheme = "file.aesgcm.v1"
	if _, _, e = normalizeTask(r, 100); !errors.Is(e, media.ErrUnsupported) {
		t.Fatal("claimed encryption", e)
	}
	for _, key := range []string{"bad key", "bad\nkey", strings.Repeat("a", 129)} {
		if _, e = taskKey(key); e == nil {
			t.Fatal("bad key")
		}
	}
	a, e := taskKey("same")
	b, e2 := taskKey("same")
	c, e3 := taskKey("other")
	if e != nil || e2 != nil || e3 != nil || a != b || a == c {
		t.Fatal(a, b, c, e, e2, e3)
	}
}
