// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

// AddressContactType=2 is evidenced by the authored Solian contact dropdown.
const AddressContactType = 2

type AddressTransaction interface {
	UpdateAddress(string, ContactInput, time.Time) (Contact, error)
}

// UpdateAddress is an owned PATCH extension using the existing type/content DTO.
// Email/phone/primary verification state cannot be changed through this path.
func (s *Service) UpdateAddress(ctx context.Context, token, id string, input ContactInput) (out Contact, e error) {
	if !authority.ValidID(id) || input.Type != AddressContactType {
		return out, ErrInput
	}
	if e = ValidateContact(&input); e != nil {
		return out, e
	}
	e = s.contacts(ctx, token, true, true, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		v, ok := c.(AddressTransaction)
		if !ok {
			return ErrUnsupported
		}
		var e error
		out, e = v.UpdateAddress(id, input, s.now())
		return e
	})
	return
}
func (s *Service) GetContact(ctx context.Context, token, id string) (out Contact, e error) {
	if !authority.ValidID(id) {
		return out, ErrInput
	}
	e = s.contacts(ctx, token, false, true, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		var e error
		out, e = c.Contact(id)
		if e != nil {
			return e
		}
		return c.AuthorizeContacts()
	})
	return
}
