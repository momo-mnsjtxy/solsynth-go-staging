// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"crypto/rand"
	"io"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const PermissionFunds = "wallets.funds.manage"
const (
	FundCreated   = 0
	FundPartial   = 1
	FundCompleted = 2
	FundExpired   = 3
	FundRefunded  = 4
)
const (
	FundEven          = 0
	FundRandom        = 1
	ContributionFree  = 0
	ContributionFixed = 1
)
const (
	PaymentFundCreate     = "wallet.fund.create"
	PaymentFundContribute = "wallet.fund.contribute"
)

type CreateFundRequest struct {
	RecipientAccountIDs []string   `json:"recipient_account_ids"`
	Currency            string     `json:"currency"`
	TotalAmount         Amount     `json:"total_amount"`
	AmountOfSplits      int        `json:"amount_of_splits"`
	SplitType           int        `json:"split_type"`
	Message             *string    `json:"message"`
	ExpirationHours     *int       `json:"expiration_hours"`
	PINCode             *string    `json:"pin_code"`
	PayerWalletID       *string    `json:"payer_wallet_id"`
	IsRaising           bool       `json:"is_raising"`
	TargetAmount        Amount     `json:"target_amount"`
	ContributionType    int        `json:"contribution_type"`
	ContributionAmount  Amount     `json:"contribution_amount"`
	IsOpen              bool       `json:"is_open"`
	DeadlineAt          *time.Time `json:"deadline_at"`
}
type ContributeFundRequest struct {
	Amount Amount `json:"amount"`
	// Owned extension: existing amount-only calls work only when current canonical
	// policy explicitly permits payment without a PIN. No bearer-age substitution.
	PINCode       *string `json:"pin_code"`
	PayerWalletID *string `json:"payer_wallet_id"`
}
type FundRecipient struct {
	ID                 string     `json:"id"`
	FundID             string     `json:"fund_id"`
	RecipientAccountID string     `json:"recipient_account_id"`
	Amount             Amount     `json:"amount"`
	IsReceived         bool       `json:"is_received"`
	ReceivedAt         *time.Time `json:"received_at"`
	TransactionID      *string    `json:"transaction_id,omitempty"`
}
type Fund struct {
	ID                 string          `json:"id"`
	CreatorAccountID   string          `json:"creator_account_id"`
	Currency           string          `json:"currency"`
	TotalAmount        Amount          `json:"total_amount"`
	RemainingAmount    Amount          `json:"remaining_amount"`
	RaisedAmount       Amount          `json:"raised_amount"`
	AmountOfSplits     int             `json:"amount_of_splits"`
	SplitType          int             `json:"split_type"`
	Message            *string         `json:"message"`
	Status             int             `json:"status"`
	IsOpen             bool            `json:"is_open"`
	IsRaising          bool            `json:"is_raising"`
	TargetAmount       Amount          `json:"target_amount"`
	ContributionType   int             `json:"contribution_type"`
	ContributionAmount Amount          `json:"contribution_amount"`
	DeadlineAt         *time.Time      `json:"deadline_at"`
	ExpiredAt          time.Time       `json:"expired_at"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Recipients         []FundRecipient `json:"recipients"`
	// Captured creation-time wallet, never a caller-supplied closing beneficiary.
	BeneficiaryWalletID string `json:"beneficiary_wallet_id"`
}
type FundCommand struct {
	ActorID, WalletID, IdempotencyKey, Fingerprint string
	Request                                        CreateFundRequest
}
type FundContributionCommand struct {
	ActorID, FundID, WalletID, Fingerprint string
	Amount                                 Amount
}
type FundRepository interface {
	CreateFund(context.Context, FundCommand) (Fund, error)
	Fund(context.Context, string, string) (Fund, error)
	Funds(context.Context, string, int, int, *int) ([]Fund, int, error)
	ReceiveFund(context.Context, string, string) (Transaction, error)
	ContributeFund(context.Context, FundContributionCommand) (Transaction, error)
	CancelFund(context.Context, string, string) (Fund, error)
}

// NormalizeFund retains the evidenced fields while imposing bounded, coherent
// immutable terms. The current creator client already sends three-digit-safe
// numeric amounts, recipient UUIDs and a selected payer wallet when available.
func NormalizeFund(q CreateFundRequest, now time.Time) (CreateFundRequest, error) {
	if q.Currency == "" || utf8.RuneCountInString(q.Currency) > 128 || q.Currency != strings.TrimSpace(q.Currency) || strings.ContainsAny(q.Currency, "\x00\r\n") || q.AmountOfSplits < 1 || q.AmountOfSplits > 512 || len(q.RecipientAccountIDs) > 512 || q.SplitType < 0 || q.SplitType > 1 {
		return q, ErrInvalid
	}
	if q.Message != nil {
		v := strings.TrimSpace(*q.Message)
		if utf8.RuneCountInString(v) > 4096 || strings.ContainsRune(v, 0) {
			return q, ErrInvalid
		}
		if v == "" {
			q.Message = nil
		} else {
			q.Message = &v
		}
	}
	hours := 24
	if q.ExpirationHours != nil {
		hours = *q.ExpirationHours
	}
	if hours < 1 || hours > 168 {
		return q, ErrInvalid
	}
	q.ExpirationHours = &hours
	ids := append([]string(nil), q.RecipientAccountIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if !ValidID(id) || i > 0 && ids[i-1] == id {
			return q, ErrInvalid
		}
	}
	q.RecipientAccountIDs = ids
	if q.PayerWalletID != nil && !ValidID(*q.PayerWalletID) {
		return q, ErrInvalid
	}
	if q.PINCode != nil && len(*q.PINCode) > 256 {
		return q, ErrPIN
	}
	if q.DeadlineAt != nil {
		deadline := q.DeadlineAt.UTC().Truncate(time.Microsecond)
		if !now.IsZero() && (!deadline.After(now) || deadline.After(now.Add(time.Duration(hours)*time.Hour))) {
			return q, ErrInvalid
		}
		q.DeadlineAt = &deadline
	}
	if q.IsRaising {
		if q.TargetAmount.Sign() <= 0 || q.ContributionType < 0 || q.ContributionType > 1 || q.ContributionType == ContributionFixed && (q.ContributionAmount.Sign() <= 0 || q.ContributionAmount.Cmp(q.TargetAmount) > 0) {
			return q, ErrAmount
		}
		if !q.IsOpen && (len(ids) == 0 || len(ids) > q.AmountOfSplits) {
			return q, ErrInvalid
		}
		q.TotalAmount = q.TargetAmount
		q.SplitType = FundEven
		if q.ContributionType == ContributionFree {
			q.ContributionAmount = Amount{}
		}
	} else {
		if q.TotalAmount.Sign() <= 0 {
			return q, ErrAmount
		}
		q.IsOpen = len(ids) == 0
		slots := q.AmountOfSplits
		if !q.IsOpen {
			slots = len(ids)
		}
		if q.TotalAmount.units().Cmp(big.NewInt(int64(slots))) < 0 {
			return q, ErrAmount
		}
		q.TargetAmount = Amount{}
		q.ContributionType = ContributionFree
		q.ContributionAmount = Amount{}
		q.DeadlineAt = nil
	}
	return q, nil
}

// FundShare computes exact mills with no float rounding or fabricated cents.
// Every remaining slot retains at least one mill, and the final slot consumes
// precisely the remaining backed amount. The source cent-overallocation defect
// is intentionally not reproduced.
func FundShare(remaining Amount, slots, kind int, entropy io.Reader) (Amount, error) {
	if slots < 1 || slots > 512 || kind < 0 || kind > 1 || remaining.units().Cmp(big.NewInt(int64(slots))) < 0 {
		return Amount{}, ErrAmount
	}
	if slots == 1 {
		return remaining, nil
	}
	n := remaining.units()
	den := big.NewInt(int64(slots))
	if kind == FundEven {
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(n, den, r)
		if r.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		}
		return fromUnits(q), nil
	}
	low := new(big.Int).Quo(n, big.NewInt(int64(2*slots)))
	if low.Sign() == 0 {
		low.SetInt64(1)
	}
	high := new(big.Int).Sub(n, new(big.Int).Mul(big.NewInt(int64(slots-1)), low))
	width := new(big.Int).Add(new(big.Int).Sub(high, low), big.NewInt(1))
	if entropy == nil {
		entropy = rand.Reader
	}
	v, e := rand.Int(entropy, width)
	if e != nil {
		return Amount{}, e
	}
	return fromUnits(v.Add(v, low)), nil
}
