// SPDX-License-Identifier: AGPL-3.0-only
package postmediapg

import (
	"context"
	"errors"
	"testing"

	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func TestUnconfiguredAssociationAndVisibilityFailClosed(t *testing.T) {
	r := New(nil, nil)
	if e := r.Ready(context.Background()); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.CanReadInTransaction(context.Background(), nil, "", ""); !errors.Is(e, media.ErrUnavailable) {
		t.Fatal(e)
	}
	if e := r.Load(context.Background(), nil, "", &posts.Post{}); !errors.Is(e, posts.ErrInvalid) {
		t.Fatal(e)
	}
}
