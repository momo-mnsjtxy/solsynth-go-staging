// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

// Package mediastore provides a filesystem-only object adapter, rooted in an
// explicitly provisioned private directory. It does not create directories.
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
	"syscall"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

type Filesystem struct {
	root     *os.Root
	maxBytes int64
}

func NewFilesystem(path string, maxBytes int64) (*Filesystem, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || maxBytes <= 0 || maxBytes > 1<<40 {
		return nil, media.ErrInvalid
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return nil, media.ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, media.ErrInvalid
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	pinned, statErr := opened.Stat()
	opened.Close()
	if statErr != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, media.ErrInvalid
	}
	return &Filesystem{root: root, maxBytes: maxBytes}, nil
}
func (s *Filesystem) Close() error { return s.root.Close() }
func validKey(key string) bool     { return authority.ValidID(key) && key == strings.ToLower(key) }
func (s *Filesystem) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.root == nil {
		return media.ErrUnavailable
	}
	f, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return media.ErrUnavailable
	}
	return nil
}
func (s *Filesystem) syncDirectory() error {
	d, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func (s *Filesystem) Put(ctx context.Context, key string, reader io.Reader, size int64, digest string) (err error) {
	if !validKey(key) || reader == nil || size < 0 || size > s.maxBytes || len(digest) != 64 {
		return media.ErrInvalid
	}
	_, _, err = s.put(ctx, key, reader, size, &digest)
	return err
}
func (s *Filesystem) PutMeasured(ctx context.Context, key string, reader io.Reader, limit int64) (int64, string, error) {
	return s.put(ctx, key, reader, limit, nil)
}
func (s *Filesystem) put(ctx context.Context, key string, reader io.Reader, size int64, digest *string) (actual int64, sha string, err error) {
	if !validKey(key) || reader == nil || size < 0 || size > s.maxBytes {
		return 0, "", media.ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return 0, "", err
	}
	temp := key + ".part"
	f, err := s.root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return 0, "", err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = s.root.Remove(temp)
		}
	}()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, reader}, size+1), make([]byte, 64<<10))
	if err != nil {
		return 0, "", err
	}
	sha = hex.EncodeToString(h.Sum(nil))
	if n > size {
		return 0, "", media.ErrTooLarge
	}
	if digest != nil && (n != size || sha != *digest) {
		return 0, "", media.ErrDigest
	}
	if err = ctx.Err(); err != nil {
		return 0, "", err
	}
	if err = f.Sync(); err != nil {
		return 0, "", err
	}
	if err = f.Close(); err != nil {
		return 0, "", err
	}
	// Link publishes only if the destination is absent. Never overwrite an
	// existing object, including one left by an uncertain earlier commit.
	if err = s.root.Link(temp, key); err != nil {
		return 0, "", err
	}
	if err = s.root.Remove(temp); err != nil {
		return 0, "", err
	}
	return n, sha, s.syncDirectory()
}
func (s *Filesystem) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	if !validKey(key) {
		return nil, media.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.root.OpenFile(key, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, media.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, media.ErrUnavailable
	}
	return f, nil
}
func (s *Filesystem) Remove(ctx context.Context, key string) error {
	if !validKey(key) {
		return media.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{key, key + ".part"} {
		if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.removeChunks(ctx, key)
}
