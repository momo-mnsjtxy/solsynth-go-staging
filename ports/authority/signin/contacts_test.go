// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"errors"
	"testing"
)

func TestContactInputCanonicalEmailAndStrictFields(t *testing.T) {
	v, e := decodeContactInput([]byte(`{"type":0,"content":"  Synthetic.Email@EXAMPLE.INVALID  "}`))
	if e != nil || v.Content != "synthetic.email@example.invalid" || v.Type != 0 {
		t.Fatal(v, e)
	}
	for _, raw := range []string{"", `null`, `[]`, `{"type":null,"content":"x@example.invalid"}`, `{"type":0,"type":1,"content":"synthetic@example.invalid"}`, `{"Type":0,"content":"synthetic@example.invalid"}`, `{"type":0,"content":"Name <synthetic@example.invalid>"}`, `{"type":3,"content":"unsupported"}`, `{"type":0,"content":"x@example.invalid","verified_at":"2026-01-01"}`, `{"content":"x@example.invalid"}`} {
		if _, e = decodeContactInput([]byte(raw)); !errors.Is(e, ErrInput) {
			t.Fatal("invalid contact input accepted", raw, e)
		}
	}
}
func TestContactPhoneAddressNeverNormalizeAsEmail(t *testing.T) {
	for _, kind := range []int{1, 2} {
		v := ContactInput{Type: kind, Content: " Synthetic owner-only data "}
		if e := ValidateContact(&v); e != nil || v.Content != "Synthetic owner-only data" {
			t.Fatal(v, e)
		}
	}
}
