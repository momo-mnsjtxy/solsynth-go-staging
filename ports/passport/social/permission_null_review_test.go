// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRealmPermissionEveryNullableCallerFieldReplacesPriorOverride(t *testing.T) {
	fields := []string{"can_chat", "can_post", "can_comment", "can_upload_media", "can_moderate_posts", "can_moderate_chat", "can_manage_members", "can_manage_realm"}
	var request UserPermissionRequest
	for i, field := range fields {
		for _, value := range []string{"true", "false", "null"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				// Reuse the request to catch accidental merging with earlier input.
				request.PermissionOverrides = OverridesFromMasks(255, 0)
				raw := fmt.Sprintf(`{"account_id":"member","%s":%s}`, field, value)
				if e := json.Unmarshal([]byte(raw), &request); e != nil {
					t.Fatal(e)
				}
				allow, deny := request.Masks()
				wantAllow, wantDeny := 0, 0
				if value == "true" {
					wantAllow = 1 << i
				} else if value == "false" {
					wantDeny = 1 << i
				}
				if allow != wantAllow || deny != wantDeny {
					t.Fatalf("%s: masks %d/%d, want %d/%d", raw, allow, deny, wantAllow, wantDeny)
				}
			})
		}
	}
	parts := []string{`"account_id":"member"`}
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf(`%q:null`, field))
	}
	for _, raw := range []string{`{"account_id":"member"}`, "{" + strings.Join(parts, ",") + "}"} {
		request.PermissionOverrides = OverridesFromMasks(255, 0)
		if e := json.Unmarshal([]byte(raw), &request); e != nil {
			t.Fatal(e)
		}
		if allow, deny := request.Masks(); allow != 0 || deny != 0 {
			t.Fatal("omitted/null caller clear retained a grant", allow, deny)
		}
	}
}
