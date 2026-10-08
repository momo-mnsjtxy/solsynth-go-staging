// SPDX-License-Identifier: AGPL-3.0-only
package social

import (
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"unicode/utf8"
)

var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?$`)

func ValidText(s string, n int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.ContainsRune(s, 0)
}
func ValidateRealm(p RealmPatch, create bool) error {
	if p.PictureID != nil || p.BackgroundID != nil {
		return ErrUnsupported
	}
	if create && (p.Slug == nil || p.Name == nil) {
		return ErrInvalid
	}
	if p.Slug != nil {
		if len(*p.Slug) > 256 || !slugPattern.MatchString(*p.Slug) || authority.ValidID(*p.Slug) {
			return ErrInvalid
		}
		switch strings.ToLower(*p.Slug) {
		case "invites", "quota", "search", "me":
			return ErrInvalid
		}
	}
	if p.Name != nil && (!ValidText(*p.Name, 1024) || strings.TrimSpace(*p.Name) == "") {
		return ErrInvalid
	}
	if p.Description != nil && !ValidText(*p.Description, 4096) {
		return ErrInvalid
	}
	return nil
}
func ValidatePage(offset, take int) error {
	if offset < 0 || offset > 1000000 || take < 1 || take > 100 {
		return ErrInvalid
	}
	return nil
}

// RelationshipPermissions uses the retained licensed permission registry. The
// owned PATCH endpoint requires both update and its underlying state action,
// so it cannot bypass a denied block or close-friend operation.
func RelationshipPermissions(c RelationshipChange) []string {
	switch c.Action {
	case RequestFriend, AcceptFriend, DeclineFriend, CancelFriend:
		return []string{"relationships.friends.manage"}
	case Block:
		return []string{"relationships.block.manage"}
	case Forget:
		return []string{"relationships.delete"}
	case SetClose, UnsetClose:
		return []string{"relationships.close.friends.manage"}
	case SetAlias:
		return []string{"relationships.alias.manage"}
	case SetStatus:
		switch c.Status {
		case Blocked:
			return []string{"relationships.update", "relationships.block.manage"}
		case CloseFriend:
			return []string{"relationships.update", "relationships.close.friends.manage"}
		case Friend:
			return []string{"relationships.update", "relationships.friends.manage", "relationships.close.friends.manage"}
		}
	}
	return []string{"relationships.update"}
}
