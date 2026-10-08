// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"errors"
	"testing"
)

func TestCandidateQueryValidation(t *testing.T) {
	for _, q := range []Query{{}, {Take: 100, Offset: 2147483647}} {
		if err := ValidateQuery(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []Query{{Take: -1}, {Take: 101}, {Offset: -1}} {
		if err := ValidateQuery(q); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("query %+v err %v", q, err)
		}
	}
}
