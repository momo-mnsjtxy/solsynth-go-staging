// SPDX-License-Identifier: AGPL-3.0-only
package namecards

import (
	"context"
	"errors"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
	"unicode/utf8"
)

const Product = "passport.name-change-card"

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid        = &Failure{"NAME_CHANGE_CARD_INVALID", "Invalid name change card request.", 400}
	ErrPurchase       = &Failure{"NAME_CHANGE_CARD_PURCHASE_DISALLOWED", "Name change card purchase is not currently allowed.", 400}
	ErrUse            = &Failure{"NAME_CHANGE_CARD_USE_FAILED", "No usable card or the requested name is unavailable.", 400}
	ErrUnavailable    = &Failure{"NAME_CHANGE_CARD_UNAVAILABLE", "Name change card dependencies are unavailable.", 503}
	ErrNotFound       = &Failure{"NAME_CHANGE_CARD_NOT_FOUND", "Name change card not found.", 404}
	ErrIdempotency    = &Failure{"NAME_CHANGE_CARD_IDEMPOTENCY_CONFLICT", "The request key was already used with different input.", 409}
	ErrActorMigration = &Failure{"NAME_CHANGE_CARD_FEDERATION_RENAME_UNSUPPORTED", "Renaming a registered federation publisher requires an actor URI migration.", 501}
)

func Normalize(e error) error {
	var f *Failure
	var a *authority.Failure
	if errors.As(e, &f) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

type Purchase struct {
	ID          string        `json:"id"`
	AccountID   string        `json:"account_id"`
	OrderID     string        `json:"order_id"`
	Amount      wallet.Amount `json:"amount"`
	FulfilledAt *time.Time    `json:"fulfilled_at"`
	ConsumedAt  *time.Time    `json:"consumed_at"`
	TargetType  *string       `json:"target_type"`
	TargetID    *string       `json:"target_id"`
	OldName     *string       `json:"old_name"`
	NewName     *string       `json:"new_name"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}
type OrderResponse struct {
	PurchaseID string        `json:"purchase_id"`
	OrderID    string        `json:"order_id"`
	Amount     wallet.Amount `json:"amount"`
}
type UseRequest struct {
	Target   string  `json:"target"`
	TargetID *string `json:"target_id"`
	NewName  string  `json:"new_name"`
}

var canonicalName = regexp.MustCompile(`^[A-Za-z0-9_-]{2,256}$`)

func ValidateUse(q *UseRequest) error {
	q.NewName = strings.TrimSpace(q.NewName)
	if q.NewName == "" || len(q.NewName) > 256 || !utf8.ValidString(q.NewName) || strings.ContainsRune(q.NewName, 0) {
		return ErrInvalid
	}
	if q.TargetID != nil && !authority.ValidID(*q.TargetID) {
		return ErrInvalid
	}
	switch q.Target {
	case "account":
		if !canonicalName.MatchString(q.NewName) || authority.ValidID(q.NewName) {
			return ErrInvalid
		}
		switch strings.ToLower(q.NewName) {
		case "me", "verify", "verification":
			return ErrInvalid
		}
	case "publisher", "realm":
		if q.TargetID == nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func ValidKey(key string) bool {
	if len(key) > 128 || !utf8.ValidString(key) {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

type Repository interface {
	Purchase(context.Context, string, string) (OrderResponse, error)
	List(context.Context, string, int, int) ([]Purchase, int64, error)
	Use(context.Context, string, UseRequest, string) (Purchase, error)
}
