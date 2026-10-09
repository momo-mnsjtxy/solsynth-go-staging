// SPDX-License-Identifier: AGPL-3.0-only
package postawards

import (
	"context"
	"math/big"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
	"unicode/utf8"
)

const Product = "posts.award"
const DistributionProduct = "posts.award.distribution"
const Currency = "points"
const MerchantFull = "merchant_full_v1"
const Documented80 = "documented_80_v1"
const Positive = 0
const Negative = 2

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"POST_AWARD_INVALID", "The award request is invalid.", 400}
	ErrForbidden   = &Failure{"POST_AWARD_FORBIDDEN", "The award is not permitted.", 403}
	ErrNotFound    = &Failure{"POST_AWARD_NOT_FOUND", "The requested resource was not found.", 404}
	ErrConflict    = &Failure{"POST_AWARD_CONFLICT", "The award state changed.", 409}
	ErrRetry       = &Failure{"POST_AWARD_RETRY_REQUIRED", "Retry the same operation after contention.", 409}
	ErrUnavailable = &Failure{"POST_AWARD_UNAVAILABLE", "Post awards are not configured or available.", 503}
	ErrWallet      = &Failure{"WALLET_MERCHANT_NO_PAYMENT_WALLET", "Configure a matching merchant payout wallet first.", 409}
)

type Request struct {
	Amount   wallet.Amount `json:"amount"`
	Attitude int           `json:"attitude"`
	Message  *string       `json:"message"`
}

func (q Request) Validate(max wallet.Amount) error {
	if q.Amount.Sign() <= 0 || q.Amount.Cmp(max) > 0 || (q.Attitude != Positive && q.Attitude != Negative) || q.Message != nil && (!utf8.ValidString(*q.Message) || strings.ContainsRune(*q.Message, 0) || utf8.RuneCountInString(*q.Message) > 4096) {
		return ErrInvalid
	}
	return nil
}

// Share floors only the explicitly selected documented 80% alternative to the
// canonical mill. The executable merchant settlement contract credits 100%.
func Share(amount wallet.Amount, attitude int, policy string) (wallet.Amount, error) {
	if amount.Sign() <= 0 || (attitude != Positive && attitude != Negative) || (policy != MerchantFull && policy != Documented80) {
		return wallet.Amount{}, ErrInvalid
	}
	if attitude == Negative {
		return wallet.ParseAmount("0")
	}
	if policy == MerchantFull {
		return amount, nil
	}
	n, ok := new(big.Rat).SetString(amount.String())
	if !ok {
		return wallet.Amount{}, ErrInvalid
	}
	n.Mul(n, big.NewRat(800, 1))
	units := new(big.Int).Quo(n.Num(), n.Denom())
	return wallet.ParseAmount(new(big.Rat).SetFrac(units, big.NewInt(1000)).FloatString(3))
}

type OrderResponse struct {
	OrderID string `json:"order_id"`
}
type Award struct {
	ID        string        `json:"id"`
	PostID    string        `json:"post_id"`
	AccountID string        `json:"account_id"`
	Amount    wallet.Amount `json:"amount"`
	Attitude  int           `json:"attitude"`
	Message   *string       `json:"message"`
	CreatedAt time.Time     `json:"created_at"`
	SettledAt *time.Time    `json:"settled_at"`
}
type Pending struct {
	Count        int           `json:"count"`
	TotalAmount  wallet.Amount `json:"total_amount"`
	PayoutAmount wallet.Amount `json:"payout_amount"`
}
type Merchant struct {
	PublisherID     string    `json:"publisher_id"`
	PaymentWalletID *string   `json:"payment_wallet_id"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type WalletPatch struct {
	WalletID *string `json:"wallet_id"`
}
type Page struct{ Offset, Take int }

func (p Page) Checked() (Page, error) {
	if p.Take == 0 {
		p.Take = 20
	}
	if p.Take < 1 || p.Take > 100 || p.Offset < 0 || p.Offset > 100000 {
		return p, ErrInvalid
	}
	return p, nil
}

type Settlement struct {
	AwardID         string        `json:"award_id"`
	PostID          string        `json:"post_id"`
	OrderID         string        `json:"order_id"`
	PaymentWalletID string        `json:"payment_wallet_id"`
	Currency        string        `json:"currency"`
	Amount          wallet.Amount `json:"amount"`
	Policy          string        `json:"policy"`
	DueAt           time.Time     `json:"due_at"`
	SettledAt       *time.Time    `json:"settled_at"`
}
type Repository interface {
	CreateOrder(context.Context, string, string, string, Request) (OrderResponse, error)
	List(context.Context, string, string, Page) ([]Award, int, error)
	Pending(context.Context, string, string) (Pending, error)
	Merchant(context.Context, string, string) (Merchant, error)
	BindWallet(context.Context, string, string, WalletPatch) (Merchant, error)
	Settlements(context.Context, string, string, Page) ([]Settlement, int, error)
	SettleMerchant(context.Context, string, string, int) (int, error)
	SettlePublisherRewards(context.Context, string, int) (int, error)
	SettlePublisherAwards(context.Context, string, int) (int, error)
	ResettlePublisherRewards(context.Context, string, ResettleRequest, int) (int, error)
}
type ResettleRequest struct {
	DateFrom    time.Time `json:"date_from"`
	DateTo      time.Time `json:"date_to"`
	PublisherID *string   `json:"publisher_id"`
}
