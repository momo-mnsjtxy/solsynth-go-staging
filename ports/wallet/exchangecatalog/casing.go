// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"strconv"
	"strings"
	"unicode"
)

// .NET's ICU ordinal casing uses simple uppercase, deliberately preserving
// dotless i and long s. Supplementary characters are compared by scalar value
// after casing, rather than naively sorting raw UTF-16 code units.
// Unicode-version compatibility is checked by the independent .NET oracle.
func ordinalUpper(r rune) rune {
	if r == '\u0131' || r == '\u017f' {
		return r
	}
	// Unicode 16 mappings missing from Go's Unicode 15 tables, verified against
	// every scalar in the independent .NET 10.0.12 ordinal-casing oracle.
	switch r {
	case 0x019b:
		return 0xa7dc
	case 0x0264:
		return 0xa7cb
	case 0x1c8a:
		return 0x1c89
	case 0xa7cd:
		return 0xa7cc
	case 0xa7db:
		return 0xa7da
	}
	if r >= 0x10d70 && r <= 0x10d85 {
		return r - 0x20
	}
	return unicode.ToUpper(r)
}
func ordinalKey(s string) string { return strings.Map(ordinalUpper, s) }
func ordinalCompare(a, b string) int {
	aa, bb := []rune(a), []rune(b)
	for i := 0; i < min(len(aa), len(bb)); i++ {
		x, y := ordinalUpper(aa[i]), ordinalUpper(bb[i])
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	if len(aa) < len(bb) {
		return -1
	}
	if len(aa) > len(bb) {
		return 1
	}
	return 0
}
func equalOrdinal(a, b string) bool { return ordinalCompare(a, b) == 0 }

func configKeyCompare(a, b string) int {
	// Child keys have no ':'; numeric keys sort before nonnumeric keys. A valid
	// currency-exchange source key always contains a nonnumeric currency suffix.
	x, xe := strconv.ParseInt(strings.Trim(a, " \t\r\n\v\f"), 10, 32)
	y, ye := strconv.ParseInt(strings.Trim(b, " \t\r\n\v\f"), 10, 32)
	if xe == nil && ye == nil {
		return int(int32(x - y))
	} // framework int subtraction
	if xe == nil {
		return -1
	}
	if ye == nil {
		return 1
	}
	return ordinalCompare(a, b)
}
