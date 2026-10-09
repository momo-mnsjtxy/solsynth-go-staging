// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Amount is immutable, exact base-10 thousandths. Its zero value is zero.
// The transfer policy truncates toward zero, matching the owned service. No
// float conversion is used. Excess precision is never rounded by PostgreSQL.
type Amount struct{ value string }

var decimalSyntax = regexp.MustCompile(`^(-?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)
var maxUnits, _ = new(big.Int).SetString("79228162514264337593543950335000", 10)

func ParseAmount(s string) (Amount, error) {
	if len(s) > 128 {
		return Amount{}, ErrAmount
	}
	m := decimalSyntax.FindStringSubmatch(s)
	if m == nil {
		return Amount{}, ErrAmount
	}
	exponent := 0
	if m[4] != "" {
		v, e := strconv.Atoi(m[4])
		if e != nil || v < -64 || v > 64 {
			return Amount{}, ErrAmount
		}
		exponent = v
	}
	digits := strings.TrimLeft(m[2]+m[3], "0")
	if digits == "" {
		return Amount{}, nil
	}
	shift := 3 + exponent - len(m[3])
	if shift >= 0 {
		if len(digits)+shift > 32 {
			return Amount{}, ErrAmount
		}
		digits += strings.Repeat("0", shift)
	} else if -shift >= len(digits) {
		digits = "0"
	} else {
		digits = digits[:len(digits)+shift]
	}
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok || n.Cmp(maxUnits) > 0 {
		return Amount{}, ErrAmount
	}
	if m[1] == "-" {
		n.Neg(n)
	}
	return fromUnits(n), nil
}
func fromUnits(n *big.Int) Amount {
	if n.Sign() == 0 {
		return Amount{}
	}
	return Amount{value: n.String()}
}
func (a Amount) units() *big.Int {
	n := new(big.Int)
	if a.value != "" {
		n.SetString(a.value, 10)
	}
	return n
}
func (a Amount) String() string {
	n := a.units()
	negative := n.Sign() < 0
	n.Abs(n)
	s := n.String()
	if len(s) < 4 {
		s = strings.Repeat("0", 4-len(s)) + s
	}
	s = s[:len(s)-3] + "." + s[len(s)-3:]
	if negative {
		s = "-" + s
	}
	return s
}
func (a Amount) Sign() int        { return a.units().Sign() }
func (a Amount) Cmp(b Amount) int { return a.units().Cmp(b.units()) }
func (a Amount) Add(b Amount) (Amount, error) {
	n := new(big.Int).Add(a.units(), b.units())
	if new(big.Int).Abs(new(big.Int).Set(n)).Cmp(maxUnits) > 0 {
		return Amount{}, ErrAmount
	}
	return fromUnits(n), nil
}
func (a Amount) Sub(b Amount) (Amount, error) { return a.Add(fromUnits(new(big.Int).Neg(b.units()))) }
func (a Amount) MarshalJSON() ([]byte, error) { return []byte(a.String()), nil }
func (a *Amount) UnmarshalJSON(b []byte) error {
	if a == nil {
		return ErrAmount
	}
	if bytes.Equal(b, []byte("null")) || !json.Valid(b) || len(b) == 0 || b[0] == '"' {
		return ErrAmount
	}
	v, e := ParseAmount(string(b))
	if e == nil {
		*a = v
	}
	return e
}
func (a Amount) Value() (driver.Value, error) { return a.String(), nil }
func (a *Amount) Scan(v any) error {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		return fmt.Errorf("wallet amount: unsupported SQL numeric %T", v)
	}
	parsed, e := ParseAmount(s)
	if e == nil {
		*a = parsed
	}
	return e
}
