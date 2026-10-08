// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func Text(v string, max int) bool {
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
		return false
	}
	for _, c := range v {
		if c == 0 || c < 32 && c != '\n' && c != '\r' && c != '\t' || c == 127 {
			return false
		}
	}
	return true
}
func ValidatePatch(p Patch) error {
	for _, v := range []struct {
		s *string
		n int
	}{{p.Title, 256}, {p.Description, 4096}, {p.Slug, 128}} {
		if v.s != nil && !Text(*v.s, v.n) {
			return ErrInvalid
		}
	}
	if p.Slug != nil {
		for _, c := range *p.Slug {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return ErrInvalid
			}
		}
	}
	if p.Type != nil && (*p.Type < Regular || *p.Type > Interactive) || p.Visibility != nil && (*p.Visibility < Public || *p.Visibility > Private) {
		return ErrInvalid
	}
	if p.Metadata != nil {
		b, e := json.Marshal(p.Metadata)
		if e != nil || len(b) > 16384 || len(p.Metadata) > 64 {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateStart(p StartRequest) error {
	if p.ParticipantName != nil && (!Text(*p.ParticipantName, 128) || strings.TrimSpace(*p.ParticipantName) == "") {
		return ErrInvalid
	}
	if p.NoIngress != nil && *p.NoIngress && (p.UseWHIP != nil || p.EnableTranscoding != nil) {
		return ErrInvalid
	}
	if (p.UseWHIP == nil || !*p.UseWHIP) && p.EnableTranscoding != nil && !*p.EnableTranscoding {
		return ErrInvalid
	}
	return nil
}
