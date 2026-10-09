// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
)

// The retained authored Solian pin caller sends expires_at as Unix milliseconds.
// ISO timestamps are also accepted for the controller's Instant representation.
func (p *PinRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		MessageID string          `json:"message_id"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if e := json.Unmarshal(data, &raw); e != nil {
		return ErrInvalid
	}
	p.MessageID = raw.MessageID
	p.ExpiresAt = nil
	value := bytes.TrimSpace(raw.ExpiresAt)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return nil
	}
	var at time.Time
	if value[0] == '"' {
		var s string
		if json.Unmarshal(value, &s) != nil {
			return ErrInvalid
		}
		var e error
		at, e = time.Parse(time.RFC3339Nano, s)
		if e != nil {
			return ErrInvalid
		}
	} else {
		ms, e := strconv.ParseInt(string(value), 10, 64)
		if e != nil || ms < -62135596800000 || ms > 253402300799999 {
			return ErrInvalid
		}
		at = time.UnixMilli(ms).UTC()
	}
	if at.Year() < 1 || at.Year() > 9999 {
		return ErrInvalid
	}
	p.ExpiresAt = &at
	return nil
}
