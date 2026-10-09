// SPDX-License-Identifier: AGPL-3.0-only
// Exact decimal binder candidate, independently checked against .NET fixtures.
package productcatalog

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var decimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
var decimalMaximum = func() *big.Int { n := new(big.Int).Lsh(big.NewInt(1), 96); return n.Sub(n, big.NewInt(1)) }()

func decimalText(value string) (string, bool) {
	value = strings.TrimFunc(value, unicode.IsSpace)
	if !decimalPattern.MatchString(value) {
		return "", false
	}
	negative := strings.HasPrefix(value, "-")
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	exponent := int64(0)
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.ParseInt(value[i+1:], 10, 64)
		if err != nil {
			// Decimal parsing saturates oversized exponents; syntax was already
			// checked. This preserves zero and underflow without huge powers.
			exponent = 1_000_000_000
			if strings.HasPrefix(value[i+1:], "-") {
				exponent = -exponent
			}
		}
		exponent = min(max(exponent, -1_000_000_000), 1_000_000_000)
		value = value[:i]
	}
	scale := int64(0)
	if i := strings.IndexByte(value, '.'); i >= 0 {
		scale = int64(len(value) - i - 1)
		value = value[:i] + value[i+1:]
	}
	scale -= exponent
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return "", false
	}
	if n.Sign() == 0 {
		return formatDecimal(n, int(min(max(scale, 0), 28)), false), true
	}
	if scale < 0 {
		if -scale > 29 {
			return "", false
		}
		n.Mul(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(-scale), nil))
		scale = 0
	}
	if scale > int64(len(n.String())+29) {
		return formatDecimal(new(big.Int), 28, false), true
	}
	for reduction := max(int64(0), scale-28); reduction <= scale; reduction++ {
		rounded := new(big.Int).Set(n)
		if reduction > 0 {
			divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(reduction), nil)
			remainder := new(big.Int)
			rounded.QuoRem(n, divisor, remainder)
			cmp := new(big.Int).Lsh(remainder, 1).Cmp(divisor)
			if cmp > 0 || cmp == 0 && rounded.Bit(0) != 0 {
				rounded.Add(rounded, big.NewInt(1))
			}
		}
		if rounded.Cmp(decimalMaximum) > 0 {
			continue
		}
		return formatDecimal(rounded, int(scale-reduction), negative), true
	}
	return "", false
}

func formatDecimal(n *big.Int, scale int, negative bool) string {
	text := n.String()
	if scale > 0 {
		if len(text) <= scale {
			text = strings.Repeat("0", scale-len(text)+1) + text
		}
		at := len(text) - scale
		text = text[:at] + "." + text[at:]
	}
	if negative && n.Sign() != 0 {
		text = "-" + text
	}
	return text
}
