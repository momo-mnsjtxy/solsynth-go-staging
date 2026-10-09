// SPDX-License-Identifier: AGPL-3.0-only
package surveys

import (
	"bytes"
	"encoding/json"
	"math"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"strings"
	"unicode/utf8"
)

func textValid(s string, n int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && taxonomy.UTF16Length(s) <= n
}
func Validate(s Survey) error {
	if s.Title != nil && !textValid(*s.Title, 1024) || s.Description != nil && !textValid(*s.Description, 4096) || len(s.Questions) == 0 || len(s.Questions) > 100 {
		return ErrValidation
	}
	if s.EndedAt != nil && s.EndedAt.IsZero() {
		return ErrValidation
	}
	seen := map[string]bool{}
	for _, q := range s.Questions {
		if !authority.ValidID(q.ID) || seen[q.ID] || !textValid(q.Title, 1024) || strings.TrimSpace(q.Title) == "" || q.Description != nil && !textValid(*q.Description, 4096) {
			return ErrValidation
		}
		seen[q.ID] = true
		switch q.Type {
		case SingleChoice, MultipleChoice:
			if len(q.Options) < 2 || len(q.Options) > 100 || q.MaxSelections != nil && (*q.MaxSelections < 1 || *q.MaxSelections > len(q.Options) || q.Type == SingleChoice && *q.MaxSelections != 1) {
				return ErrValidation
			}
			opts := map[string]bool{}
			for _, o := range q.Options {
				if !authority.ValidID(o.ID) || opts[o.ID] || !textValid(o.Label, 1024) || strings.TrimSpace(o.Label) == "" || o.Description != nil && !textValid(*o.Description, 4096) {
					return ErrValidation
				}
				opts[o.ID] = true
			}
		case FreeText:
			if q.MaxLength != nil && (*q.MaxLength <= 0 || *q.MaxLength > 16384) {
				return ErrValidation
			}
		case Rating:
			if q.MinValue != nil && (math.IsNaN(*q.MinValue) || math.IsInf(*q.MinValue, 0)) || q.MaxValue != nil && (math.IsNaN(*q.MaxValue) || math.IsInf(*q.MaxValue, 0)) || q.MinValue != nil && q.MaxValue != nil && *q.MinValue >= *q.MaxValue {
				return ErrValidation
			}
		case YesNo:
		default:
			return ErrValidation
		}
		if q.Type != SingleChoice && q.Type != MultipleChoice && (len(q.Options) > 0 || q.MaxSelections != nil) || q.Type != FreeText && q.MaxLength != nil || q.Type != Rating && (q.MinValue != nil || q.MaxValue != nil) {
			return ErrValidation
		}
	}
	return nil
}
func ValidateAnswer(s Survey, a map[string]json.RawMessage) error {
	if a == nil || len(a) > len(s.Questions) {
		return ErrValidation
	}
	known := map[string]bool{}
	for _, q := range s.Questions {
		known[q.ID] = true
		v, ok := a[q.ID]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			if q.Required {
				return ErrValidation
			}
			continue
		}
		if !json.Valid(v) {
			return ErrValidation
		}
		options := map[string]bool{}
		for _, o := range q.Options {
			options[o.ID] = true
		}
		switch q.Type {
		case SingleChoice:
			var x string
			if json.Unmarshal(v, &x) != nil || !options[x] {
				return ErrValidation
			}
		case MultipleChoice:
			var x []string
			if json.Unmarshal(v, &x) != nil || q.Required && len(x) == 0 || q.MaxSelections != nil && len(x) > *q.MaxSelections || len(x) > len(q.Options) {
				return ErrValidation
			}
			seen := map[string]bool{}
			for _, id := range x {
				if !options[id] || seen[id] {
					return ErrValidation
				}
				seen[id] = true
			}
		case FreeText:
			var x string
			limit := 16384
			if q.MaxLength != nil {
				limit = *q.MaxLength
			}
			if json.Unmarshal(v, &x) != nil || !textValid(x, limit) || q.Required && strings.TrimSpace(x) == "" {
				return ErrValidation
			}
		case Rating:
			var x float64
			if json.Unmarshal(v, &x) != nil || math.IsNaN(x) || math.IsInf(x, 0) || q.MinValue != nil && x < *q.MinValue || q.MaxValue != nil && x > *q.MaxValue {
				return ErrValidation
			}
		case YesNo:
			var x bool
			if json.Unmarshal(v, &x) != nil {
				return ErrValidation
			}
		default:
			return ErrValidation
		}
	}
	for k := range a {
		if !known[k] {
			return ErrValidation
		}
	}
	return nil
}
