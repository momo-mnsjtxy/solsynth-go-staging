// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

func TestUnavailableDoesNotPanic(t *testing.T) {
	r := New(nil)
	if _, e := r.Create(context.Background(), "", "", posts.Patch{}); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, _, e := r.List(context.Background(), "", posts.ListOptions{Take: 20}); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestPatchDoesNotReplaceOmittedFields(t *testing.T) {
	title, content := "before", "body"
	p := posts.Post{Title: &title, Content: content, Visibility: posts.Private, Type: posts.Article}
	newContent := "after"
	apply(&p, posts.Patch{Content: &newContent})
	if *p.Title != "before" || p.Content != "after" || p.Visibility != posts.Private || p.Type != posts.Article {
		t.Fatal(p)
	}
	now := time.Now()
	apply(&p, posts.Patch{DraftedAt: &now})
	if p.PublishedAt != nil || p.DraftedAt == nil {
		t.Fatal(p)
	}
	apply(&p, posts.Patch{PublishedAt: &now})
	if p.PublishedAt == nil || p.DraftedAt != nil {
		t.Fatal(p)
	}
}
