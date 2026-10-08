// SPDX-License-Identifier: AGPL-3.0-only
package application

import "context"

func (b *Backend) CleanupMedia(ctx context.Context, limit int) (int, error) {
	if b.media == nil {
		return 0, nil
	}
	return b.media.Cleanup(ctx, limit)
}
