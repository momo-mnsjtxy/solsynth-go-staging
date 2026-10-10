// SPDX-License-Identifier: AGPL-3.0-only
package publishernotificationpg

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipUTF8PreservesBoundaryAndByteLimit(t *testing.T) {
	value := strings.Repeat("🙂", 256) + "tail"
	for _, limit := range []int{0, 2, 3, 4, 5, 31, 512, 1024} {
		got := clipUTF8(value, limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("limit %d produced %d invalid bytes", limit, len(got))
		}
	}
	if got := clipUTF8("plain", 5); got != "plain" {
		t.Fatal(got)
	}
}
