// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package mediastore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"solsynth.local/go-migration/ports/media"
)

func TestStreamingArtifactsStayInsidePrivateDescriptorNamespace(t *testing.T) {
	const id = "b1000000-0000-4000-8000-000000000001"
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(root, "streaming", "synthetic", id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := NewFilesystem(root, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.StreamingReady(context.Background(), "synthetic"); e != nil {
		t.Fatal(e)
	}
	good := filepath.Join(dir, "segment.ts")
	if e = os.WriteFile(good, []byte("synthetic-segment"), 0600); e != nil {
		t.Fatal(e)
	}
	f, n, _, e := store.OpenStreamingArtifact(context.Background(), "synthetic", id, "segment.ts", 100)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(f)
	f.Close()
	if e != nil || n != int64(len(b)) || string(b) != "synthetic-segment" {
		t.Fatal(n, e)
	}
	if e = os.Symlink(good, filepath.Join(dir, "linked.ts")); e != nil {
		t.Fatal(e)
	}
	if e = os.Link(good, filepath.Join(dir, "hardlinked.ts")); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Mkfifo(filepath.Join(dir, "pipe.ts"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "public.ts"), []byte("synthetic"), 0644); e != nil {
		t.Fatal(e)
	}
	for _, asset := range []string{"linked.ts", "hardlinked.ts", "segment.ts", "pipe.ts", "public.ts", "../segment.ts", "https://external.invalid/file.ts"} {
		if f, _, _, e := store.OpenStreamingArtifact(context.Background(), "synthetic", id, asset, 100); e == nil {
			f.Close()
			t.Fatal("unsafe artifact accepted", asset)
		}
	}
	if e = os.Symlink(dir, filepath.Join(root, "streaming", "linked")); e != nil {
		t.Fatal(e)
	}
	if e = store.StreamingReady(context.Background(), "linked"); e == nil {
		t.Fatal("directory symlink accepted")
	}
	var missing *Filesystem
	if _, _, _, e = missing.OpenStreamingArtifact(context.Background(), "synthetic", id, "segment.ts", 100); !errors.Is(e, media.ErrUnavailable) {
		t.Fatal("nil adapter", e)
	}
}
