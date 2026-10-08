// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"strings"
	"testing"
)

func TestPublisherAliasSelectorIsExactAndRejectsExpressions(t *testing.T) {
	v := PublisherNamePredicate("p", "$2")
	if !strings.Contains(v, "publisher_id=p.id") || !strings.Contains(v, "lower($2::text)") || strings.Contains(v, "LIKE") {
		t.Fatal(v)
	}
	for _, args := range [][2]string{{"p.id", "$1"}, {"p", "request_value"}, {"p q", "$1"}, {"p", "$0"}} {
		if PublisherNamePredicate(args[0], args[1]) != "FALSE" {
			t.Fatal(args)
		}
	}
}
