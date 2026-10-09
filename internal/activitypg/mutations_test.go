// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"testing"

	"solsynth.local/go-migration/ports/passport/activities"
)

func TestManualRenewalPreservesOmittedTypeAndAudience(t *testing.T) {
	for _, audience := range []int{activities.Private, activities.Friends} {
		typ, manual := "coding", "editor"
		v := row{Activity: activities.Activity{Content: activities.Content{Type: &typ}, ManualID: &manual, Visibility: audience}}
		q := activities.Request{ManualID: &manual, LeaseMinutes: 5, Visibility: activities.Public}
		if e := activities.Normalize(&q, true); e != nil {
			t.Fatal(e)
		}
		applyManual(&v, q, true)
		if v.Type == nil || *v.Type != typ || v.Visibility != audience {
			t.Fatal("renewal published private activity or removed type", v)
		}
		next := "gaming"
		q.Type, q.VisibilitySpecified = &next, true
		applyManual(&v, q, true)
		if *v.Type != next || v.Visibility != activities.Public {
			t.Fatal("explicit update was ignored", v)
		}
	}
}
