package accounts

import (
	"encoding/json"
	"testing"
)

func mustAmount(t *testing.T, s string) Amount {
	t.Helper()
	a, e := ParseAmount(s)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestAmountExactDecimal(t *testing.T) {
	for _, tc := range []struct{ in, out string }{{"0", "0.000"}, {"-0.0001", "0.000"}, {"1.234999", "1.234"}, {"-1.234999", "-1.234"}, {"9007199254740993.123", "9007199254740993.123"}, {"0.0009999", "0.000"}, {"1e-3", "0.001"}, {"1.2345e3", "1234.500"}, {"100000000000000000000000000000e-1", "10000000000000000000000000000.000"}, {"79228162514264337593543950335", "79228162514264337593543950335.000"}} {
		a, e := ParseAmount(tc.in)
		if e != nil || a.String() != tc.out {
			t.Fatalf("%s: %s %v", tc.in, a.String(), e)
		}
		b, e := json.Marshal(a)
		if e != nil || string(b) != tc.out {
			t.Fatal(string(b), e)
		}
		var decoded Amount
		if e = json.Unmarshal(b, &decoded); e != nil || decoded != a {
			t.Fatal(e, decoded)
		}
	}
}
func TestAmountRejectsUnsafeRepresentations(t *testing.T) {
	for _, v := range []string{"", "NaN", "Infinity", " 1", "1 ", ".1", "1.", "+1", "1/2", "1e999999", "79228162514264337593543950336", "79228162514264337593543950335.001"} {
		if _, e := ParseAmount(v); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range []string{`null`, `"1"`, `{}`, `[]`, `01`, `true`} {
		var a Amount
		if json.Unmarshal([]byte(v), &a) == nil {
			t.Fatal(v)
		}
	}
	var a Amount
	if a.Scan(float64(1)) == nil {
		t.Fatal("SQL float accepted")
	}
}
func TestAmountArithmetic(t *testing.T) {
	a := mustAmount(t, "9007199254740993.123")
	b := mustAmount(t, "0.001")
	c, e := a.Add(b)
	if e != nil || c.String() != "9007199254740993.124" {
		t.Fatal(c, e)
	}
	d, e := c.Sub(a)
	if e != nil || d != b {
		t.Fatal(d, e)
	}
	if _, e = mustAmount(t, "79228162514264337593543950335").Add(b); e == nil {
		t.Fatal("overflow")
	}
	if a.Cmp(b) != 1 || b.Sign() != 1 || (Amount{}).Sign() != 0 {
		t.Fatal("comparison")
	}
}
