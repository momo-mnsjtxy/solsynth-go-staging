// SPDX-License-Identifier: AGPL-3.0-only
package realmboosts

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/passport/accounts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strconv"
	"strings"
	"time"
)

const Product = "realms.boost"
const ExpirationDays = 30
const MaxShares = 1000000

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (f *Failure) Error() string { return f.Code }

var (
	ErrInvalid     = &Failure{"REALM_BOOST_INVALID", "Invalid realm boost request.", 400}
	ErrCurrency    = &Failure{"REALM_BAD_BOOST_CURRENCY", "Unsupported boost currency. Use golds or points.", 400}
	ErrNotFound    = &Failure{"PASSPORT_REALM_NOT_FOUND", "Realm not found.", 404}
	ErrForbidden   = &Failure{"REALM_BOOST_FORBIDDEN", "Active realm membership and permission are required.", 403}
	ErrUnavailable = &Failure{"REALM_BOOST_UNAVAILABLE", "Realm boosts require an explicit owned policy and payment adapters.", 503}
	ErrConflict    = &Failure{"REALM_BOOST_CONFLICT", "Realm boost state changed.", 409}
	ErrRetry       = &Failure{"REALM_BOOST_RETRY_REQUIRED", "Contention rolled back the transaction; retry the same request.", 409}
)

type Request struct {
	Shares   int64   `json:"shares"`
	Currency *string `json:"currency"`
}
type OrderResponse struct {
	OrderID  string        `json:"order_id"`
	Shares   int64         `json:"shares"`
	Currency string        `json:"currency"`
	Amount   wallet.Amount `json:"amount"`
}
type Status struct {
	BoostPoints         int64    `json:"boost_points"`
	BoostLevel          int      `json:"boost_level"`
	LabelCap            int      `json:"label_cap"`
	ExpiresAfterDays    int      `json:"expires_after_days"`
	SupportedCurrencies []string `json:"supported_currencies"`
	DefaultCurrency     string   `json:"default_currency"`
}
type Entry struct {
	AccountID     string            `json:"account_id"`
	Account       *accounts.Account `json:"account"`
	AmountGolds   wallet.Amount     `json:"amount_golds"`
	AmountPoints  wallet.Amount     `json:"amount_points"`
	Shares        int64             `json:"shares"`
	Boosts        int64             `json:"boosts"`
	LastBoostedAt time.Time         `json:"last_boosted_at"`
}
type Experience struct {
	ID         string     `json:"id"`
	RealmID    string     `json:"realm_id"`
	AccountID  string     `json:"account_id"`
	ReasonType string     `json:"reason_type"`
	Reason     string     `json:"reason"`
	Delta      int64      `json:"delta"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at"`
}
type Repository interface {
	CreateOrder(context.Context, string, string, string, Request) (OrderResponse, error)
	Status(context.Context, string, string) (Status, error)
	Leaderboard(context.Context, string, string, int) ([]Entry, error)
	Experience(context.Context, string, string, string, int) ([]Experience, error)
}
type Auth func(*http.Request, bool) (string, error)

// Normalize is an owned bounded ASCII spelling policy, not the excluded shared implementation.
func Normalize(currency *string) (string, error) {
	if currency == nil || strings.TrimSpace(*currency) == "" {
		return "golds", nil
	}
	if len(*currency) > 128 {
		return "", ErrCurrency
	}
	s := strings.ToLower(strings.TrimSpace(*currency))
	if s != "golds" && s != "points" {
		return "", ErrCurrency
	}
	return s, nil
}
func Price(shares int64, currency string) (wallet.Amount, error) {
	if shares < 1 || shares > MaxShares {
		return wallet.Amount{}, ErrInvalid
	}
	rate := int64(1)
	if currency == "points" {
		rate = 1000
	} else if currency != "golds" {
		return wallet.Amount{}, ErrCurrency
	}
	return wallet.ParseAmount(strconv.FormatInt(rate*shares, 10))
}
func Level(shares int64) int {
	switch {
	case shares >= 50:
		return 3
	case shares >= 25:
		return 2
	case shares >= 10:
		return 1
	default:
		return 0
	}
}
func BoostXP(shares int64, perk int) (int64, error) {
	if shares < 1 || shares > MaxShares || perk < 0 || perk > 3 {
		return 0, ErrInvalid
	}
	twice := int64(2)
	if perk > 0 {
		twice = int64(perk + 2)
	}
	return shares * 100 * twice / 2, nil
}
