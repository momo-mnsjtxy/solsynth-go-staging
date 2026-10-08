// SPDX-License-Identifier: AGPL-3.0-only
package fortunes

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var languagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$`)
var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidLanguage(v string) bool { return len(v) <= 35 && languagePattern.MatchString(v) }
func ValidRevision(v string) bool { return revisionPattern.MatchString(v) }
func ValidateRequest(v Request) error {
	if v.Version != 2 || !authority.ValidID(v.AccountID) || !authority.ValidID(v.CheckInID) || v.IdempotencyKey != "fortune:"+v.CheckInID+":v2" || v.Level < 0 || v.Level > 4 || (v.Language != "" && !ValidLanguage(v.Language)) || len(v.TimeZone) > 128 || v.TimeZone == "Local" {
		return ErrInvalid
	}
	if _, e := time.LoadLocation(v.TimeZone); e != nil {
		return ErrInvalid
	}
	for _, s := range []string{v.Day, v.LocalDate} {
		d, e := time.Parse("2006-01-02", s)
		if e != nil || d.Year() < 1900 || d.Year() > 2199 {
			return ErrInvalid
		}
	}
	return nil
}
func text(v string, max int) bool {
	return utf8.ValidString(v) && strings.TrimSpace(v) != "" && !strings.ContainsRune(v, 0) && len(utf16.Encode([]rune(v))) <= max
}
func normalized(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, v)
}
func ValidateGeneration(v *Generation) error {
	if v.Report.Version != 2 || len(v.Tips) > 4 {
		return ErrInvalid
	}
	r := &v.Report
	for _, f := range []struct {
		p *string
		n int
	}{{&r.Poem, 120}, {&r.Summary, 160}, {&r.SummaryDetail, 360}, {&r.Wish, 120}, {&r.Love, 120}, {&r.Study, 120}, {&r.Career, 120}, {&r.Health, 120}, {&r.LostItem, 120}, {&r.LuckyColor, 40}, {&r.LuckyDirection, 40}, {&r.LuckyTime, 40}, {&r.LuckyItem, 40}, {&r.LuckyAction, 120}, {&r.AvoidAction, 120}, {&r.Ritual, 160}} {
		*f.p = strings.TrimSpace(*f.p)
		if !text(*f.p, f.n) {
			return ErrInvalid
		}
	}
	titles, contents := map[string]bool{}, map[string]bool{}
	for i := range v.Tips {
		t := &v.Tips[i]
		t.Title = strings.TrimSpace(t.Title)
		t.Content = strings.TrimSpace(t.Content)
		a, b := normalized(t.Title), normalized(t.Content)
		if !text(t.Title, 48) || !text(t.Content, 180) || a == "" || b == "" || titles[a] || contents[b] {
			return ErrInvalid
		}
		titles[a] = true
		contents[b] = true
	}
	if v.Tips == nil {
		v.Tips = []Tip{}
	}
	return nil
}
func ValidateCorpus(c *Corpus) error {
	if !ValidRevision(c.Revision) || !text(c.Label, 256) || !text(c.Provenance, 2048) || len(c.Sayings) > 1000 || len(c.Reports) > 1000 || len(c.Sayings)+len(c.Reports) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for i := range c.Sayings {
		v := &c.Sayings[i]
		v.Language = strings.ToLower(v.Language)
		if !ValidLanguage(v.Language) || !text(v.Content, 4096) || !text(v.Source, 512) {
			return ErrInvalid
		}
		key := v.Language + "\x00" + v.Content
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for i := range c.Reports {
		v := &c.Reports[i]
		v.Language = strings.ToLower(v.Language)
		if !ValidRevision(v.ID) || !ValidLanguage(v.Language) || v.Level < 0 || v.Level > 4 || seen[v.ID] {
			return ErrInvalid
		}
		seen[v.ID] = true
		if e := ValidateGeneration(&v.Generation); e != nil {
			return e
		}
	}
	return nil
}
func Decode(b []byte, v any) error {
	if len(b) > 2<<20 || !utf8.Valid(b) || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	if e := unique(d, 0); e != nil {
		return ErrInvalid
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return ErrInvalid
	}
	return nil
}
func unique(d *json.Decoder, depth int) error {
	if depth > 20 {
		return ErrInvalid
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := token.(string)
			if !ok || seen[k] {
				return ErrInvalid
			}
			seen[k] = true
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return ErrInvalid
		}
	case '[':
		for d.More() {
			if e = unique(d, depth+1); e != nil {
				return e
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
