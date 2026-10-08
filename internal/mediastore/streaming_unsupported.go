//go:build !linux

// SPDX-License-Identifier: AGPL-3.0-only
package mediastore

import (
	"context"
	"io"
	"solsynth.local/go-migration/ports/media"
	"time"
)

func (*Filesystem) StreamingReady(context.Context, string) error { return media.ErrUnsupported }
func (*Filesystem) OpenStreamingArtifact(context.Context, string, string, string, int64) (io.ReadSeekCloser, int64, time.Time, error) {
	return nil, 0, time.Time{}, media.ErrUnsupported
}
