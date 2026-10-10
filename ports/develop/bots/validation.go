// SPDX-License-Identifier: AGPL-3.0-only
package bots

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop"
	"strings"
	"time"
	"unicode/utf8"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{2,256}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var commandPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var pluginPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)+$`)

func bounded(s *string, max int) bool {
	return s == nil || utf8.ValidString(*s) && len([]rune(*s)) <= max && !strings.ContainsRune(*s, 0)
}
func ValidatePatch(p Patch, create bool) error {
	if create && (p.Name == nil || p.Nick == nil || p.Slug == nil) {
		return ErrInvalid
	}
	if p.Name != nil && !namePattern.MatchString(*p.Name) || p.Nick != nil && strings.TrimSpace(*p.Nick) == "" || p.Slug != nil && !keyPattern.MatchString(*p.Slug) {
		return ErrInvalid
	}
	for _, v := range []struct {
		s *string
		n int
	}{{p.Nick, 256}, {p.Language, 128}, {p.FirstName, 256}, {p.MiddleName, 256}, {p.LastName, 256}, {p.Gender, 1024}, {p.Pronouns, 128}, {p.TimeZone, 128}, {p.Location, 256}, {p.Bio, 4096}} {
		if !bounded(v.s, v.n) {
			return ErrInvalid
		}
	}
	if p.TimeZone != nil && *p.TimeZone != "" {
		if _, e := time.LoadLocation(*p.TimeZone); e != nil {
			return ErrInvalid
		}
	}
	if p.Birthday != nil && (p.Birthday.Year() < 1900 || p.Birthday.Year() > 2200) {
		return ErrInvalid
	}
	if p.PictureID != nil && !authority.ValidID(*p.PictureID) || p.BackgroundID != nil && !authority.ValidID(*p.BackgroundID) {
		return ErrInvalid
	}
	return nil
}
func ValidateCommands(commands []develop.BotCommand) error {
	if len(commands) > 100 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range commands {
		if !commandPattern.MatchString(c.Name) || seen[c.Name] || strings.TrimSpace(c.Description) == "" || !bounded(&c.Description, 1024) || !bounded(c.Usage, 2048) || len(c.Parameters) > 32 {
			return ErrInvalid
		}
		seen[c.Name] = true
		params := map[string]bool{}
		for _, p := range c.Parameters {
			if !commandPattern.MatchString(p.Name) || params[p.Name] || !bounded(p.Description, 1024) || !bounded(p.Type, 64) {
				return ErrInvalid
			}
			params[p.Name] = true
		}
	}
	raw, e := json.Marshal(commands)
	if e != nil || len(raw) > 65536 {
		return ErrInvalid
	}
	return nil
}
func ValidateManifestPatch(p ManifestPatch) error {
	if p.Commands != nil {
		if e := ValidateCommands(*p.Commands); e != nil {
			return e
		}
	}
	if p.SubscribedEvents != nil {
		if len(*p.SubscribedEvents) > 32 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, v := range *p.SubscribedEvents {
			if !keyPattern.MatchString(v) || seen[v] {
				return ErrInvalid
			}
			seen[v] = true
		}
	}
	return nil
}
func httpsURL(v *string) bool {
	if v == nil || *v == "" {
		return true
	}
	u, e := url.Parse(*v)
	return e == nil && len(*v) <= 2048 && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(*v, "\r\n")
}
func ValidateMini(p MiniPatch, create bool) error {
	if create && p.Slug == nil || p.Slug != nil && !namePattern.MatchString(*p.Slug) || p.Stage != nil && (*p.Stage < 0 || *p.Stage > 2) {
		return ErrInvalid
	}
	for _, id := range []*string{p.IconID, p.BackgroundID} {
		if id != nil && strings.TrimSpace(*id) != "" && !authority.ValidID(*id) {
			return ErrInvalid
		}
	}
	if p.Manifest == nil {
		return nil
	}
	m := p.Manifest
	if len(m.ID) > 256 || !pluginPattern.MatchString(m.ID) || strings.TrimSpace(m.Name) == "" || !bounded(&m.Name, 256) || len(m.Version) > 32 || strings.TrimSpace(m.Version) == "" || !bounded(m.Author, 256) || !bounded(m.Description, 4096) || !httpsURL(m.Homepage) || !httpsURL(m.EntryURL) || len(m.Entry) > 256 || m.Entry == "" || path.Clean(m.Entry) != m.Entry || strings.HasPrefix(m.Entry, "/") || strings.ContainsAny(m.Entry, "\\:\x00?#") || strings.HasPrefix(m.Entry, "../") || m.Entry == ".." || len(m.Permissions) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range m.Permissions {
		if !keyPattern.MatchString(p) || seen[p] {
			return ErrInvalid
		}
		seen[p] = true
	}
	raw, e := json.Marshal(m)
	if e != nil || len(raw) > 16384 {
		return ErrInvalid
	}
	return nil
}

// DecodePackageManifest accepts the authored archive's case-insensitive property
// convention plus the wire entry_url spelling, while rejecting alias collisions.
func DecodePackageManifest(raw []byte) (MiniManifest, error) {
	var out MiniManifest
	if len(raw) > 16384 {
		return out, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d, 0) != nil {
		return out, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return out, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return out, ErrInvalid
	}
	normalized := map[string]json.RawMessage{}
	for key, v := range fields {
		name := strings.ToLower(key)
		if name == "entryurl" {
			name = "entry_url"
		}
		switch name {
		case "id", "name", "version", "author", "description", "entry", "permissions", "background", "icon", "homepage", "entry_url":
		default:
			return out, ErrInvalid
		}
		if _, ok := normalized[name]; ok {
			return out, ErrInvalid
		}
		normalized[name] = v
	}
	data, _ := json.Marshal(normalized)
	if json.Unmarshal(data, &out) != nil {
		return out, ErrInvalid
	}
	if e := ValidateMini(MiniPatch{Manifest: &out}, false); e != nil {
		return out, e
	}
	return out, nil
}
