// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
)

func TestUnavailableAndInvalidIdentifiers(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	if _, e := r.Bookmark(ctx, "", ""); !errors.Is(e, interactions.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e := r.Reactions(ctx, "", "bad", interactions.Page{Take: 20}); !errors.Is(e, posts.ErrNotFound) {
		t.Fatal(e)
	}
	if _, _, e := r.Collections(ctx, "", "", interactions.Page{Take: 20, Query: "long"}); !errors.Is(e, interactions.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := validIDs([]string{"notuuid"}); !errors.Is(e, interactions.ErrInvalid) {
		t.Fatal(e)
	}
}
func TestNormalizedTagsShareExistingInvariantRules(t *testing.T) {
	got := normalized([]string{" mixed ", "MiXeD", "", "İ"})
	if len(got) != 2 || got[0] != "mixed" || got[1] != "İ" {
		t.Fatal(got)
	}
}
