// SPDX-License-Identifier: AGPL-3.0-only
package posts

import (
	"encoding/json"
	"math"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Embed contains only a canonical reference or a location explicitly supplied
// by the composer. It never accepts snapshots of another domain's private data.
type Embed struct {
	Type    string  `json:"type"`
	ID      string  `json:"id,omitempty"`
	Name    *string `json:"name,omitempty"`
	Address *string `json:"address,omitempty"`
	WKT     *string `json:"wkt,omitempty"`
}

func (e Embed) Validate() error {
	switch e.Type {
	case "survey", "fund", "meet", "live_stream", "notable_day", "calendar_event":
		if !authority.ValidID(e.ID) || e.Name != nil || e.Address != nil || e.WKT != nil {
			return ErrInvalid
		}
	case "location":
		if e.ID != "" || !locationText(e.Name, 256) || !locationText(e.Address, 1024) || !locationText(e.WKT, 128) {
			return ErrInvalid
		}
		if !empty(e.WKT) {
			if _, err := normalizePoint(*e.WKT); err != nil {
				return err
			}
		}
		if empty(e.Name) && empty(e.Address) && empty(e.WKT) {
			return ErrInvalid
		}
	default:
		return ErrUnsupported
	}
	return nil
}
func empty(v *string) bool { return v == nil || strings.TrimSpace(*v) == "" }
func ValidateEmbeds(v []Embed) error {
	if len(v) > 16 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, e := range v {
		if err := e.Validate(); err != nil {
			return err
		}
		if seen[e.Type] {
			return ErrInvalid
		}
		seen[e.Type] = true
	}
	return nil
}

// ResolveEmbeds preserves omitted fields, replaces an explicit list (including
// []), and applies explicitly supplied legacy fields. Conflicting forms fail.
func ResolveEmbeds(old []Embed, p Patch) ([]Embed, error) {
	out := append([]Embed{}, old...)
	if p.Embeds != nil {
		if len(p.EmbedUpdates) > 0 {
			return nil, ErrInvalid
		}
		out = append([]Embed{}, (*p.Embeds)...)
	}
	for _, kind := range []string{"survey", "fund", "meet", "live_stream", "notable_day", "calendar_event", "location"} {
		value, ok := p.EmbedUpdates[kind]
		if !ok {
			continue
		}
		next := []Embed{}
		for _, v := range out {
			if v.Type != kind {
				next = append(next, v)
			}
		}
		out = next
		if value != nil {
			out = append(out, *value)
		}
	}
	for i := range out {
		if out[i].WKT != nil && !empty(out[i].WKT) {
			point, e := normalizePoint(*out[i].WKT)
			if e != nil {
				return nil, ErrInvalid
			}
			s := point
			out[i].WKT = &s
		}
	}
	return out, ValidateEmbeds(out)
}
func (p Patch) HasComposition() bool {
	return p.Embeds != nil || len(p.EmbedUpdates) > 0 || p.ThumbnailSet
}

func (m Metadata) MarshalJSON() ([]byte, error) {
	embeds := []any{}
	for _, e := range m.Embeds {
		embeds = append(embeds, e)
	}
	for _, e := range m.Composed {
		embeds = append(embeds, e)
	}
	return json.Marshal(struct {
		Embeds    []any   `json:"embeds"`
		Thumbnail *string `json:"thumbnail,omitempty"`
	}{embeds, m.Thumbnail})
}

func locationText(s *string, n int) bool {
	return s == nil || utf8.ValidString(*s) && !strings.ContainsRune(*s, 0) && taxonomy.UTF16Length(*s) <= n
}

var compositionPoint = regexp.MustCompile(`(?i)^POINT\s*\(\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?)\s+([+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[Ee][+-]?\d+)?)\s*\)$`)

func normalizePoint(s string) (string, error) {
	m := compositionPoint.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", ErrInvalid
	}
	lon, e := strconv.ParseFloat(m[1], 64)
	if e != nil {
		return "", ErrInvalid
	}
	lat, e := strconv.ParseFloat(m[2], 64)
	if e != nil || math.IsNaN(lon) || math.IsInf(lon, 0) || math.IsNaN(lat) || math.IsInf(lat, 0) || lon < -180 || lon > 180 || lat < -90 || lat > 90 {
		return "", ErrInvalid
	}
	return "POINT(" + strconv.FormatFloat(lon, 'f', -1, 64) + " " + strconv.FormatFloat(lat, 'f', -1, 64) + ")", nil
}
func (m *Metadata) UnmarshalJSON(b []byte) error {
	var raw struct {
		Embeds    []json.RawMessage `json:"embeds"`
		Thumbnail *string           `json:"thumbnail"`
	}
	if e := json.Unmarshal(b, &raw); e != nil {
		return e
	}
	*m = Metadata{Thumbnail: raw.Thumbnail}
	for _, b := range raw.Embeds {
		var kind struct {
			Type string `json:"type"`
		}
		if e := json.Unmarshal(b, &kind); e != nil {
			return e
		}
		if kind.Type == "link" {
			var v LinkEmbed
			if e := json.Unmarshal(b, &v); e != nil {
				return e
			}
			m.Embeds = append(m.Embeds, v)
		} else {
			var v Embed
			if e := json.Unmarshal(b, &v); e != nil {
				return e
			}
			if e := v.Validate(); e != nil {
				return e
			}
			m.Composed = append(m.Composed, v)
		}
	}
	return nil
}
