// SPDX-License-Identifier: AGPL-3.0-only
package taxonomypg

import (
	"strings"
	"testing"
)

func TestFixedOwnedNamespacePolicy(t *testing.T) {
	q := "SELECT * FROM taxonomy_candidate.post_tags JOIN taxonomy_candidate.publishers ON true WHERE slug=$1"
	expected := "SELECT * FROM owned_taxonomy.post_tags JOIN owned_publishers ON true WHERE slug=$1"
	if policySQL(q, true) != expected || policySQL(q, false) != q {
		t.Fatal("namespace isolation")
	}
	if strings.Contains(policySQL(q, true), "taxonomy_candidate") {
		t.Fatal("candidate leaked")
	}
	owned := NewWithIndependentPolicy(nil, 42)
	fixture := NewSyntheticRepository(nil, 42)
	if !owned.owned || fixture.owned || New(nil).owned {
		t.Fatal("constructors mix namespaces")
	}
	// The sole selector is a private bool. There is no schema/table string input,
	// including from a slug such as this value, which stays a SQL parameter.
	userSlug := "taxonomy_candidate.publishers; DROP TABLE owned_publishers"
	if strings.Contains(policySQL(q, true), userSlug) {
		t.Fatal("unbound slug")
	}
}
