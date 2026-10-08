// SPDX-License-Identifier: AGPL-3.0-only
package ipcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"strings"
	"unicode/utf8"
)

const MaxDatasetBytes = 32 << 20

// Dataset is operator-supplied geographic data; no upstream corpus is bundled.
type Dataset struct {
	Revision   string  `json:"revision"`
	Provenance string  `json:"provenance"`
	Entries    []Entry `json:"entries"`
}
type Entry struct {
	Prefix string `json:"prefix"`
	Geo    Geo    `json:"geo"`
}
type CIDRProvider struct{ records map[netip.Prefix]Geo }

func DecodeDataset(r io.Reader) (*CIDRProvider, error) {
	raw, e := io.ReadAll(io.LimitReader(r, MaxDatasetBytes+1))
	if e != nil || len(raw) > MaxDatasetBytes {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var data Dataset
	if d.Decode(&data) != nil {
		return nil, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || d.InputOffset() > MaxDatasetBytes {
		return nil, ErrInvalid
	}
	return NewDataset(data)
}
func NewDataset(data Dataset) (*CIDRProvider, error) {
	if !text(data.Revision, 128) || !text(data.Provenance, 4096) || len(data.Entries) == 0 || len(data.Entries) > 100000 {
		return nil, ErrInvalid
	}
	p := &CIDRProvider{records: map[netip.Prefix]Geo{}}
	for _, entry := range data.Entries {
		prefix, e := netip.ParsePrefix(entry.Prefix)
		if e != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() {
			return nil, ErrInvalid
		}
		if _, ok := p.records[prefix]; ok {
			return nil, ErrInvalid
		}
		g := entry.Geo
		if !validGeo(g) {
			return nil, ErrInvalid
		}
		p.records[prefix] = clone(g)
	}
	return p, nil
}
func text(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, func(c rune) bool { return c < 32 || c == 127 }) < 0
}
func clone(g Geo) Geo {
	pointers := []**string{&g.City, &g.Country, &g.CountryCode, &g.Subdivision, &g.SubdivisionCode, &g.ContinentCode, &g.TimeZone}
	for _, p := range pointers {
		if *p != nil {
			v := **p
			*p = &v
		}
	}
	if g.Latitude != nil {
		v := *g.Latitude
		g.Latitude = &v
	}
	if g.Longitude != nil {
		v := *g.Longitude
		g.Longitude = &v
	}
	return g
}
func (p *CIDRProvider) Lookup(ctx context.Context, a netip.Addr) (*Geo, error) {
	if ctx.Err() != nil || p == nil || !a.IsValid() || a.Zone() != "" {
		return nil, ErrUnavailable
	}
	a = a.Unmap()
	for bits := a.BitLen(); bits >= 0; bits-- {
		if g, ok := p.records[netip.PrefixFrom(a, bits).Masked()]; ok {
			v := clone(g)
			return &v, nil
		}
	}
	return nil, nil
}

func validGeo(g Geo) bool {
	for _, v := range []*string{g.City, g.Country, g.Subdivision, g.SubdivisionCode, g.CountryCode, g.ContinentCode, g.TimeZone} {
		if v != nil && !text(*v, 256) {
			return false
		}
	}
	if g.Latitude != nil && (math.IsNaN(*g.Latitude) || math.IsInf(*g.Latitude, 0) || *g.Latitude < -90 || *g.Latitude > 90) {
		return false
	}
	if g.Longitude != nil && (math.IsNaN(*g.Longitude) || math.IsInf(*g.Longitude, 0) || *g.Longitude < -180 || *g.Longitude > 180) {
		return false
	}
	return true
}
