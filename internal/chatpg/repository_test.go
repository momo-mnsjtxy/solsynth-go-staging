// SPDX-License-Identifier: AGPL-3.0-only
package chatpg

import (
	"solsynth.local/go-migration/ports/messager/rooms"
	"testing"
)

func TestRepositoryRequiresExplicitPolicyAndDatabase(t *testing.T) {
	if _, e := New(nil, rooms.Policy{}); e == nil {
		t.Fatal("missing dependencies")
	}
	for i := 0; i < 100; i++ {
		v, e := id()
		if e != nil || !rooms.ValidID(v) {
			t.Fatal(v, e)
		}
	}
	if page(0, 0) == nil || page(101, 0) == nil || page(1, -1) == nil || page(100, 0) != nil {
		t.Fatal("pagination")
	}
}
