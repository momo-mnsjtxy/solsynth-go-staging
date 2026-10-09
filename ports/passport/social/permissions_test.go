// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"encoding/json"
	"testing"
)

func TestRealmPermissionDTORequiresExactBooleansAndNullClear(t *testing.T) {
	all := `"can_chat":true,"can_post":true,"can_comment":true,"can_upload_media":true,"can_moderate_posts":false,"can_moderate_chat":false,"can_manage_members":false,"can_manage_realm":false`
	var r RolePermissionRequest
	if e := json.Unmarshal([]byte(`{"role_level":17,`+all+`}`), &r); e != nil || r.RoleLevel == nil || *r.RoleLevel != 17 {
		t.Fatal(r, e)
	}
	for _, raw := range []string{`{"role_level":17}`, `{"role_level":17,` + all + `,"can_chat":false}`, `{"role_level":17,` + all + `,"admin":true}`, `{"role_level":17,` + all + `,"role_level":18}`} {
		if json.Unmarshal([]byte(raw), &r) == nil {
			t.Fatal("ambiguous role grant accepted", raw)
		}
	}
	var u UserPermissionRequest
	if e := json.Unmarshal([]byte(`{"account_id":"account","canChat":null,"canPost":null,"canComment":null,"canUploadMedia":null,"canModeratePosts":null,"canModerateChat":null,"canManageMembers":null,"canManageRealm":null}`), &u); e != nil {
		t.Fatal(e)
	}
	if a, d := u.Masks(); a != 0 || d != 0 {
		t.Fatal("null clear granted permissions", a, d)
	}
	for _, raw := range []string{`{"account_id":"account","canChat":true}`, `{"account_id":"account","canChat":null,"can_chat":false}`, `{"account_id":"account","can_chat":true,"can_chat":false}`, `{"account_id":"account","can_delete_everything":true}`} {
		if json.Unmarshal([]byte(raw), &u) == nil {
			t.Fatal("unknown or ambiguous override accepted", raw)
		}
	}
}
func TestRealmPermissionMaskIsOwnedAndUnknownDenies(t *testing.T) {
	for mask := 0; mask < 256; mask++ {
		p := PermissionsFromMask(mask)
		if p.Mask() != mask {
			t.Fatal(mask, p)
		}
		if p.Allows(RealmPermission("admin")) || p.Allows(RealmPermission("*")) {
			t.Fatal("unknown permission allowed")
		}
	}
	for _, role := range []Role{1, 17, 49, 51, 99} {
		if !role.Valid() {
			t.Fatal(role)
		}
	}
}
