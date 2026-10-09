// SPDX-License-Identifier: AGPL-3.0-only
package taxonomy

import (
	"encoding/json"
	"os"
	"testing"
	"unicode"
)

func TestEveryUnicodeScalarAgainstInvariantOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/invariant-casing.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Lower      [][2]rune
		Whitespace []rune
	}
	if err = json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Lower) < 1000 || len(oracle.Whitespace) != 25 {
		t.Fatal("incomplete casing fixture")
	}
	expected := map[rune]rune{}
	spaces := map[rune]bool{}
	for _, p := range oracle.Lower {
		expected[p[0]] = p[1]
	}
	for _, r := range oracle.Whitespace {
		spaces[r] = true
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		want, ok := expected[r]
		if !ok {
			want = r
		}
		if got := invariantLowerRune(r); got != want {
			t.Fatalf("U+%04X got U+%04X want U+%04X", r, got, want)
		}
		if unicode.IsSpace(r) != spaces[r] {
			t.Fatalf("trim mismatch U+%04X", r)
		}
	}
	if NormalizeSlug(" \u0130 I \u00a0") != "\u0130 i" {
		t.Fatal("dotted-I normalization")
	}
}
