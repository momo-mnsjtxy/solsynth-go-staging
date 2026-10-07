// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"strings"
	"testing"
)

func TestHeadingFormattingCases(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"empty", "", ""},
		{"plain", "hello 世界 😀", "hello 世界 😀"},
		{"heading", "# title", "\\# title"},
		{"empty heading", "###", "\\###"},
		{"six", "###### title", "\\###### title"},
		{"seven", "####### title", "####### title"},
		{"no separator", "#title", "#title"},
		{"nonascii separator", "#中", "#中"},
		{"supplementary separator", "#😀", "#😀"},
		{"three spaces", "   ## title", "   \\## title"},
		{"four spaces", "    ## title", "    ## title"},
		{"tab indent", "\t# title", "\t# title"},
		{"unicode indent", "\u00a0# title", "\u00a0# title"},
		{"already escaped", "\\# title", "\\# title"},
		{"inline hash", "hello # title", "hello # title"},
		{"crlf and trailing lf", "# a\r\n## b\n", "\\# a\r\n\\## b\n"},
		{"lone cr is not line break", "text\r# title", "text\r# title"},
		{"unicode line separator is not line break", "text\u2028# title", "text\u2028# title"},
		{"backtick fence", "```go\n# code\n```\n# title", "```go\n# code\n```\n\\# title"},
		{"tilde fence", "~~~\n# code\n~~~\n# title", "~~~\n# code\n~~~\n\\# title"},
		{"unicode fence indentation", "\u3000```\n# code\n\t```\u00a0\r\n# title", "\u3000```\n# code\n\t```\u00a0\r\n\\# title"},
		{"longer fence does not close", "```\n````\n# code\n```\n# title", "```\n````\n# code\n```\n\\# title"},
		{"shorter fence does not close", "````\n```\n# code\n````\n# title", "````\n```\n# code\n````\n\\# title"},
		{"different marker does not close", "```\n~~~\n# code\n```\n# title", "```\n~~~\n# code\n```\n\\# title"},
		{"closing info does not close", "~~~\n~~~info\n# code\n~~~\n# title", "~~~\n~~~info\n# code\n~~~\n\\# title"},
		{"short marker does not open", "``\n# title\n~~\n## title", "``\n\\# title\n~~\n\\## title"},
		{"unclosed fence", "```\n# code\n", "```\n# code\n"},
		{"deeply indented fence still opens", "    ```\n# code\n```\n# title", "    ```\n# code\n```\n\\# title"},
		{"zero width space not fence indentation", "\u200b```\n# title", "\u200b```\n\\# title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.input
			got := EscapeMarkdownHeadingMarkers(&input)
			if got == nil || *got != tc.want {
				t.Fatalf("got %v, want %q", got, tc.want)
			}
			if input != tc.input {
				t.Fatal("mutated input")
			}
			if twice := EscapeMarkdownHeadingMarkers(got); *twice != *got {
				t.Fatal("formatting is not idempotent")
			}
		})
	}
}

func TestHeadingUnicodeWhitespace(t *testing.T) {
	// Fixed expected code points, independent of the implementation predicate.
	for _, r := range "\t\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000" {
		input := "#" + string(r) + "title"
		if got := EscapeMarkdownHeadingMarkers(&input); *got != "\\"+input {
			t.Errorf("U+%04X should separate a heading: %q", r, *got)
		}
	}
	for _, r := range "\u001c\u001d\u001e\u001f\u180e\u200b\ufeff\ufffd" {
		input := "#" + string(r) + "title"
		if got := EscapeMarkdownHeadingMarkers(&input); *got != input {
			t.Errorf("U+%04X is not whitespace: %q", r, *got)
		}
	}
}

func TestHeadingNilAndEncryptionBypass(t *testing.T) {
	if EscapeMarkdownHeadingMarkers(nil) != nil || FormatMessageContent(nil, false) != nil || FormatMessageContent(nil, true) != nil {
		t.Fatal("nil must be preserved")
	}
	for _, input := range []string{"", "# plaintext", "```\n# ciphertext", "#\xff\n# text"} {
		if got := FormatMessageContent(&input, true); got == nil || *got != input {
			t.Fatal("encrypted content changed")
		}
	}
	input := "# plaintext"
	if got := FormatMessageContent(&input, false); *got != "\\# plaintext" {
		t.Fatal("unencrypted content was not escaped")
	}
	input = "#\xff\n# text"
	if got := EscapeMarkdownHeadingMarkers(&input); *got != "#\xff\n\\# text" {
		t.Fatal("invalid UTF-8 bytes were discarded")
	}
}

func TestHeadingLongInput(t *testing.T) {
	input := strings.Repeat("# hello 世界\r\n", 10000)
	want := strings.Repeat("\\# hello 世界\r\n", 10000)
	if got := EscapeMarkdownHeadingMarkers(&input); *got != want {
		t.Fatal("long multiline content mismatch")
	}
}
