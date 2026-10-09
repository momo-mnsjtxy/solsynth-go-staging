//go:build linux

// SPDX-License-Identifier: AGPL-3.0-only
package mediastore

import (
	"context"
	"errors"
	"io"
	"os"
	"solsynth.local/go-migration/ports/media"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	"syscall"
	"time"
)

// streamingDir walks only fixed/validated single components, opening each
// relative to its already-held parent descriptor. O_DIRECTORY|O_NOFOLLOW rejects
// symlinks and special files before any artifact is exposed. No directory is
// created: provisioning and provider storage permissions remain operator work.
func (s *Filesystem) streamingDir(ctx context.Context, parts ...string) (*os.File, error) {
	if s == nil || s.root == nil {
		return nil, media.ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	parent, e := s.root.Open(".")
	if e != nil {
		return nil, media.ErrUnavailable
	}
	info, e := parent.Stat()
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		parent.Close()
		return nil, media.ErrUnavailable
	}
	for _, part := range parts {
		if e = ctx.Err(); e != nil {
			parent.Close()
			return nil, e
		}
		fd, x := syscall.Openat(int(parent.Fd()), part, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		parent.Close()
		if x != nil {
			return nil, streamingError(x)
		}
		parent = os.NewFile(uintptr(fd), "private-streaming-directory")
		info, x := parent.Stat()
		if x != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			parent.Close()
			return nil, media.ErrUnavailable
		}
	}
	return parent, nil
}
func streamingError(e error) error {
	if errors.Is(e, os.ErrNotExist) {
		return media.ErrNotFound
	}
	return media.ErrUnavailable
}
func (s *Filesystem) StreamingReady(ctx context.Context, namespace string) error {
	if !live.ValidOutputDestination(namespace) {
		return media.ErrInvalid
	}
	root, e := s.streamingDir(ctx, "streaming", namespace)
	if e != nil {
		return e
	}
	return root.Close()
}
func (s *Filesystem) OpenStreamingArtifact(ctx context.Context, namespace, output, asset string, limit int64) (io.ReadSeekCloser, int64, time.Time, error) {
	var zero time.Time
	if s == nil || s.root == nil {
		return nil, 0, zero, media.ErrUnavailable
	}
	if !live.ValidOutputDestination(namespace) || !validKey(output) || !live.ValidOutputAsset(asset) || limit < 1 || limit > s.maxBytes {
		return nil, 0, zero, media.ErrInvalid
	}
	root, e := s.streamingDir(ctx, "streaming", namespace, output)
	if e != nil {
		return nil, 0, zero, e
	}
	defer root.Close()
	if e = ctx.Err(); e != nil {
		return nil, 0, zero, e
	}
	fd, e := syscall.Openat(int(root.Fd()), asset, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, 0, zero, streamingError(e)
	}
	f := os.NewFile(uintptr(fd), "private-streaming-artifact")
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, 0, zero, media.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > limit {
		f.Close()
		return nil, 0, zero, media.ErrUnavailable
	}
	return f, info.Size(), info.ModTime().UTC(), nil
}
