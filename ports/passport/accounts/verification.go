// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Numeric indices 0..6 are evidenced by the licensed authored Solian renderer.
// Semantic enum-name mappings are deliberately not invented from icon choices.
type VerificationMark struct {
	Type        int     `json:"type"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	VerifiedBy  *string `json:"verified_by"`
}

func ValidateVerification(v *VerificationMark) error {
	if v == nil {
		return nil
	}
	if v.Type < 0 || v.Type > 6 {
		return ErrInvalid
	}
	for _, x := range []struct {
		v *string
		n int
	}{{v.Title, 1024}, {v.Description, 8192}, {v.VerifiedBy, 1024}} {
		if x.v != nil {
			if !utf8.ValidString(*x.v) || strings.ContainsRune(*x.v, 0) {
				return ErrInvalid
			}
			n := 0
			for _, r := range *x.v {
				n++
				if r > 0xffff {
					n++
				}
			}
			if n > x.n {
				return ErrInvalid
			}
		}
	}
	return nil
}
func DecodeVerification(raw []byte) (*VerificationMark, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v VerificationMark
	if e := json.Unmarshal(raw, &v); e != nil {
		return nil, e
	}
	if e := ValidateVerification(&v); e != nil {
		return nil, e
	}
	return &v, nil
}
