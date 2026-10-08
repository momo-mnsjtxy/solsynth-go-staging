// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"testing"
)

func TestRealmPermissionSQLRejectsUnknownKeysAndExpressions(t *testing.T) {
	r := &Repository{}
	for _, bad := range []string{"", "r.id OR TRUE", "r.id); DROP TABLE x;--", "$0"} {
		if r.RealmPermissionSQL(bad, "$1", social.CanChat) != "FALSE" || r.RealmPermissionSQL("r.id", bad, social.CanChat) != "FALSE" {
			t.Fatal(bad)
		}
	}
	if r.RealmPermissionSQL("r.id", "$1", social.RealmPermission("realm.owner")) != "FALSE" {
		t.Fatal("unknown legacy alias granted")
	}
	if q := r.RealmPermissionSQL("r.id", "$1", social.CanChat); !strings.Contains(q, "owned_realm_permission_mask") || !strings.Contains(q, "& 1)") {
		t.Fatal(q)
	}
}
