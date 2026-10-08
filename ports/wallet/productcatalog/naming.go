// Portions translated from dotnet/runtime JsonSeparatorNamingPolicy.cs v10.0.0.
// Copyright (c) .NET Foundation and Contributors. Licensed under the MIT license.
// See source/LICENSE-MIT.txt. Modified 2026-10-08.
package productcatalog

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// naming-bmp.json is independently emitted by the retained .NET framework
// reference harness, not copied from an excluded model/schema implementation.
//
//go:embed naming-bmp.json
var namingData []byte

type namingCharacter struct {
	Code     rune   `json:"code"`
	Category string `json:"category"`
	Lower    rune   `json:"lower"`
}

var namingCharacters = func() map[rune]namingCharacter {
	var rows []namingCharacter
	if err := json.Unmarshal(namingData, &rows); err != nil {
		panic("invalid compiled naming evidence")
	}
	result := make(map[rune]namingCharacter, len(rows))
	for _, row := range rows {
		result[row.Code] = row
	}
	return result
}()

// snakeCase follows the source UTF-16-char state machine. Supplementary Unicode
// scalars pass through/reset state, matching two uncased source surrogate chars.
// BMP category/lowercase behavior is frozen to independently executed .NET data.
func snakeCase(name string) string {
	const (
		notStarted = iota
		upper
		lowerOrDigit
		space
	)
	state := notStarted
	chars := []rune(name)
	var out strings.Builder
	for i, current := range chars {
		classification := namingCharacters[current]
		switch classification.Category {
		case "Lu":
			if state == lowerOrDigit || state == space || state == upper && i+1 < len(chars) && namingCharacters[chars[i+1]].Category == "Ll" {
				out.WriteByte('_')
			}
			out.WriteRune(classification.Lower)
			state = upper
		case "Ll", "Nd":
			if state == space {
				out.WriteByte('_')
			}
			out.WriteRune(current)
			state = lowerOrDigit
		case "Zs":
			if state != notStarted {
				state = space
			}
		default:
			out.WriteRune(current)
			state = notStarted
		}
	}
	return out.String()
}
