// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type Service struct {
	Store    Store
	Access   Access
	Payments PaymentVerifier
}

func (s *Service) ready(p Principal) error {
	if !ValidID(p.AccountID) || p.SessionID == "" {
		return ErrUnauthorized
	}
	if s == nil || s.Store == nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) accessible(ctx context.Context, p Principal, w Wallet, role RealmRole) error {
	if !w.Owner().Valid() || w.DeletedAt != nil {
		return ErrNotFound
	}
	if w.AccountID != nil && *w.AccountID == p.AccountID {
		return nil
	}
	if w.RealmID != nil {
		if s.Access == nil {
			return ErrUnavailable
		}
		return s.Access.Realm(ctx, p, *w.RealmID, role)
	}
	return ErrForbidden
}
func (s *Service) permission(ctx context.Context, p Principal, key string) error {
	if s.Access == nil {
		return ErrUnavailable
	}
	return s.Access.Permission(ctx, p, key)
}
func (s *Service) Get(ctx context.Context, p Principal, id string) (Wallet, error) {
	if e := s.ready(p); e != nil {
		return Wallet{}, e
	}
	var w Wallet
	var e error
	if id == "" {
		w, e = s.Store.DefaultWallet(ctx, p.AccountID)
	} else if ValidID(id) {
		w, e = s.Store.GetWallet(ctx, id)
	} else {
		return w, ErrInvalid
	}
	if e != nil {
		return w, e
	}
	return w, s.accessible(ctx, p, w, Viewer)
}
func (s *Service) List(ctx context.Context, p Principal) ([]Wallet, error) {
	if e := s.ready(p); e != nil {
		return nil, e
	}
	if s.Access == nil {
		return nil, ErrUnavailable
	}
	realms, e := s.Access.RealmIDs(ctx, p)
	if e != nil {
		return nil, e
	}
	for _, r := range realms {
		if !ValidID(r) {
			return nil, ErrUnavailable
		}
	}
	return s.Store.ListWallets(ctx, p.AccountID, realms)
}
func (s *Service) Create(ctx context.Context, p Principal, r CreateRequest) (Wallet, error) {
	if e := s.ready(p); e != nil {
		return Wallet{}, e
	}
	owner := Owner{AccountID: p.AccountID}
	if r.RealmID != nil {
		if !ValidID(*r.RealmID) {
			return Wallet{}, ErrInvalid
		}
		if s.Access == nil {
			return Wallet{}, ErrUnavailable
		}
		if e := s.Access.Realm(ctx, p, *r.RealmID, Editor); e != nil {
			return Wallet{}, e
		}
		owner = Owner{RealmID: *r.RealmID}
	}
	name := strings.TrimSpace(r.Name)
	if utf8.RuneCountInString(name) > 1024 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return Wallet{}, ErrInvalid
	}
	if name == "" {
		name = "Wallet"
		if owner.RealmID != "" {
			name = "Organization Wallet"
		}
	}
	return s.Store.CreateWallet(ctx, owner, name)
}
func (s *Service) SetDefault(ctx context.Context, p Principal, id string) (Wallet, error) {
	w, e := s.Get(ctx, p, id)
	if e != nil {
		return Wallet{}, e
	}
	if w.AccountID == nil || *w.AccountID != p.AccountID {
		return Wallet{}, ErrForbidden
	}
	return s.Store.SetDefault(ctx, id, p.AccountID)
}
func (s *Service) PublicID(ctx context.Context, p Principal, id string, enabled bool) (Wallet, error) {
	if e := s.ready(p); e != nil {
		return Wallet{}, e
	}
	if e := s.permission(ctx, p, PermissionPublicID); e != nil {
		return Wallet{}, e
	}
	w, e := s.Get(ctx, p, id)
	if e != nil {
		return Wallet{}, e
	}
	if e = s.accessible(ctx, p, w, Editor); e != nil {
		return Wallet{}, e
	}
	return s.Store.SetPublicID(ctx, id, w.Owner(), enabled)
}
func (s *Service) Transfer(ctx context.Context, p Principal, r TransferRequest, key string) (Transaction, error) {
	if e := s.ready(p); e != nil {
		return Transaction{}, e
	}
	if e := s.permission(ctx, p, PermissionTransactions); e != nil {
		return Transaction{}, e
	}
	if s.Payments == nil {
		return Transaction{}, ErrUnavailable
	}
	if r.TransferRequestID != nil {
		return Transaction{}, ErrUnavailable
	} // QR request fulfillment must be atomic; not implemented.
	if r.Amount.Sign() <= 0 {
		return Transaction{}, ErrAmount
	}
	if len(r.Currency) == 0 || utf8.RuneCountInString(r.Currency) > 128 || r.Currency != strings.TrimSpace(r.Currency) || strings.ContainsAny(r.Currency, "\x00\r\n") {
		return Transaction{}, ErrInvalid
	}
	if r.Remark != nil && (utf8.RuneCountInString(*r.Remark) > 4096 || strings.ContainsRune(*r.Remark, 0)) {
		return Transaction{}, ErrInvalid
	}
	if len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
		return Transaction{}, ErrInvalid
	}
	payerID := ""
	if r.PayerWalletID != nil {
		payerID = *r.PayerWalletID
		if !ValidID(payerID) {
			return Transaction{}, ErrInvalid
		}
	}
	payer, e := s.Get(ctx, p, payerID)
	if e != nil {
		return Transaction{}, e
	}
	if e = s.accessible(ctx, p, payer, Editor); e != nil {
		return Transaction{}, e
	}
	var payee Wallet
	switch {
	case r.PayeeWalletID != nil:
		if !ValidID(*r.PayeeWalletID) {
			return Transaction{}, ErrInvalid
		}
		payee, e = s.Store.GetWallet(ctx, *r.PayeeWalletID)
	case r.PayeePublicID != nil && strings.TrimSpace(*r.PayeePublicID) != "":
		if len(*r.PayeePublicID) > 32 {
			return Transaction{}, ErrInvalid
		}
		payee, e = s.Store.WalletByPublicID(ctx, *r.PayeePublicID)
	case r.PayeeAccountID != nil:
		if !ValidID(*r.PayeeAccountID) {
			return Transaction{}, ErrInvalid
		}
		if *r.PayeeAccountID == p.AccountID {
			return Transaction{}, ErrInvalid
		}
		payee, e = s.Store.DefaultWallet(ctx, *r.PayeeAccountID)
	default:
		return Transaction{}, ErrInvalid
	}
	if e != nil {
		return Transaction{}, e
	}
	if payer.ID == payee.ID || !payee.Owner().Valid() || payee.DeletedAt != nil {
		return Transaction{}, ErrInvalid
	}
	pin := ""
	if r.PINCode != nil {
		pin = *r.PINCode
	}
	if len(pin) > 256 {
		return Transaction{}, ErrPIN
	}
	command := TransferCommand{ActorID: p.AccountID, IdempotencyKey: key, PayerWalletID: payer.ID, PayeeWalletID: payee.ID, PayerOwner: payer.Owner(), Currency: r.Currency, Amount: r.Amount, Remark: r.Remark, Freeze: r.Freeze, RequireConfirmation: r.RequireConfirmation}
	// PIN and session credentials are deliberately excluded from persistence and hashes.
	b, _ := json.Marshal(struct {
		Payer, Payee, Currency      string
		Amount                      Amount
		Remark                      *string
		Freeze, RequireConfirmation bool
	}{payer.ID, payee.ID, r.Currency, r.Amount, r.Remark, r.Freeze, r.RequireConfirmation})
	hash := sha256.Sum256(b)
	command.Fingerprint = hex.EncodeToString(hash[:])
	if payments, ok := s.Payments.(PaymentAuthorizer); ok {
		ctx, e = payments.PreparePayment(ctx, p, pin, PaymentTransfer, command.Fingerprint)
	} else {
		e = s.Payments.VerifyPayment(ctx, p, pin)
	}
	if e != nil {
		return Transaction{}, e
	}
	return s.Store.Transfer(ctx, command)
}
func (s *Service) GetTransaction(ctx context.Context, p Principal, id string) (Transaction, error) {
	if e := s.ready(p); e != nil {
		return Transaction{}, e
	}
	if !ValidID(id) {
		return Transaction{}, ErrInvalid
	}
	tr, e := s.Store.GetTransaction(ctx, id)
	if e != nil {
		return Transaction{}, e
	}
	for _, w := range []string{tr.PayerWalletID, tr.PayeeWalletID} {
		if _, e = s.Get(ctx, p, w); e == nil {
			return tr, nil
		}
		if e == ErrUnavailable {
			return Transaction{}, e
		}
	}
	return Transaction{}, ErrNotFound
}
func (s *Service) Transactions(ctx context.Context, p Principal, q TransactionQuery) (TransactionPage, error) {
	if q.Offset < 0 || q.Take < 0 || q.Take > 100 {
		return TransactionPage{}, ErrInvalid
	}
	w, e := s.Get(ctx, p, q.WalletID)
	if e != nil {
		return TransactionPage{}, e
	}
	q.WalletID = w.ID
	switch strings.ToLower(strings.TrimSpace(q.Direction)) {
	case "":
		q.Direction = ""
	case "income", "incoming", "in":
		q.Direction = "in"
	case "outcome", "outgoing", "out":
		q.Direction = "out"
	default:
		return TransactionPage{}, ErrInvalid
	}
	return s.Store.Transactions(ctx, q)
}
func (s *Service) Transition(ctx context.Context, p Principal, id string, action Transition) (Transaction, error) {
	if e := s.ready(p); e != nil {
		return Transaction{}, e
	}
	if e := s.permission(ctx, p, PermissionTransactions); e != nil {
		return Transaction{}, e
	}
	if !ValidID(id) || (action != Confirm && action != Reject) {
		return Transaction{}, ErrInvalid
	}
	tr, e := s.Store.GetTransaction(ctx, id)
	if e != nil {
		return Transaction{}, e
	}
	payee, e := s.Get(ctx, p, tr.PayeeWalletID)
	if e != nil {
		return Transaction{}, e
	}
	if e = s.accessible(ctx, p, payee, Editor); e != nil {
		return Transaction{}, e
	}
	return s.Store.Transition(ctx, id, payee.Owner(), action)
}
