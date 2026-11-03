// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDraftListingIncludesScheduledOnlyWhenRequested(t *testing.T) {
	for _, tc := range []struct {
		query   string
		include bool
		invalid bool
	}{
		{"", false, false},
		{"?include_scheduled=false", false, false},
		{"?include_scheduled=true", true, false},
		{"?include_scheduled=TRUE", true, false},
		{"?include_scheduled=wrong", false, true},
		{"?include_scheduled=true&include_scheduled=false", false, true},
	} {
		r := httptest.NewRequest("GET", "/api/posts/drafts"+tc.query, nil)
		o, e := listOptions(r, true)
		if (e != nil) != tc.invalid || e == nil && (o.IncludeScheduled != tc.include || !o.Drafts) {
			t.Fatal(tc.query, o, e)
		}
	}
	if _, e := listOptions(httptest.NewRequest("GET", "/api/posts?include_scheduled=true", nil), false); e == nil {
		t.Fatal("scheduled draft selector accepted on public post listing")
	}
}

func TestScheduledWireProjectionKeepsStoredDraftPrivate(t *testing.T) {
	now := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	due := now.Add(time.Hour)
	p := Post{DraftedAt: &now, Schedule: &Schedule{ID: 7, DueAt: due, State: "pending"}}
	b, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]any
	if e = json.Unmarshal(b, &wire); e != nil {
		t.Fatal(e)
	}
	if wire["drafted_at"] != nil || wire["published_at"] != due.Format(time.RFC3339) || p.DraftedAt == nil || p.PublishedAt != nil {
		t.Fatal(string(b), p)
	}
	p.Schedule.State = "blocked"
	b, e = json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &wire); e != nil {
		t.Fatal(e)
	}
	if wire["drafted_at"] == nil || wire["published_at"] != nil {
		t.Fatal("blocked schedule looked published", string(b))
	}
}
