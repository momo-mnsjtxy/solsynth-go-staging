// SPDX-License-Identifier: AGPL-3.0-only
package signin

import "testing"

func TestPaymentPINSixDigitsAndBoundActionInput(t *testing.T) {
	if !sixDigitPIN("012345") {
		t.Fatal("leading zero PIN rejected")
	}
	for _, v := range []string{"", "12345", "1234567", "１２３４５６", "12-456", "12345 "} {
		if sixDigitPIN(v) {
			t.Fatal(v)
		}
	}
	id := "12345678-1234-4234-9234-123456789012"
	for _, v := range [][2]string{{"create", id}, {"delete", ""}, {"enable", "someone"}, {"replace", id}, {"login", id}} {
		if validPINAction(v[0], v[1]) {
			t.Fatal(v)
		}
	}
	for _, action := range []string{"enable", "disable", "delete"} {
		if !validPINAction(action, id) {
			t.Fatal(action)
		}
	}
}
