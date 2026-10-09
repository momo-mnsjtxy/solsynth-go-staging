// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/sphere/posts"
	"testing"
)

func TestPostPinFilterScopePrecedence(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		o    posts.ListOptions
		want string
	}{{posts.ListOptions{}, ""}, {posts.ListOptions{Pinned: &yes, Publisher: "pub"}, " AND p.pin_mode=0"}, {posts.ListOptions{Pinned: &yes, Publisher: "pub", Realm: "realm"}, " AND p.pin_mode=1"}, {posts.ListOptions{Pinned: &no, Realm: "realm"}, " AND p.pin_mode IS NULL"}} {
		if got := pinFilter(tc.o); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
func TestPostPinUnavailableAndRealmMoveInvalidation(t *testing.T) {
	r := New(nil)
	id := "11111111-1111-4111-8111-111111111111"
	if _, e := r.Pin(context.Background(), id, id, 0); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.Unpin(context.Background(), id, id); !errors.Is(e, posts.ErrUnavailable) {
		t.Fatal(e)
	}
	old, next := "old", "next"
	mode := posts.RealmPin
	p := posts.Post{RealmID: &old, PinMode: &mode}
	apply(&p, posts.Patch{RealmID: &next})
	if p.PinMode != nil {
		t.Fatal("realm move retained pin")
	}
	p = posts.Post{RealmID: &old, PinMode: &mode}
	apply(&p, posts.Patch{ClearRealm: true})
	if p.PinMode != nil {
		t.Fatal("realm detach retained pin")
	}
	mode = posts.PublisherPin
	p = posts.Post{RealmID: &old, PinMode: &mode}
	apply(&p, posts.Patch{RealmID: &next})
	if p.PinMode == nil || *p.PinMode != 0 {
		t.Fatal("publisher pin lost")
	}
}
