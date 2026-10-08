// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"solsynth.local/go-migration/ports/messager"
	"strings"
	"unicode/utf8"
)

func ValidID(id string) bool {
	return id != "00000000-0000-0000-0000-000000000000" && id == strings.ToLower(id) && messager.ValidateSyncID(id) == nil
}
func validText(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if r == 0 || r < 32 && r != '\n' && r != '\t' && r != '\r' || r == 127 {
			return false
		}
	}
	return true
}
func ValidateRoom(p RoomPatch, create bool, policy Policy) error {
	if p.RealmID != nil && !ValidID(*p.RealmID) {
		return ErrInvalid
	}
	if create && p.Name == nil || p.Name != nil && (!validText(*p.Name, 1024) || strings.TrimSpace(*p.Name) == "") || p.Description != nil && !validText(*p.Description, 4096) {
		return ErrInvalid
	}
	if p.EncryptionMode != nil && *p.EncryptionMode != policy.NoneEncryptionMode {
		return ErrUnsupported
	}
	return nil
}
func ValidateTimeout(p *TimeoutRequest) error {
	if p != nil && (p.TimeoutUntil.IsZero() || p.Reason != nil && !validText(*p.Reason, 4096)) {
		return ErrInvalid
	}
	return nil
}
func ValidateText(p TextRequest, edit bool) (TextRequest, error) {
	if !validText(p.Content, 4096) || strings.TrimSpace(p.Content) == "" {
		return p, ErrInvalid
	}
	if p.Nonce != nil && (!validText(*p.Nonce, 36) || strings.TrimSpace(*p.Nonce) == "") {
		return p, ErrInvalid
	}
	if p.ClientMessageID != nil && (!validText(*p.ClientMessageID, 128) || strings.TrimSpace(*p.ClientMessageID) == "") {
		return p, ErrInvalid
	}
	if p.ClientMessageID != nil && p.Nonce != nil && *p.ClientMessageID != *p.Nonce {
		return p, ErrInvalid
	}
	if p.ClientMessageID == nil {
		p.ClientMessageID = p.Nonce
	}
	p.Nonce = nil
	if p.RepliedMessageID != nil && !ValidID(*p.RepliedMessageID) {
		return p, ErrInvalid
	}
	if edit && (p.ClientMessageID != nil || p.RepliedMessageID != nil) {
		return p, ErrUnsupported
	}
	return p, nil
}
