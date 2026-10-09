// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// ProcessScheduledPosts is a bounded local lifecycle hook. It never sends to a
// provider; existing downstream workers observe committed canonical publication.
func (b *Backend) ProcessScheduledPosts(ctx context.Context, limit int) (int, error) {
	if b == nil || b.scheduledPosts == nil {
		return 0, posts.ErrUnavailable
	}
	return b.scheduledPosts.ProcessScheduled(ctx, limit)
}
