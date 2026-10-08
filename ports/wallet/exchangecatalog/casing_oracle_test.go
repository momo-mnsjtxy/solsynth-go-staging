// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"encoding/json"
	"os"
	"testing"
)

func TestEveryUnicodeScalarAgainstDotNetOrdinalOracle(t *testing.T) {
	data, err := os.ReadFile("../../../tests/contract/exchange-oracle/unicode-map.json")
	if err != nil {
		t.Fatal(err)
	}
	var sparse [][2]rune
	if err = json.Unmarshal(data, &sparse); err != nil {
		t.Fatal(err)
	}
	if len(sparse) < 1000 {
		t.Fatal("missing or incomplete ordinal oracle")
	}
	expected := make(map[rune]rune, len(sparse))
	for _, pair := range sparse {
		expected[pair[0]] = pair[1]
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		want, ok := expected[r]
		if !ok {
			want = r
		}
		if got := ordinalUpper(r); got != want {
			t.Fatalf("U+%04X Go U+%04X .NET U+%04X", r, got, want)
		}
	}
}
