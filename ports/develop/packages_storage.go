// SPDX-License-Identifier: AGPL-3.0-only
// Local synthetic adapter authored 2026-10-08. Not a Minio/S3 replacement.
package develop

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxSyntheticPackageBytes matches the licensed source upload bound. Applying
// it to reads is a deliberate local candidate limit, not historical S3 parity.
const MaxSyntheticPackageBytes int64 = 5 * 1024 * 1024

// LocalPackageStorage reads genuine local files from an explicitly supplied
// synthetic root. os.Root contains resolution, including symlinks, within that
// root. A lexical check alone would not provide that guarantee.
type LocalPackageStorage struct{ root *os.Root }

func NewSyntheticLocalPackageStorage(directory string) (*LocalPackageStorage, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &LocalPackageStorage{root: root}, nil
}

func (s *LocalPackageStorage) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *LocalPackageStorage) ReadPackage(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.root == nil {
		return nil, ErrPackageUnavailable
	}
	if !filepath.IsLocal(key) || strings.ContainsAny(key, "\\\x00") {
		return nil, ErrPackageInvalidKey
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, ErrPackageInvalidKey
		}
	}
	// Avoid opening known special files. The root is synthetic fixture storage
	// controlled by its owner, not a writable directory granted to clients.
	info, err := s.root.Stat(key)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSyntheticPackageBytes {
		return nil, ErrPackageUnavailable
	}
	file, err := s.root.Open(key)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSyntheticPackageBytes {
		return nil, ErrPackageUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(packageContextReader{ctx: ctx, reader: file}, MaxSyntheticPackageBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxSyntheticPackageBytes {
		return nil, ErrPackageUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

type packageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r packageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
