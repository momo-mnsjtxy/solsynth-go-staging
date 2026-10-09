// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
	"time"
)

type publicationProbe struct {
	calls   int
	failure error
}

func (p *publicationProbe) RecordPublication(context.Context, *sql.DB, *sql.Tx, posts.Post) error {
	p.calls++
	return p.failure
}
func TestPublicationTransitionObserver(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	for _, v := range []struct {
		name string
		p    posts.Post
		was  bool
		want int
	}{
		{"new", posts.Post{PublishedAt: &now}, false, 1},
		{"edit", posts.Post{PublishedAt: &now}, true, 0},
		{"draft", posts.Post{DraftedAt: &now}, false, 0},
		{"future", posts.Post{PublishedAt: &future}, false, 0},
		{"deleted", posts.Post{PublishedAt: &now, DeletedAt: &now}, false, 0},
	} {
		t.Run(v.name, func(t *testing.T) {
			probe := &publicationProbe{}
			r := New(nil).WithPublicationObserver(probe)
			if e := r.recordPublication(context.Background(), nil, v.p, v.was, now); e != nil || probe.calls != v.want {
				t.Fatal(e, probe.calls)
			}
		})
	}
	want := errors.New("enqueue failed")
	probe := &publicationProbe{failure: want}
	r := New(nil).WithPublicationObserver(probe)
	if e := r.recordPublication(context.Background(), nil, posts.Post{PublishedAt: &now}, false, now); !errors.Is(e, want) {
		t.Fatal("enqueue failure hidden", e)
	}
}
