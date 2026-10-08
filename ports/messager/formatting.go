// SPDX-License-Identifier: AGPL-3.0-only
// Adapted from Solsynth/DysonNetwork ChatService.cs, revision
// 4ee2d0cebb9da8284a92483fee6dce795bf6947c. Modified 2026-10-07.

package messager

import (
	"strings"
	"unicode/utf8"
)

// FormatMessageContent applies the source's display-heading policy only when
// encryption metadata is absent. The caller must derive hasEncryptionMeta from
// the actual message, not from whether the metadata's contents look empty.
// This is a formatting helper, not message hydration or an HTML sanitizer.
func FormatMessageContent(content *string, hasEncryptionMeta bool) *string {
	if hasEncryptionMeta {
		return content
	}
	return EscapeMarkdownHeadingMarkers(content)
}

// EscapeMarkdownHeadingMarkers preserves nil and empty strings and escapes
// heading markers outside the source's fenced-code state machine. It preserves
// line endings and all other bytes. Inputs are expected to be valid UTF-8;
// invalid bytes are retained, but cannot represent C# unpaired UTF-16 surrogates.
// The input is never modified; unchanged results may alias the input pointer.
func EscapeMarkdownHeadingMarkers(content *string) *string {
	if content == nil || *content == "" {
		return content
	}
	lines := strings.Split(*content, "\n")
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, messageFormattingWhitespace)
		if len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			run := 0
			for run < len(trimmed) && trimmed[run] == trimmed[0] {
				run++
			}
			if run >= 3 {
				if fence == "" {
					fence = trimmed[:run]
				} else if strings.HasPrefix(trimmed, fence) && strings.TrimFunc(trimmed[len(fence):], messageFormattingWhitespace) == "" {
					fence = ""
				}
			}
			continue
		}
		if fence != "" {
			continue
		}
		index := 0
		for index < len(line) && index < 3 && line[index] == ' ' {
			index++
		}
		hashStart := index
		for index < len(line) && line[index] == '#' && index-hashStart < 6 {
			index++
		}
		if index == hashStart {
			continue
		}
		atEnd := index == len(line)
		next, _ := utf8.DecodeRuneInString(line[index:])
		if atEnd || messageFormattingWhitespace(next) {
			lines[i] = line[:hashStart] + "\\" + line[hashStart:]
		}
	}
	result := strings.Join(lines, "\n")
	return &result
}

// C# char.IsWhiteSpace / Trim whitespace consists of these BMP characters.
// Explicitly list the set to avoid tying the policy to Go's Unicode version.
func messageFormattingWhitespace(r rune) bool {
	return r >= '\t' && r <= '\r' || r == ' ' || r == '\u0085' ||
		r == '\u00a0' || r == '\u1680' || r >= '\u2000' && r <= '\u200a' ||
		r == '\u2028' || r == '\u2029' || r == '\u202f' ||
		r == '\u205f' || r == '\u3000'
}
