// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"time"
)

// Schedule is an owned lifecycle diagnostic for the original published_at
// contract. It contains no credentials or authority proof.
type Schedule struct {
	ID      int64     `json:"id"`
	DueAt   time.Time `json:"due_at"`
	State   string    `json:"state"`
	Failure *string   `json:"failure,omitempty"`
}

// The stored pending post remains a draft, so all canonical readers deny early
// publication. The originating API still reports the authored future timestamp.
func (p Post) MarshalJSON() ([]byte, error) {
	type plain Post
	v := plain(p)
	if p.Schedule != nil && p.Schedule.State == "pending" {
		due := p.Schedule.DueAt
		v.DraftedAt = nil
		v.PublishedAt = &due
	}
	return json.Marshal(v)
}
