// SPDX-License-Identifier: AGPL-3.0-only
package exchangecatalog

import (
	"math/big"
	"strings"
	"unicode"
)

// Decimal stores a finite nonnegative .NET-compatible decimal without float64.
// Values are constructed by the catalog parser; JSON uses an unquoted exact
// decimal number and retains the representable decimal scale.
type Decimal struct{ text string }

func (d Decimal) String() string {
	if d.text == "" {
		return "0"
	}
	return d.text
}
func (d Decimal) MarshalJSON() ([]byte, error) { return []byte(d.String()), nil }

var decimalMax = func() *big.Int { n := new(big.Int).Lsh(big.NewInt(1), 96); return n.Sub(n, big.NewInt(1)) }()

func parseDecimal(value string) (Decimal, bool) {
	if value == "" {
		return Decimal{}, false
	}
	scale := 0
	point := false
	digits := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '.' && !point {
			point = true
			continue
		}
		if c < '0' || c > '9' {
			return Decimal{}, false
		}
		digits = append(digits, c)
		if point {
			scale++
		}
	}
	if len(digits) == 0 {
		return Decimal{}, false
	}
	coefficient, ok := new(big.Int).SetString(string(digits), 10)
	if !ok {
		return Decimal{}, false
	}
	// Reduce scale only as required by .NET's scale-28/96-bit representation.
	// Each candidate is rounded from the ORIGINAL value, avoiding double rounding.
	reduction := max(0, scale-28)
	for ; reduction <= scale; reduction++ {
		rounded := new(big.Int).Set(coefficient)
		if reduction > 0 {
			divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(reduction)), nil)
			remainder := new(big.Int)
			rounded.QuoRem(coefficient, divisor, remainder)
			comparison := new(big.Int).Lsh(remainder, 1).Cmp(divisor)
			if comparison > 0 || comparison == 0 && rounded.Bit(0) != 0 {
				rounded.Add(rounded, big.NewInt(1))
			}
		}
		if rounded.Cmp(decimalMax) > 0 {
			continue
		}
		if rounded.Sign() == 0 {
			return Decimal{}, false
		} // caller requires amount > 0
		finalScale := scale - reduction
		text := rounded.String()
		if finalScale > 0 {
			if len(text) <= finalScale {
				text = strings.Repeat("0", finalScale-len(text)+1) + text
			}
			cut := len(text) - finalScale
			text = text[:cut] + "." + text[cut:]
		}
		return Decimal{text: text}, true
	}
	return Decimal{}, false
}

func trimDotNet(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// Parse the caller's numeric-prefix grammar, not a generic currency or exponent
// parser. Digits are examined as UTF-16 chars; supplementary digits therefore
// stop the prefix just as the source's char.IsDigit(char) loop does.
func parseCurrencyAmount(s *string) (Decimal, string, bool) {
	if s == nil {
		return Decimal{}, "", false
	}
	value := trimDotNet(*s)
	split := 0
	for i, r := range value {
		if r == '.' || r <= 0xffff && unicode.IsDigit(r) {
			split = i + len(string(r))
			continue
		}
		break
	}
	if split == 0 || split == len(value) {
		return Decimal{}, "", false
	}
	amount, ok := parseDecimal(value[:split])
	if !ok {
		return Decimal{}, "", false
	}
	currency := trimDotNet(value[split:])
	if currency == "" {
		return Decimal{}, "", false
	}
	return amount, currency, true
}
