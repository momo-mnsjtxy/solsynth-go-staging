// SPDX-License-Identifier: AGPL-3.0-only
package livestreams

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"solsynth.local/go-migration/ports/passport/accounts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

const AwardProduct = "livestreams.award"
const AwardDistributionProduct = "livestreams.award.distribution"

var ErrAwardRetry = &Failure{"LIVE_STREAM_AWARD_RETRY", "A concurrent account, stream or wallet change requires a retry.", 409}
var ErrAwardUnavailable = &Failure{"LIVE_STREAM_AWARDS_UNAVAILABLE", "Paid awards are not configured for this stream.", 503}

// AwardPoints accepts a JSON number only, preserving exact decimal value before
// entering the canonical millipoint ledger. It never silently truncates input.
type AwardPoints struct{ value wallet.Amount }

func ParseAwardPoints(raw string) (AwardPoints, error) {
	a, e := wallet.ParseAmount(raw)
	if e != nil || a.Sign() <= 0 {
		return AwardPoints{}, ErrInvalid
	}
	input, ok := new(big.Rat).SetString(raw)
	if !ok {
		return AwardPoints{}, ErrInvalid
	}
	exact, _ := new(big.Rat).SetString(a.String())
	if input.Cmp(exact) != 0 {
		return AwardPoints{}, ErrInvalid
	}
	units := new(big.Rat).Mul(exact, big.NewRat(1000, 1))
	if !units.IsInt() || new(big.Int).Mod(units.Num(), big.NewInt(10)).Sign() != 0 {
		return AwardPoints{}, ErrInvalid
	}
	return AwardPoints{a}, nil
}
func (p *AwardPoints) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || raw[0] == '"' || bytes.Equal(raw, []byte("null")) || !json.Valid(raw) {
		return ErrInvalid
	}
	v, e := ParseAwardPoints(string(raw))
	if e == nil {
		*p = v
	}
	return e
}
func (p AwardPoints) MarshalJSON() ([]byte, error) { return p.value.MarshalJSON() }
func (p AwardPoints) Amount() wallet.Amount        { return p.value }
func (p AwardPoints) Share() (wallet.Amount, error) {
	n, ok := new(big.Rat).SetString(p.value.String())
	if !ok || n.Sign() <= 0 {
		return wallet.Amount{}, ErrInvalid
	}
	n.Mul(n, big.NewRat(9, 10))
	return wallet.ParseAmount(n.FloatString(3))
}
func (p AwardPoints) HighlightSeconds() (int64, error) {
	n, ok := new(big.Rat).SetString(p.value.String())
	if !ok {
		return 0, ErrInvalid
	}
	n.Mul(n, big.NewRat(2, 1))
	whole := new(big.Int).Quo(n.Num(), n.Denom())
	if !whole.IsInt64() {
		return 0, ErrInvalid
	}
	return whole.Int64(), nil
}

type AwardRequest struct {
	Amount  AwardPoints `json:"amount"`
	Message *string     `json:"message"`
}
type AwardOrderResponse struct {
	OrderID string `json:"order_id"`
}
type AwardRefundRequest struct {
	Reason string `json:"reason"`
}
type AwardRefund struct {
	PurchaseID  string     `json:"purchase_id"`
	State       string     `json:"state"`
	Reason      string     `json:"reason"`
	RequestedAt time.Time  `json:"requested_at"`
	RefundedAt  *time.Time `json:"refunded_at"`
}
type Award struct {
	ID           string        `json:"id"`
	Amount       wallet.Amount `json:"amount"`
	Attitude     int           `json:"attitude"`
	Message      *string       `json:"message"`
	LiveStreamID string        `json:"live_stream_id"`
	AccountID    string        `json:"account_id"`
	SenderName   string        `json:"sender_name"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	DeletedAt    *time.Time    `json:"deleted_at"`
}
type AwardLeader struct {
	Rank        int               `json:"rank"`
	AccountID   string            `json:"account_id"`
	TotalAmount wallet.Amount     `json:"total_amount"`
	AwardCount  int               `json:"award_count"`
	Account     *accounts.Account `json:"account"`
}
type AwardRepository interface {
	CreateAwardOrder(context.Context, string, string, string, AwardRequest) (AwardOrderResponse, error)
	ListAwards(context.Context, string, string, int, int, bool) ([]Award, int, error)
	AwardLeaderboard(context.Context, string, string, int) ([]AwardLeader, error)
}
type AwardReconciliationRepository interface {
	RequestAwardRefund(context.Context, string, string, string, AwardRefundRequest) (AwardRefund, error)
}
