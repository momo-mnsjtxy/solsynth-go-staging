// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

// These are complete duration values submitted by the pinned authored caller.
func RelationshipDuration(value string) (time.Duration, error) {
	switch value {
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	}
	return 0, ErrUnsupported
}

type ExpiryRequest struct {
	ExpiresIn *string `json:"expires_in"`
	DegradeTo *int    `json:"degrade_to"`
}

func (v *ExpiryRequest) UnmarshalJSON(raw []byte) error {
	*v = ExpiryRequest{}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		tok, e = d.Token()
		if e != nil {
			return ErrInvalid
		}
		key, ok := tok.(string)
		if !ok || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		switch key {
		case "expires_in":
			e = d.Decode(&v.ExpiresIn)
		case "degrade_to":
			e = d.Decode(&v.DegradeTo)
		default:
			return ErrInvalid
		}
		if e != nil {
			return ErrInvalid
		}
	}
	if tok, e = d.Token(); e != nil || tok != json.Delim('}') {
		return ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
func ValidateRelationshipChange(c RelationshipChange) error {
	if c.ExpiresIn != nil {
		if c.Action != Block && c.Action != Mute {
			return ErrUnsupported
		}
		if _, e := RelationshipDuration(*c.ExpiresIn); e != nil {
			return e
		}
	}
	if c.DegradeTo != nil && (c.Action != Block || c.ExpiresIn == nil || *c.DegradeTo != Muted) {
		return ErrUnsupported
	}
	return nil
}
