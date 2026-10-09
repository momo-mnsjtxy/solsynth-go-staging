// SPDX-License-Identifier: AGPL-3.0-only
package content

import (
	"testing"
)

func TestPostAdminAcceptsAuthoredCamelCaseQueryAliasesAndRejectsMixedDuplicates(t *testing.T) {
	f := &testRepo{}
	w := send(t, f, "GET", "/api/admin/posts?publisherId="+testID+"&realmId="+testID+"&shadowbanReason=none", "")
	if w.Code != 200 || f.query.PublisherID != testID || f.query.RealmID != testID || f.query.Reason == nil || *f.query.Reason != "none" {
		t.Fatal(w.Code, w.Body.String(), f.query)
	}
	for _, q := range []string{"publisherId=" + testID + "&publisher_id=" + testID, "realmId=" + testID + "&realm_id=" + testID, "shadowbanReason=none&shadowban_reason=none"} {
		before := f.calls
		w = send(t, f, "GET", "/api/admin/posts?"+q, "")
		if w.Code != 400 || f.calls != before {
			t.Fatal(q, w.Code)
		}
	}
}
