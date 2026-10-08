// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"context"
	"net/mail"
	"solsynth.local/go-migration/ports/authority"
	"strings"
	"time"
	"unicode/utf8"
)

type Contact struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	Type       int        `json:"type"`
	Content    string     `json:"content"`
	VerifiedAt *time.Time `json:"verified_at"`
	IsPrimary  bool       `json:"is_primary"`
	IsPublic   bool       `json:"is_public"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at"`
}
type ContactInput struct {
	Type    int    `json:"type"`
	Content string `json:"content"`
}

// ContactTransaction extends the same canonical signin/account transaction.
// A primary contact references the existing credentials row; it owns no copy.
type ContactTransaction interface {
	AuthorizeContacts() error
	Contacts() ([]Contact, error)
	Contact(string) (Contact, error)
	AddContact(ContactInput, time.Time) (Contact, error)
	DeleteContact(string, time.Time) error
	SetContactPrimary(string, time.Time) (Contact, error)
	SetContactPublic(string, bool, time.Time) (Contact, error)
	VerifyContact(string, time.Time) error
	CancelContactChallenges(string) error
}

func NormalizeEmail(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	parsed, e := mail.ParseAddress(v)
	if e != nil || parsed.Address != v || len(v) > 254 || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return "", ErrInput
	}
	return v, nil
}
func ValidateContact(v *ContactInput) error {
	v.Content = strings.TrimSpace(v.Content)
	if v.Type < 0 || v.Type > 2 || v.Content == "" || !utf8.ValidString(v.Content) || strings.ContainsRune(v.Content, 0) {
		return ErrInput
	}
	limit := 254
	if v.Type == 1 {
		limit = 64
	}
	if v.Type == 2 {
		limit = 2048
	}
	if len(v.Content) > limit {
		return ErrInput
	}
	if v.Type == 0 {
		email, e := NormalizeEmail(v.Content)
		if e != nil {
			return e
		}
		v.Content = email
	}
	return nil
}
func (s *Service) contacts(ctx context.Context, token string, recent, write bool, fn func(Transaction, ContactTransaction, authority.Principal) error) error {
	return s.authenticated(ctx, token, recent, func(tx Transaction, p authority.Principal) error {
		contacts, ok := tx.(ContactTransaction)
		if !ok {
			return ErrUnsupported
		}
		if write {
			if e := contacts.AuthorizeContacts(); e != nil {
				return e
			}
		}
		if e := fn(tx, contacts, p); e != nil {
			return e
		}
		// A downstream row/email wait must not extend the recent-login window.
		// Check the original session time, including for a promotion that has
		// intentionally advanced this transaction's canonical account version.
		if recent && (s.now().Sub(p.Session.CreatedAt) > 10*time.Minute || s.now().Before(p.Session.CreatedAt)) {
			return ErrRecent
		}
		return nil
	})
}
func (s *Service) ListContacts(ctx context.Context, token string) (out []Contact, e error) {
	out = []Contact{}
	e = s.contacts(ctx, token, false, false, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		var e error
		out, e = c.Contacts()
		return e
	})
	return
}
func (s *Service) CreateContact(ctx context.Context, token string, input ContactInput) (out Contact, e error) {
	if e = ValidateContact(&input); e != nil {
		return
	}
	e = s.contacts(ctx, token, true, true, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		var e error
		out, e = c.AddContact(input, s.now())
		return e
	})
	return
}
func (s *Service) RemoveContact(ctx context.Context, token, id string) error {
	if !authority.ValidID(id) {
		return ErrInput
	}
	return s.contacts(ctx, token, true, true, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		return c.DeleteContact(id, s.now())
	})
}
func (s *Service) MakeContactPrimary(ctx context.Context, token, id string) (out Contact, e error) {
	if !authority.ValidID(id) {
		return out, ErrInput
	}
	e = s.contacts(ctx, token, true, true, func(tx Transaction, c ContactTransaction, _ authority.Principal) error {
		var e error
		out, e = c.SetContactPrimary(id, s.now())
		return e
	})
	return
}
func (s *Service) PublishContact(ctx context.Context, token, id string, public bool) (out Contact, e error) {
	if !authority.ValidID(id) {
		return out, ErrInput
	}
	e = s.contacts(ctx, token, false, true, func(_ Transaction, c ContactTransaction, _ authority.Principal) error {
		var e error
		out, e = c.SetContactPublic(id, public, s.now())
		return e
	})
	return
}
func (s *Service) SendContactVerification(ctx context.Context, token, id string) error {
	if !authority.ValidID(id) {
		return ErrInput
	}
	if s.cfg.Delivery == nil {
		return ErrDelivery
	}
	var destination, raw string
	p, e := s.verifier.Authenticate(ctx, token)
	if e != nil {
		return e
	}
	if e = s.limit(ctx, "contact-verification", p.Account.ID+":"+id, 3, time.Hour); e != nil {
		return e
	}
	e = s.contacts(ctx, token, false, true, func(tx Transaction, c ContactTransaction, p authority.Principal) error {
		contact, e := c.Contact(id)
		if e != nil {
			return e
		}
		if contact.Type != 0 {
			return ErrUnsupported
		}
		if contact.VerifiedAt != nil {
			return ErrConflict
		}
		challenge, e := s.makeChallenge(p.Account.ID, p.Account.Version, "verify_contact", "", 0, 30*time.Minute)
		if e != nil {
			return e
		}
		challenge.ContactID = id
		if e = c.CancelContactChallenges(id); e != nil {
			return e
		}
		if e = tx.SaveChallenge(challenge); e != nil {
			return e
		}
		destination, raw = contact.Content, challenge.ID
		return nil
	})
	if e != nil {
		return e
	}
	if e = s.cfg.Delivery.Send(ctx, destination, "verify_contact", raw); e != nil {
		return ErrDelivery
	}
	return nil
}
