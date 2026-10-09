// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"testing"
	"time"
)

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
