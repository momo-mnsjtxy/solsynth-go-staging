// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/passport/activities"
)

func TestActivityFilterSQLLiteralTermsAndOwnership(t *testing.T) {
	v := 1
	now := time.Now().UTC()
	f := activities.Filter{Take: 20, Query: "%' OR true --", Provider: "owned", ReferenceID: "ref", Term: "IDE", CatalogID: "11111111-1111-4111-8111-111111111111", Type: "coding", Tags: []string{"work", "game"}, Visibility: &v, From: &now, To: &now}
	type statement struct {
		Query string `json:"query"`
		Args  int    `json:"args"`
	}
	cases := []statement{}
	for _, catalog := range []bool{false, true} {
		p := filters("owner", f, catalog)
		if strings.Contains(p.sql, f.Query) || !strings.HasPrefix(p.sql, "account_id=$1::uuid AND deleted_at IS NULL AND ") {
			t.Fatal("query injection or owner scope absent", p.sql)
		}
		if !strings.Contains(p.sql, "position(") || strings.Contains(p.sql, " LIKE ") {
			t.Fatal("wildcard query lost literal semantics", p.sql)
		}
		cases = append(cases, statement{"SELECT 1 FROM " + map[bool]string{true: "owned_activity_catalog", false: "owned_presence_activities"}[catalog] + " WHERE " + p.sql, len(p.args)})
	}
	// Optional offline parser artifact contains SQL and counts only, no authority
	// material. It is not a database result or a successful execution assertion.
	if path := os.Getenv("ACTIVITY_SQL_GRAMMAR_OUTPUT"); path != "" {
		b, e := json.Marshal(cases)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
