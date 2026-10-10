// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"strings"
	"testing"
)

func TestCollectionPublicationReadsFailClosedWithoutPolicy(t *testing.T) {
	r := New(nil, nil, "test.app")
	got := r.inboxVisibilitySQL()
	if !strings.Contains(got, "collection-post:") || !strings.Contains(got, "publisher-content:") || strings.Contains(got, " OR ") {
		t.Fatal("unconfigured domain source must remain hidden", got)
	}
}
