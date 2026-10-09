// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

// These are the authored JSON permission fields, not aliases for excluded enums.
type RealmPermission string

const (
	CanChat          RealmPermission = "can_chat"
	CanPost          RealmPermission = "can_post"
	CanComment       RealmPermission = "can_comment"
	CanUploadMedia   RealmPermission = "can_upload_media"
	CanModeratePosts RealmPermission = "can_moderate_posts"
	CanModerateChat  RealmPermission = "can_moderate_chat"
	CanManageMembers RealmPermission = "can_manage_members"
	CanManageRealm   RealmPermission = "can_manage_realm"
)

var realmPermissionFields = []RealmPermission{CanChat, CanPost, CanComment, CanUploadMedia, CanModeratePosts, CanModerateChat, CanManageMembers, CanManageRealm}

func (p RealmPermission) Bit() int {
	for i, k := range realmPermissionFields {
		if p == k {
			return 1 << i
		}
	}
	return 0
}

type RealmPermissions struct {
	CanChat          bool `json:"can_chat"`
	CanPost          bool `json:"can_post"`
	CanComment       bool `json:"can_comment"`
	CanUploadMedia   bool `json:"can_upload_media"`
	CanModeratePosts bool `json:"can_moderate_posts"`
	CanModerateChat  bool `json:"can_moderate_chat"`
	CanManageMembers bool `json:"can_manage_members"`
	CanManageRealm   bool `json:"can_manage_realm"`
}

func PermissionsFromMask(n int) RealmPermissions {
	return RealmPermissions{n&1 != 0, n&2 != 0, n&4 != 0, n&8 != 0, n&16 != 0, n&32 != 0, n&64 != 0, n&128 != 0}
}
func (p RealmPermissions) Mask() int {
	out := 0
	for i, v := range []bool{p.CanChat, p.CanPost, p.CanComment, p.CanUploadMedia, p.CanModeratePosts, p.CanModerateChat, p.CanManageMembers, p.CanManageRealm} {
		if v {
			out |= 1 << i
		}
	}
	return out
}
func (p RealmPermissions) Allows(k RealmPermission) bool {
	return k.Bit() != 0 && p.Mask()&k.Bit() != 0
}

type PermissionOverrides struct {
	CanChat          *bool `json:"can_chat"`
	CanPost          *bool `json:"can_post"`
	CanComment       *bool `json:"can_comment"`
	CanUploadMedia   *bool `json:"can_upload_media"`
	CanModeratePosts *bool `json:"can_moderate_posts"`
	CanModerateChat  *bool `json:"can_moderate_chat"`
	CanManageMembers *bool `json:"can_manage_members"`
	CanManageRealm   *bool `json:"can_manage_realm"`
}

func (p PermissionOverrides) Masks() (allow, deny int) {
	for i, v := range []*bool{p.CanChat, p.CanPost, p.CanComment, p.CanUploadMedia, p.CanModeratePosts, p.CanModerateChat, p.CanManageMembers, p.CanManageRealm} {
		if v == nil {
			continue
		}
		if *v {
			allow |= 1 << i
		} else {
			deny |= 1 << i
		}
	}
	return
}
func OverridesFromMasks(allow, deny int) PermissionOverrides {
	var values [8]*bool
	for i := range values {
		if allow&(1<<i) != 0 || deny&(1<<i) != 0 {
			v := allow&(1<<i) != 0
			values[i] = &v
		}
	}
	return PermissionOverrides{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7]}
}

type RolePermission struct {
	ID         string `json:"id"`
	RealmID    string `json:"realm_id"`
	RoleLevel  Role   `json:"role_level"`
	Generation int64  `json:"generation"`
	RealmPermissions
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type UserPermission struct {
	ID        string `json:"id"`
	RealmID   string `json:"realm_id"`
	AccountID string `json:"account_id"`
	PermissionOverrides
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type RolePermissionRequest struct {
	RoleLevel *Role `json:"role_level"`
	PermissionOverrides
}
type UserPermissionRequest struct {
	AccountID string `json:"account_id"`
	PermissionOverrides
}
type PermissionRepository interface {
	RolePermissions(context.Context, string, string) ([]RolePermission, error)
	SetRolePermission(context.Context, string, string, RolePermissionRequest) (RolePermission, error)
	DeleteRolePermission(context.Context, string, string, Role) error
	UserPermissions(context.Context, string, string, string) (*UserPermission, error)
	SetUserPermission(context.Context, string, string, UserPermissionRequest) (*UserPermission, error)
	EffectivePermissions(context.Context, string, string) (RealmPermissions, error)
}
type PermissionProvider interface {
	RealmPermissionsInTransaction(context.Context, *sql.Tx, string, string) (RealmPermissions, error)
	RealmPermissionForWriteInTransaction(context.Context, *sql.Tx, string, string, RealmPermission) error
	RealmPermissionSQL(string, string, RealmPermission) string
}

func permissionObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return nil, ErrInvalid
		}
		s, ok := key.(string)
		if !ok {
			return nil, ErrInvalid
		}
		if _, exists := out[s]; exists {
			return nil, ErrInvalid
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrInvalid
		}
		out[s] = v
	}
	if token, e = d.Token(); e != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	return out, nil
}
func (r *RolePermissionRequest) UnmarshalJSON(data []byte) error {
	fields, e := permissionObject(data)
	if e != nil {
		return e
	}
	for k, v := range fields {
		if k != "role_level" && RealmPermission(k).Bit() == 0 {
			return ErrInvalid
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalid
		}
	}
	type plain RolePermissionRequest
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil {
		return ErrInvalid
	}
	a, b := v.PermissionOverrides.Masks()
	if v.RoleLevel == nil || a|b != 255 {
		return ErrInvalid
	}
	*r = RolePermissionRequest(v)
	return nil
}
func (r *UserPermissionRequest) UnmarshalJSON(data []byte) error {
	fields, e := permissionObject(data)
	if e != nil {
		return e
	}
	aliases := map[string]string{"canChat": "can_chat", "canPost": "can_post", "canComment": "can_comment", "canUploadMedia": "can_upload_media", "canModeratePosts": "can_moderate_posts", "canModerateChat": "can_moderate_chat", "canManageMembers": "can_manage_members", "canManageRealm": "can_manage_realm"}
	count := 0
	for k, v := range fields {
		if mapped, ok := aliases[k]; ok {
			if !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return ErrInvalid
			}
			if _, exists := fields[mapped]; exists {
				return ErrInvalid
			}
			count++
		} else if k != "account_id" && RealmPermission(k).Bit() == 0 {
			return ErrInvalid
		}
	}
	// Only the actual authored all-null camelCase reset map is an alias contract.
	if count > 0 {
		if count != 8 || len(fields) != 9 {
			return ErrInvalid
		}
		for k, mapped := range aliases {
			fields[mapped] = fields[k]
			delete(fields, k)
		}
		data, _ = json.Marshal(fields)
	}
	type plain UserPermissionRequest
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil {
		return ErrInvalid
	}
	*r = UserPermissionRequest(v)
	return nil
}
