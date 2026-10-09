// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

const PermissionTransferRequests = "wallets.transfer.requests.manage"

// Numeric states are owned persistence semantics. The retained authored Flutter
// model consumes the other fields and does not deserialize the original enum.
const (
	RequestActive    = 0
	RequestFulfilled = 1
	RequestExpired   = 2
)

type CreateTransferRequest struct {
	Amount              Amount  `json:"amount"`
	Currency            string  `json:"currency"`
	Remark              *string `json:"remark"`
	WalletID            *string `json:"wallet_id"`
	ExpirationHours     int     `json:"expiration_hours"`
	Freeze              bool    `json:"freeze"`
	RequireConfirmation bool    `json:"require_confirmation"`
}
type TransferRequestRecord struct {
	ID                  string     `json:"id"`
	Status              int        `json:"status"`
	Currency            string     `json:"currency"`
	Amount              Amount     `json:"amount"`
	Remark              *string    `json:"remark"`
	Freeze              bool       `json:"freeze"`
	RequireConfirmation bool       `json:"require_confirmation"`
	ExpiresAt           time.Time  `json:"expires_at"`
	FulfilledAt         *time.Time `json:"fulfilled_at"`
	PayeeWalletID       string     `json:"payee_wallet_id"`
	PayeePublicID       *string    `json:"payee_public_id"`
	TransactionID       *string    `json:"transaction_id"`
}
type TransferRequestRepository interface {
	CreateTransferRequest(context.Context, string, Owner, string, CreateTransferRequest) (TransferRequestRecord, error)
	PublicTransferRequest(context.Context, string) (TransferRequestRecord, error)
	ResolveTransferRequest(context.Context, string, string) (TransferRequestRecord, error)
}

func ValidateTransferRequest(q CreateTransferRequest) error {
	if q.Amount.Sign() <= 0 {
		return ErrAmount
	}
	if q.ExpirationHours < 1 || q.ExpirationHours > 168 || q.Currency == "" || utf8.RuneCountInString(q.Currency) > 128 || q.Currency != strings.TrimSpace(q.Currency) || strings.ContainsAny(q.Currency, "\x00\r\n") {
		return ErrInvalid
	}
	if q.Remark != nil && (utf8.RuneCountInString(*q.Remark) > 4096 || strings.ContainsRune(*q.Remark, 0)) {
		return ErrInvalid
	}
	return nil
}
func (s *Service) CreateTransferRequest(ctx context.Context, p Principal, q CreateTransferRequest) (TransferRequestRecord, error) {
	if e := s.ready(p); e != nil {
		return TransferRequestRecord{}, e
	}
	if e := s.permission(ctx, p, PermissionTransferRequests); e != nil {
		return TransferRequestRecord{}, e
	}
	if e := ValidateTransferRequest(q); e != nil {
		return TransferRequestRecord{}, e
	}
	repo, ok := s.Store.(TransferRequestRepository)
	if !ok {
		return TransferRequestRecord{}, ErrUnavailable
	}
	id := ""
	if q.WalletID != nil {
		id = *q.WalletID
		if !ValidID(id) {
			return TransferRequestRecord{}, ErrInvalid
		}
	}
	w, e := s.Get(ctx, p, id)
	if e != nil {
		return TransferRequestRecord{}, e
	}
	if e = s.accessible(ctx, p, w, Editor); e != nil {
		return TransferRequestRecord{}, e
	}
	if w.PublicID == nil || *w.PublicID == "" {
		return TransferRequestRecord{}, ErrInvalid
	}
	if q.Remark != nil {
		v := strings.TrimSpace(*q.Remark)
		if v == "" {
			q.Remark = nil
		} else {
			q.Remark = &v
		}
	}
	return repo.CreateTransferRequest(ctx, p.AccountID, w.Owner(), w.ID, q)
}
