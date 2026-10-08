// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"errors"
	"solsynth.local/go-migration/ports/passport/social"
	"testing"
)

func TestUnavailableRepositoryFailsClosed(t *testing.T) {
	r := NewWithAuthority(nil, nil)
	ctx := context.Background()
	if _, e := r.ChangeRelationship(ctx, "c0520000-0000-4000-8000-000000000001", "c0520000-0000-4000-8000-000000000002", social.RelationshipChange{Action: social.RequestFriend}); !errors.Is(e, social.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.IsBlockedInTransaction(ctx, nil, "", ""); !errors.Is(e, social.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.CanReadFriendsInTransaction(ctx, nil, "", "", false); !errors.Is(e, social.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := r.RealmRoleInTransaction(ctx, nil, "", ""); !errors.Is(e, social.ErrUnavailable) {
		t.Fatal(e)
	}
}
