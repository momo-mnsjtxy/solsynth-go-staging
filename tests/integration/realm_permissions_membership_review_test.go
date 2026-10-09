// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/passport/social"
)

func TestPGRealmPermissionDeletedMembershipCannotReviveOverride(t *testing.T) {
	db, repository, ids := socialFixture(t)
	ctx := context.Background()
	owner, member := ids[0], ids[1]
	realm := socialRealm(t, repository, owner, true, true)
	realmPermissionJoin(t, repository, member, realm.ID)
	grant := true
	if _, e := repository.SetUserPermission(ctx, owner, realm.ID, social.UserPermissionRequest{AccountID: member, PermissionOverrides: social.PermissionOverrides{CanManageRealm: &grant}}); e != nil {
		t.Fatal(e)
	}
	realmPermissionMask(t, repository, member, realm.ID, 143)
	var memberID string
	var joinedAt time.Time
	if e := db.QueryRow(`SELECT id::text,joined_at FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2`, realm.ID, member).Scan(&memberID, &joinedAt); e != nil {
		t.Fatal(e)
	}
	query(t, db, `DELETE FROM owned_realm_members WHERE id=$1`, memberID)
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM owned_realm_user_permissions WHERE realm_id=$1 AND account_id=$2`, realm.ID, member).Scan(&count); e != nil || count != 0 {
		t.Fatal("deleted membership did not cascade its override", count, e)
	}
	// Deliberately reuse the exact row identifier, with a new incarnation. Even
	// a stale externally restored override must remain bound to its old join.
	query(t, db, `INSERT INTO owned_realm_members(id,realm_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,0,$4,$4,$4)`, memberID, realm.ID, member, joinedAt.Add(time.Second))
	query(t, db, `INSERT INTO owned_realm_user_permissions(realm_id,account_id,member_id,membership_since,allow_mask,deny_mask)VALUES($1,$2,$3,$4,128,0)`, realm.ID, member, memberID, joinedAt)
	realmPermissionMask(t, repository, member, realm.ID, 15)
	if current, e := repository.UserPermissions(ctx, owner, realm.ID, member); e != nil || current != nil {
		t.Fatal("new incarnation exposed stale override", current, e)
	}
}
