// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
)

// CouponPolicy is an owned opt-in contract, not a recovered legacy formula.
// Both fields must be explicitly configured; zero values disable coupons.
type CouponPolicy struct {
	Type         string `json:"type"`
	Cancellation string `json:"cancellation"`
}

const SellerFixedMills = "seller_fixed_mills_v1"
const RetainCouponUsage = "retain_usage"

func (p CouponPolicy) Valid() bool {
	return p.Type == SellerFixedMills && p.Cancellation == RetainCouponUsage
}
func (p CouponPolicy) Empty() bool { return p == (CouponPolicy{}) }

type CouponTerms struct {
	Type              string      `json:"type"`
	Scope             string      `json:"scope"`
	Currency          string      `json:"currency"`
	Discount          json.Number `json:"discount"`
	Products          []string    `json:"products"`
	Audience          string      `json:"audience"`
	Accounts          []string    `json:"accounts"`
	StartsAt          time.Time   `json:"starts_at"`
	ExpiresAt         time.Time   `json:"expires_at"`
	MaxUses           int         `json:"max_uses"`
	MaxUsesPerAccount int         `json:"max_uses_per_account"`
}
type Coupon struct {
	ID            string       `json:"id"`
	Identifier    string       `json:"identifier"`
	AppID         string       `json:"app_id"`
	PublisherID   string       `json:"publisher_id"`
	PayeeWalletID string       `json:"payee_wallet_id"`
	Terms         CouponTerms  `json:"terms"`
	Policy        CouponPolicy `json:"policy"`
	CreatedAt     time.Time    `json:"created_at"`
	Enabled       bool         `json:"enabled"`
}
type CreateCoupon struct {
	Credentials
	Identifier string      `json:"identifier"`
	Terms      CouponTerms `json:"terms"`
}
type RevokeCoupon struct{ Credentials }
type CouponQuote struct {
	CouponID          string        `json:"coupon_id"`
	Identifier        string        `json:"identifier"`
	TermsHash         string        `json:"terms_hash"`
	CheckoutTermsHash string        `json:"checkout_terms_hash"`
	Policy            CouponPolicy  `json:"policy"`
	AppID             string        `json:"app_id"`
	PublisherID       string        `json:"publisher_id"`
	PayeeWalletID     string        `json:"payee_wallet_id"`
	AccountID         string        `json:"account_id"`
	Scope             string        `json:"scope"`
	Products          []string      `json:"products"`
	Currency          string        `json:"currency"`
	Gross             wallet.Amount `json:"gross"`
	Discount          wallet.Amount `json:"discount"`
	Net               wallet.Amount `json:"net"`
	ExpiresAt         time.Time     `json:"expires_at"`
}
type CouponStore interface {
	CreateCoupon(context.Context, CreateCoupon, string) (Coupon, error)
	RevokeCoupon(context.Context, string, RevokeCoupon) (Coupon, error)
	ApplyOrderCoupon(context.Context, string, string, string, bool) (Order, error)
}

var couponCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)

func ValidCouponCode(s string) bool { return couponCode.MatchString(s) && !wallet.ValidID(s) }
func ExactDiscount(n json.Number) (wallet.Amount, error) {
	a, e := wallet.ParseAmount(string(n))
	if e != nil || a.Sign() <= 0 {
		return wallet.Amount{}, ErrInvalid
	}
	raw, ok := new(big.Rat).SetString(string(n))
	canonical, _ := new(big.Rat).SetString(a.String())
	if !ok || raw.Cmp(canonical) != 0 {
		return wallet.Amount{}, ErrInvalid
	}
	return a, nil
}
func (t CouponTerms) Validate() error {
	if t.Type != "fixed_mills" || (t.Scope != "order" && t.Scope != "subscription" && t.Scope != "gift") || strings.TrimSpace(t.Currency) == "" || len(t.Currency) > 128 || len(t.Products) < 1 || len(t.Products) > 100 || t.StartsAt.IsZero() || !t.ExpiresAt.After(t.StartsAt) || t.MaxUses < 1 || t.MaxUses > 1000000 || t.MaxUsesPerAccount < 1 || t.MaxUsesPerAccount > t.MaxUses {
		return ErrInvalid
	}
	if _, e := ExactDiscount(t.Discount); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, p := range t.Products {
		if strings.TrimSpace(p) == "" || len(p) > 256 || seen[p] {
			return ErrInvalid
		}
		seen[p] = true
	}
	if t.Audience == "all" {
		if len(t.Accounts) != 0 {
			return ErrInvalid
		}
	} else if t.Audience == "accounts" {
		if len(t.Accounts) < 1 || len(t.Accounts) > 1000 {
			return ErrInvalid
		}
		seen = map[string]bool{}
		for _, a := range t.Accounts {
			if !wallet.ValidID(a) || seen[a] {
				return ErrInvalid
			}
			seen[a] = true
		}
	} else {
		return ErrInvalid
	}
	return nil
}
func CouponFingerprint(c Coupon) (string, error) {
	// Enabled is a live admission control, not part of immutable quote economics.
	c.Enabled = false
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func QuoteCoupon(c Coupon, actor, app, payee, scope, currency string, products []string, gross wallet.Amount, now time.Time) (*CouponQuote, error) {
	if !c.Policy.Valid() || c.Terms.Validate() != nil || !c.Enabled || !wallet.ValidID(actor) || c.AppID != app || c.PayeeWalletID != payee || c.Terms.Scope != scope || c.Terms.Currency != currency || now.Before(c.Terms.StartsAt) || !now.Before(c.Terms.ExpiresAt) || len(products) == 0 {
		return nil, ErrForbidden
	}
	if c.Terms.Audience == "accounts" && !slices.Contains(c.Terms.Accounts, actor) {
		return nil, ErrForbidden
	}
	for _, p := range products {
		if !slices.Contains(c.Terms.Products, p) {
			return nil, ErrForbidden
		}
	}
	discount, e := ExactDiscount(c.Terms.Discount)
	if e != nil {
		return nil, e
	}
	net, e := gross.Sub(discount)
	if e != nil || net.Sign() <= 0 {
		return nil, ErrInvalid
	}
	hash, e := CouponFingerprint(c)
	if e != nil {
		return nil, e
	}
	return &CouponQuote{CouponID: c.ID, Identifier: c.Identifier, TermsHash: hash, Policy: c.Policy, AppID: c.AppID, PublisherID: c.PublisherID, PayeeWalletID: payee, AccountID: actor, Scope: scope, Products: slices.Clone(products), Currency: currency, Gross: gross, Discount: discount, Net: net, ExpiresAt: c.Terms.ExpiresAt}, nil
}
func (o Order) GrossAmount() wallet.Amount {
	if o.Coupon != nil {
		return o.Coupon.Gross
	}
	return o.Amount
}
func (s Subscription) GrossPrice() wallet.Amount {
	if s.Coupon != nil {
		return s.Coupon.Gross
	}
	return s.FinalPrice
}
