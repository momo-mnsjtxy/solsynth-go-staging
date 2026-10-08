// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package mediastore

import (
	"context"
	"crypto/md5" // Documented transport checksum, never an authentication primitive.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"strconv"
	"syscall"

	"solsynth.local/go-migration/ports/media"
)

func (s *Filesystem) chunkRoot(key string, create bool) (*os.Root, error) {
	if !validKey(key) {
		return nil, media.ErrInvalid
	}
	name := key + ".chunks"
	if create {
		if err := s.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := s.syncDirectory(); err != nil {
			return nil, err
		}
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, media.ErrUnavailable
	}
	root, err := s.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	pinned, err := f.Stat()
	f.Close()
	if err != nil || !os.SameFile(info, pinned) {
		root.Close()
		return nil, media.ErrUnavailable
	}
	return root, nil
}

func syncChunkRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func openChunk(root *os.Root, index int) (*os.File, error) {
	f, err := root.OpenFile(strconv.Itoa(index)+".chunk", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, media.ErrUnavailable
	}
	return f, nil
}

func hashChunk(ctx context.Context, reader io.Reader, size int64) (string, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(h, io.LimitReader(contextReader{ctx, reader}, size+1), make([]byte, 64<<10))
	if err != nil {
		return "", err
	}
	if n > size {
		return "", media.ErrTooLarge
	}
	if n != size {
		return "", media.ErrDigest
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PutChunk is called only while the repository holds the canonical task lock.
// A chunk left by an uncertain SQL commit is adopted only for identical bytes.
func (s *Filesystem) PutChunk(ctx context.Context, key string, index int, reader io.Reader, size int64, expectedSHA string) (sha string, err error) {
	if !validKey(key) || index < 0 || index >= media.MaxUploadChunks || reader == nil || size < 1 || size > min(s.maxBytes, media.MaxChunkBytes) {
		return "", media.ErrInvalid
	}
	if expectedSHA != "" {
		if len(expectedSHA) != 64 {
			return "", media.ErrInvalid
		}
		if _, e := hex.DecodeString(expectedSHA); e != nil {
			return "", media.ErrInvalid
		}
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	root, err := s.chunkRoot(key, true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := strconv.Itoa(index)
	if err = root.Remove(name + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	existing, err := openChunk(root, index)
	if err == nil {
		prior, e := hashChunk(ctx, existing, size)
		existing.Close()
		if e != nil {
			return "", e
		}
		incoming, e := hashChunk(ctx, reader, size)
		if e != nil {
			return "", e
		}
		if expectedSHA != "" && prior != expectedSHA {
			return "", media.ErrDigest
		}
		if incoming != prior {
			return "", media.ErrConflict
		}
		return incoming, syncChunkRoot(root)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	f, err := root.OpenFile(name+".part", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		f.Close()
		if err != nil {
			_ = root.Remove(name + ".part")
		}
	}()
	sha, err = hashChunk(ctx, io.TeeReader(reader, f), size)
	if err != nil {
		return "", err
	}
	if expectedSHA != "" && sha != expectedSHA {
		return "", media.ErrConflict
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = root.Link(name+".part", name+".chunk"); err != nil {
		return "", err
	}
	if err = root.Remove(name + ".part"); err != nil {
		return "", err
	}
	return sha, syncChunkRoot(root)
}

func (s *Filesystem) removeChunks(ctx context.Context, key string) error {
	if !validKey(key) {
		return media.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// os.Root's descriptor-relative RemoveAll never follows a symlink target.
	if err := s.root.RemoveAll(key + ".chunks"); err != nil {
		return err
	}
	return s.syncDirectory()
}

type chunkSequence struct {
	ctx    context.Context
	root   *os.Root
	chunks []media.StoredChunk
	index  int
	file   *os.File
	digest hash.Hash
	read   int64
}

func (r *chunkSequence) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}
func (r *chunkSequence) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.index == len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	if r.file == nil {
		f, err := openChunk(r.root, chunk.Index)
		if err != nil {
			return 0, err
		}
		r.file = f
		r.digest = sha256.New()
		r.read = 0
	}
	// Read one extra byte to detect a modified/oversized on-disk chunk.
	if int64(len(p)) > chunk.Size-r.read+1 {
		p = p[:chunk.Size-r.read+1]
	}
	n, err := r.file.Read(p)
	r.digest.Write(p[:n])
	r.read += int64(n)
	if r.read > chunk.Size {
		return n, media.ErrDigest
	}
	if errors.Is(err, io.EOF) {
		closeErr := r.file.Close()
		r.file = nil
		if closeErr != nil {
			return n, closeErr
		}
		if r.read != chunk.Size || hex.EncodeToString(r.digest.Sum(nil)) != chunk.SHA256 {
			return n, media.ErrDigest
		}
		r.index++
		if r.index != len(r.chunks) {
			return n, nil
		}
	}
	return n, err
}

type fileChecksumReader struct {
	reader     io.Reader
	expected   string
	size, read int64
	digest     hash.Hash
}

func (r *fileChecksumReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	r.digest.Write(p[:n])
	if r.read > r.size {
		return n, media.ErrDigest
	}
	if errors.Is(err, io.EOF) && (r.read != r.size || hex.EncodeToString(r.digest.Sum(nil)) != r.expected) {
		return n, media.ErrDigest
	}
	return n, err
}

// AssembleChunks reuses durable atomic object publication. It accepts a fully
// verified final object after a crash between fsync and the metadata commit.
// Staging is removed before quota moves from reserved to used; an interrupted
// cleanup is retryable from that verified final object without needing chunks.
func (s *Filesystem) AssembleChunks(ctx context.Context, key string, chunks []media.StoredChunk, size int64, expectedMD5 string) (string, error) {
	if !validKey(key) || size < 1 || size > s.maxBytes || len(chunks) == 0 || len(chunks) > media.MaxUploadChunks || len(expectedMD5) != 32 {
		return "", media.ErrInvalid
	}
	if _, err := hex.DecodeString(expectedMD5); err != nil {
		return "", media.ErrInvalid
	}
	var total int64
	for i, c := range chunks {
		if c.Index != i || c.Size < 1 || c.Size > media.MaxChunkBytes || c.Size > size-total || len(c.SHA256) != 64 {
			return "", media.ErrInvalid
		}
		total += c.Size
	}
	if total != size {
		return "", media.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	final, err := s.Open(ctx, key)
	if err == nil {
		sha, e := hashChunk(ctx, &fileChecksumReader{reader: final, expected: expectedMD5, size: size, digest: md5.New()}, size)
		final.Close()
		if e != nil {
			return "", e
		}
		if e = s.root.Remove(key + ".part"); e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		return sha, s.removeChunks(ctx, key)
	}
	if !errors.Is(err, media.ErrNotFound) {
		return "", err
	}
	if err = s.root.Remove(key + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	root, err := s.chunkRoot(key, false)
	if err != nil {
		return "", err
	}
	defer root.Close()
	sequence := &chunkSequence{ctx: ctx, root: root, chunks: chunks}
	defer sequence.Close()
	reader := &fileChecksumReader{reader: sequence, expected: expectedMD5, size: size, digest: md5.New()}
	_, sha, err := s.PutMeasured(ctx, key, reader, size)
	if err != nil {
		return "", err
	}
	return sha, s.removeChunks(ctx, key)
}
