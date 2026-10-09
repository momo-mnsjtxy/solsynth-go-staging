// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/media"
)

func (b *Backend) CleanupMedia(ctx context.Context, limit int) (int, error) {
	if b.media == nil {
		return 0, nil
	}
	if limit < 1 || limit > 1000 {
		return 0, media.ErrInvalid
	}
	var v int
	var ve error
	if b.voiceEnabled {
		v, ve = b.media.CleanupVoices(ctx, limit)
	}
	n, e := b.media.CleanupArtworks(ctx, min(limit, 200))
	m, x := b.media.Cleanup(ctx, limit)
	return v + n + m, errors.Join(ve, e, x)
}
