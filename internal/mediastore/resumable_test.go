// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package mediastore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"solsynth.local/go-migration/ports/media"
)

func md5sum(body string) string { sum := md5.Sum([]byte(body)); return hex.EncodeToString(sum[:]) }
func stageChunks(t *testing.T, s *Filesystem, parts []string) []media.StoredChunk {
	t.Helper()
	chunks := []media.StoredChunk{}
	for i, part := range parts {
		sha, e := s.PutChunk(context.Background(), key, i, strings.NewReader(part), int64(len(part)), "")
		if e != nil {
			t.Fatal(e)
		}
		chunks = append(chunks, media.StoredChunk{Index: i, Size: int64(len(part)), SHA256: sha})
	}
	return chunks
}
func TestResumableChunksReplayRestartAndAtomicAssembly(t *testing.T) {
	s, dir := filesystem(t)
	ctx := context.Background()
	chunks := stageChunks(t, s, []string{"hel", "lo"})
	sha, e := s.PutChunk(ctx, key, 0, strings.NewReader("hel"), 3, "")
	if e != nil || sha != digest("hel") {
		t.Fatal(sha, e)
	}
	if _, e = s.PutChunk(ctx, key, 0, strings.NewReader("new"), 3, ""); !errors.Is(e, media.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	if _, e = s.Open(ctx, key); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("staging became readable", e)
	}
	reopened, e := NewFilesystem(dir, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	sha, e = reopened.AssembleChunks(ctx, key, chunks, 5, md5sum("hello"))
	if e != nil || sha != digest("hello") {
		t.Fatal(sha, e)
	}
	for _, name := range []string{key + ".part", key + ".chunks"} {
		if _, e = os.Stat(filepath.Join(dir, name)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("staging retained", name, e)
		}
	}
	// Recovery after object publication and staging cleanup but before SQL commit.
	sha, e = reopened.AssembleChunks(ctx, key, chunks, 5, md5sum("hello"))
	if e != nil || sha != digest("hello") {
		t.Fatal("verified recovery", sha, e)
	}
	if _, e = reopened.AssembleChunks(ctx, key, chunks, 5, md5sum("other")); !errors.Is(e, media.ErrDigest) {
		t.Fatal("unverified recovery", e)
	}
	f, e := reopened.Open(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(b) != "hello" {
		t.Fatal(string(b), e)
	}
}

func TestResumableBoundsIntegrityAndInterruption(t *testing.T) {
	for _, body := range []string{"", "ab", "abcd"} {
		t.Run("size_"+body, func(t *testing.T) {
			s, dir := filesystem(t)
			if _, e := s.PutChunk(context.Background(), key, 0, strings.NewReader(body), 3, ""); e == nil {
				t.Fatal("size accepted")
			}
			entries, e := os.ReadDir(filepath.Join(dir, key+".chunks"))
			if e != nil || len(entries) != 0 {
				t.Fatal("partial retained", entries, e)
			}
		})
	}
	for _, index := range []int{-1, media.MaxUploadChunks} {
		s, _ := filesystem(t)
		if _, e := s.PutChunk(context.Background(), key, index, strings.NewReader("a"), 1, ""); e == nil {
			t.Fatal("index accepted")
		}
	}
	s, dir := filesystem(t)
	ctx := context.Background()
	chunks := stageChunks(t, s, []string{"hel", "lo"})
	if e := os.WriteFile(filepath.Join(dir, key+".part"), []byte("interrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := s.AssembleChunks(ctx, key, chunks, 5, md5sum("other")); !errors.Is(e, media.ErrDigest) {
		t.Fatal(e)
	}
	if _, e := s.Open(ctx, key); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("bad checksum published", e)
	}
	if _, e := s.AssembleChunks(ctx, key, chunks, 5, md5sum("hello")); e != nil {
		t.Fatal("retry failed", e)
	}
	if e := s.Remove(ctx, key); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
	stageChunks(t, s, []string{"hel"})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.PutChunk(canceled, key, 1, strings.NewReader("lo"), 2, ""); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e := s.Remove(ctx, key); e != nil {
		t.Fatal(e)
	}
}

func TestResumableRejectsChunkCorruptionAndUnsafeEntries(t *testing.T) {
	for _, kind := range []string{"corruption", "symlink", "hardlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := filesystem(t)
			chunks := stageChunks(t, s, []string{"hello"})
			name := filepath.Join(dir, key+".chunks", "0.chunk")
			outside := filepath.Join(t.TempDir(), "sentinel")
			if e := os.WriteFile(outside, []byte("hello"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Remove(name); e != nil {
				t.Fatal(e)
			}
			var e error
			switch kind {
			case "corruption":
				e = os.WriteFile(name, []byte("other"), 0600)
			case "symlink":
				e = os.Symlink(outside, name)
			case "hardlink":
				e = os.Link(outside, name)
			case "directory":
				e = os.Mkdir(name, 0700)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.AssembleChunks(context.Background(), key, chunks, 5, md5sum("hello")); e == nil {
				t.Fatal("unsafe chunk accepted")
			}
			if e = s.Remove(context.Background(), key); e != nil {
				t.Fatal(e)
			}
			b, e := os.ReadFile(outside)
			if e != nil || string(b) != "hello" {
				t.Fatal("outside changed", e)
			}
		})
	}
	s, dir := filesystem(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(dir, key+".chunks")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PutChunk(context.Background(), key, 0, strings.NewReader("a"), 1, ""); e == nil {
		t.Fatal("symlink root accepted")
	}
	if e := s.Remove(context.Background(), key); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(outside); e != nil {
		t.Fatal("outside directory removed", e)
	}
}

func TestResumableAdoptsUnrecordedChunkAndRemovesStalePart(t *testing.T) {
	s, dir := filesystem(t)
	chunks := stageChunks(t, s, []string{"hello"})
	if e := os.WriteFile(filepath.Join(dir, key+".chunks", "0.part"), []byte("interrupted"), 0600); e != nil {
		t.Fatal(e)
	}
	sha, e := s.PutChunk(context.Background(), key, 0, strings.NewReader("hello"), 5, "")
	if e != nil || sha != chunks[0].SHA256 {
		t.Fatal(sha, e)
	}
	if _, e = os.Stat(filepath.Join(dir, key+".chunks", "0.part")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	if e = s.Remove(context.Background(), key); e != nil {
		t.Fatal(e)
	}
}

func TestResumableRestoresRecordedChunkWithoutAcceptingChangedBytes(t *testing.T) {
	s, dir := filesystem(t)
	ctx := context.Background()
	chunks := stageChunks(t, s, []string{"hello"})
	name := filepath.Join(dir, key+".chunks", "0.chunk")
	if e := os.Remove(name); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PutChunk(ctx, key, 0, strings.NewReader("other"), 5, chunks[0].SHA256); !errors.Is(e, media.ErrConflict) {
		t.Fatal(e)
	}
	if _, e := os.Stat(name); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("bad restore published", e)
	}
	if _, e := s.PutChunk(ctx, key, 0, strings.NewReader("hello"), 5, chunks[0].SHA256); e != nil {
		t.Fatal("valid restore blocked", e)
	}
	if _, e := s.AssembleChunks(ctx, key, chunks, 5, md5sum("hello")); e != nil {
		t.Fatal(e)
	}
}
