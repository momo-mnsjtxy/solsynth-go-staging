// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"strings"
	"testing"
)

func TestRelationSQLRejectsUntrustedExpressionsAndDefaultsClosed(t *testing.T) {
	r := &Repository{}
	for _, build := range []func(string, string) string{r.ReadSQL, r.SendSQL, r.CallSQL} {
		for _, input := range []string{"$0", "room); SELECT 1", "r.id OR TRUE", "r.id--", "r.\"id\"", ""} {
			if build(input, "$2") != "FALSE" || build("$1", input) != "FALSE" {
				t.Fatal("nonconstant SQL expression accepted")
			}
		}
	}
	for _, pair := range [][2]string{{"$1", "$2"}, {"r.id", "cm.account_id"}} {
		q := r.ReadSQL(pair[0], pair[1])
		if !strings.Contains(q, "owned_relation_guard") || strings.Contains(q, "owned_chat_direct_pairs") {
			t.Fatal("missing social provider must exclude linked rooms without querying optional tables")
		}
	}
}
