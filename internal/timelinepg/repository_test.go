// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"testing"
	"time"
)

func TestCursorExactTimestampAndUUID(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 123456000, time.UTC)
	p := posts.Post{ID: "10000000-0000-4000-8000-000000000001", PublishedAt: &now}
	encoded := encodeCursor(p)
	got, id, e := Cursor(encoded)
	if e != nil || !got.Equal(now) || id != p.ID {
		t.Fatal(got, id, e)
	}
	got, id, e = Cursor(now.Format(time.RFC3339Nano))
	if e != nil || !got.Equal(now) || id != "" {
		t.Fatal(got, id, e)
	}
	for _, v := range []string{"yesterday", "v1.", "v1.!!!", strings.Repeat("x", 257), "2026-99-01T00:00:00Z"} {
		if _, _, e = Cursor(v); !errors.Is(e, timeline.ErrCursor) {
			t.Fatal(v, e)
		}
	}
}
func TestMissingRepositoryAndAuthorityFailClosed(t *testing.T) {
	r := NewWithAuthority(nil, nil)
	if _, e := r.Subscribe(context.Background(), "10000000-0000-4000-8000-000000000001", "pub"); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.WatchPreferences(context.Background(), "spoof"); !errors.Is(e, posts.ErrUnauthenticated) {
		t.Fatal(e)
	}
	if e := r.Apply(nil, nil, "", nil, posts.Patch{}, true); e == nil {
		t.Fatal("missing canonical transaction")
	}
}
func TestSearchBoundsAndUnsupportedShapes(t *testing.T) {
	for _, o := range []timeline.SearchOptions{{Take: 0}, {Take: 101}, {Take: 20, Offset: -1}, {Take: 20, Query: strings.Repeat("q", 1025)}, {Take: 20, Tags: []string{""}}, {Take: 20, Order: "injection"}} {
		if e := validateSearch(o); e == nil {
			t.Fatal(o)
		}
	}
}
