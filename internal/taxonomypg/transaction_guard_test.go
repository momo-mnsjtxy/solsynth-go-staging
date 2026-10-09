// SPDX-License-Identifier: AGPL-3.0-only
package taxonomypg

import (
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"testing"
)

func TestMutationPermissionPreservesRoutePolicy(t *testing.T) {
	for _, c := range []struct {
		k     taxonomy.Kind
		admin bool
		op    taxonomy.Mutation
		want  string
	}{
		{taxonomy.Tag, false, "create", ""}, {taxonomy.Tag, false, taxonomy.Update, ""}, {taxonomy.Tag, false, taxonomy.Release, ""}, {taxonomy.Tag, false, taxonomy.Protect, ""}, {taxonomy.Tag, false, taxonomy.Claim, "posts.tags.claim"},
		{taxonomy.Tag, true, "create", "posts.tags.create"}, {taxonomy.Tag, true, taxonomy.Update, "posts.tags.update"}, {taxonomy.Tag, true, taxonomy.Assign, "posts.tags.assign"}, {taxonomy.Tag, true, taxonomy.Unassign, "posts.tags.assign"}, {taxonomy.Tag, true, taxonomy.Protect, "posts.tags.protect"}, {taxonomy.Tag, true, taxonomy.Event, "posts.tags.event"}, {taxonomy.Tag, true, taxonomy.Delete, "posts.tags.delete"},
		{taxonomy.Category, true, "create", "post.categories.manage"}, {taxonomy.Category, true, taxonomy.Update, "post.categories.manage"}, {taxonomy.Category, true, taxonomy.Delete, "post.categories.manage"},
	} {
		got, e := mutationPermission(c.k, c.admin, c.op)
		if e != nil || got != c.want {
			t.Fatalf("%s admin=%v %s: %q %v", c.k, c.admin, c.op, got, e)
		}
	}
	for _, c := range []struct {
		k     taxonomy.Kind
		admin bool
		op    taxonomy.Mutation
	}{{taxonomy.Category, false, "create"}, {taxonomy.Category, false, taxonomy.Delete}, {taxonomy.Category, true, taxonomy.Claim}, {taxonomy.Tag, false, taxonomy.Delete}, {taxonomy.Tag, false, taxonomy.Assign}, {taxonomy.Tag, true, taxonomy.Claim}, {taxonomy.Tag, false, "unknown"}, {"unknown", true, "create"}} {
		if _, e := mutationPermission(c.k, c.admin, c.op); e == nil {
			t.Fatalf("unsupported mutation accepted: %+v", c)
		}
	}
}
