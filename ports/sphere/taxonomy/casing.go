// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"strings"
	"unicode"
)

// LowerInvariant matches the retained .NET 10 SDK 10.0.401 scalar oracle.
// Deployment runtime/ICU and PostgreSQL collation still need compatibility
// review. In particular Go's default dotted-I mapping differs from .NET.
func LowerInvariant(value string) string { return strings.Map(invariantLowerRune, value) }
func invariantLowerRune(r rune) rune {
	switch r {
	case 0x0130:
		return r
	case 0x1C89:
		return 0x1C8A
	case 0xA7CB:
		return 0x0264
	case 0xA7CC:
		return 0xA7CD
	case 0xA7DA:
		return 0xA7DB
	case 0xA7DC:
		return 0x019B
	}
	if r >= 0x10D50 && r <= 0x10D65 {
		return r + 0x20
	}
	return unicode.ToLower(r)
}
