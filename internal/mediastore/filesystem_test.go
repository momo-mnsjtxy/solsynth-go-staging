// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package mediastore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"solsynth.local/go-migration/ports/media"
)

const key = "a0000000-0000-4000-8000-000000000001"

func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func filesystem(t *testing.T) (*Filesystem, string) {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := NewFilesystem(dir, 1024)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func TestFilesystemRoundTripRestartAndRemoval(t *testing.T) {
	s, dir := filesystem(t)
	ctx := context.Background()
	if e := s.Put(ctx, key, strings.NewReader("hello"), 5, digest("hello")); e != nil {
		t.Fatal(e)
	}
	reopened, e := NewFilesystem(dir, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	f, e := reopened.Open(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(f)
	f.Close()
	if e != nil || string(b) != "hello" {
		t.Fatal(string(b), e)
	}
	if e = reopened.Remove(ctx, key); e != nil {
		t.Fatal(e)
	}
	if e = reopened.Remove(ctx, key); e != nil {
		t.Fatal("idempotent remove", e)
	}
	if _, e = reopened.Open(ctx, key); !errors.Is(e, media.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestFilesystemBoundedIntegrityCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size       int64
		sha        string
		cancel     bool
	}{{"short", "abc", 5, digest("abc"), false}, {"long", "abcdef", 5, digest("abcde"), false}, {"digest", "hello", 5, digest("other"), false}, {"cancel", "hello", 5, digest("hello"), true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := filesystem(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if e := s.Put(ctx, key, strings.NewReader(tc.body), tc.size, tc.sha); e == nil {
				t.Fatal("accepted invalid stream")
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) != 0 {
				t.Fatal("partial bytes remain", entries, e)
			}
		})
	}
}
func TestFilesystemIsolationAndNoOverwrite(t *testing.T) {
	s, dir := filesystem(t)
	ctx := context.Background()
	for _, bad := range []string{"../outside", "/tmp/object", "a/b", key + ".part", strings.ToUpper(key)} {
		if _, e := s.Open(ctx, bad); e == nil {
			t.Fatal("bad key accepted", bad)
		}
		if e := s.Remove(ctx, bad); e == nil {
			t.Fatal("bad remove accepted")
		}
	}
	outside := filepath.Join(t.TempDir(), "sentinel")
	if e := os.WriteFile(outside, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(dir, key)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Open(ctx, key); e == nil {
		t.Fatal("symlink opened")
	}
	if e := s.Put(ctx, key, strings.NewReader("hello"), 5, digest("hello")); e == nil {
		t.Fatal("existing key overwritten")
	}
	if e := s.Remove(ctx, key); e != nil {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(outside); e != nil || string(b) != "private" {
		t.Fatal("outside object altered", e)
	}
	if e := os.Link(outside, filepath.Join(dir, key)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Open(ctx, key); e == nil {
		t.Fatal("external hard link opened")
	}
}
func TestFilesystemConcurrentPublish(t *testing.T) {
	s, _ := filesystem(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.Put(context.Background(), key, strings.NewReader("hello"), 5, digest("hello"))
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal(successes)
	}
	f, e := s.Open(context.Background(), key)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	b, e := io.ReadAll(f)
	if e != nil || string(b) != "hello" {
		t.Fatal(string(b), e)
	}
}
func TestFilesystemRequiresPrivateExplicitRoot(t *testing.T) {
	if _, e := NewFilesystem("", 1024); e == nil {
		t.Fatal("empty root")
	}
	if _, e := NewFilesystem("relative", 1024); e == nil {
		t.Fatal("relative root")
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if _, e := NewFilesystem(dir, 1024); e == nil {
		t.Fatal("shared root")
	}
	private := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if e := os.Symlink(private, link); e != nil {
		t.Fatal(e)
	}
	if _, e := NewFilesystem(link, 1024); e == nil {
		t.Fatal("symlink root")
	}
}
