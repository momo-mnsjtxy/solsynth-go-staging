// SPDX-License-Identifier: AGPL-3.0-only
//go:build !linux

package mediastore

import (
	"context"
	"io"
	"solsynth.local/go-migration/ports/media"
)

// Other operating systems fail explicitly until an equivalent no-follow,
// descriptor-rooted, durable filesystem adapter has been verified there.
type Filesystem struct{}

func NewFilesystem(string, int64) (*Filesystem, error) { return nil, media.ErrUnsupported }
func (*Filesystem) Close() error                       { return nil }
func (*Filesystem) Ready(context.Context) error        { return media.ErrUnsupported }
func (*Filesystem) Put(context.Context, string, io.Reader, int64, string) error {
	return media.ErrUnsupported
}
func (*Filesystem) PutMeasured(context.Context, string, io.Reader, int64) (int64, string, error) {
	return 0, "", media.ErrUnsupported
}
func (*Filesystem) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, media.ErrUnsupported
}
func (*Filesystem) Remove(context.Context, string) error { return media.ErrUnsupported }
