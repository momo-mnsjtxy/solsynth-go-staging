// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const PaymentExchange = "wallet.exchange"

// Explicit operator economics. Rate operands are exact decimal strings; there
// are no defaults, inferred fees, treasury selection or balance provisioning.
type ExchangeRate struct {
	SourceCurrency string `json:"source_currency"`
	SourceAmount   string `json:"source_amount"`
	TargetCurrency string `json:"target_currency"`
	TargetAmount   string `json:"target_amount"`
}
type ExchangeConfig struct {
	Version           string         `json:"version"`
	TreasuryAccountID string         `json:"treasury_account_id"`
	TreasuryWalletID  string         `json:"treasury_wallet_id"`
	Rates             []ExchangeRate `json:"rates"`
}
type ExchangePolicy struct {
	config ExchangeConfig
	hash   string
	rates  map[string]ExchangeRate
}

var exchangeCurrency = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
var exchangeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var exchangeDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,28})?$`)

func exchangeOperand(s string) (*big.Rat, bool) {
	if len(s) > 64 || !exchangeDecimal.MatchString(s) {
		return nil, false
	}
	v, ok := new(big.Rat).SetString(s)
	max, _ := new(big.Rat).SetString("79228162514264337593543950335")
	return v, ok && v.Sign() > 0 && v.Cmp(max) <= 0
}
func NewExchangePolicy(c ExchangeConfig) (*ExchangePolicy, error) {
	if !exchangeVersion.MatchString(c.Version) || !ValidID(c.TreasuryAccountID) || !ValidID(c.TreasuryWalletID) || len(c.Rates) == 0 || len(c.Rates) > 32 {
		return nil, ErrInvalid
	}
	c.Rates = append([]ExchangeRate(nil), c.Rates...)
	p := &ExchangePolicy{rates: map[string]ExchangeRate{}}
	for i, r := range c.Rates {
		if !exchangeCurrency.MatchString(r.SourceCurrency) || !exchangeCurrency.MatchString(r.TargetCurrency) || strings.EqualFold(r.SourceCurrency, r.TargetCurrency) {
			return nil, ErrInvalid
		}
		if _, ok := exchangeOperand(r.SourceAmount); !ok {
			return nil, ErrAmount
		}
		if _, ok := exchangeOperand(r.TargetAmount); !ok {
			return nil, ErrAmount
		}
		// Canonicalize trailing zeroes so equivalent operator files have one digest.
		r.SourceAmount = canonicalExchangeDecimal(r.SourceAmount)
		r.TargetAmount = canonicalExchangeDecimal(r.TargetAmount)
		key := strings.ToLower(r.SourceCurrency)
		if _, ok := p.rates[key]; ok {
			return nil, ErrInvalid
		}
		p.rates[key] = r
		c.Rates[i] = r
	}
	sort.Slice(c.Rates, func(i, j int) bool { return c.Rates[i].SourceCurrency < c.Rates[j].SourceCurrency })
	p.config = c
	b, e := json.Marshal(c)
	if e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	p.hash = hex.EncodeToString(h[:])
	return p, nil
}
func canonicalExchangeDecimal(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
func (p *ExchangePolicy) Config() ExchangeConfig {
	if p == nil {
		return ExchangeConfig{}
	}
	c := p.config
	c.Rates = append([]ExchangeRate(nil), c.Rates...)
	return c
}
func (p *ExchangePolicy) Digest() string {
	if p == nil {
		return ""
	}
	return p.hash
}

type ExchangeQuote struct {
	PolicyVersion     string `json:"policy_version"`
	PolicyHash        string `json:"policy_hash"`
	TreasuryAccountID string `json:"treasury_account_id"`
	TreasuryWalletID  string `json:"treasury_wallet_id"`
	SourceCurrency    string `json:"source_currency"`
	SourceAmount      Amount `json:"source_amount"`
	TargetCurrency    string `json:"target_currency"`
	TargetAmount      Amount `json:"target_amount"`
	RateSourceAmount  string `json:"rate_source_amount"`
	RateTargetAmount  string `json:"rate_target_amount"`
	// Exact nonnegative fraction of one target mill retained by the treasury.
	RemainderMills string `json:"remainder_mills"`
}

func (p *ExchangePolicy) Quote(currency string, amount Amount) (ExchangeQuote, error) {
	var q ExchangeQuote
	if p == nil {
		return q, ErrUnavailable
	}
	if amount.Sign() <= 0 {
		return q, ErrAmount
	}
	r, ok := p.rates[strings.ToLower(strings.TrimSpace(currency))]
	if !ok {
		return q, ErrInvalid
	}
	source, _ := exchangeOperand(r.SourceAmount)
	target, _ := exchangeOperand(r.TargetAmount)
	result := new(big.Rat).Mul(new(big.Rat).SetInt(amount.units()), new(big.Rat).Quo(target, source))
	units, remainder := new(big.Int), new(big.Int)
	units.QuoRem(result.Num(), result.Denom(), remainder)
	if units.Sign() <= 0 || units.Cmp(maxUnits) > 0 {
		return q, ErrAmount
	}
	return ExchangeQuote{p.config.Version, p.hash, p.config.TreasuryAccountID, p.config.TreasuryWalletID, r.SourceCurrency, amount, r.TargetCurrency, fromUnits(units), r.SourceAmount, r.TargetAmount, new(big.Rat).SetFrac(remainder, result.Denom()).RatString()}, nil
}

type ExchangeRequest struct {
	Amount   Amount  `json:"amount"`
	Currency string  `json:"currency"`
	WalletID *string `json:"wallet_id"`
	Remark   *string `json:"remark"`
	PINCode  *string `json:"pin_code"`
}
type ExchangeCommand struct {
	ActorID, WalletID, IdempotencyKey, Fingerprint string
	Owner                                          Owner
	Quote                                          ExchangeQuote
	Remark                                         *string
}
type ExchangeResponse struct {
	WalletID          string      `json:"wallet_id"`
	SourceCurrency    string      `json:"source_currency"`
	SourceAmount      Amount      `json:"source_amount"`
	TargetCurrency    string      `json:"target_currency"`
	TargetAmount      Amount      `json:"target_amount"`
	DebitTransaction  Transaction `json:"debit_transaction"`
	CreditTransaction Transaction `json:"credit_transaction"`
	PolicyVersion     string      `json:"policy_version"`
	RemainderMills    string      `json:"remainder_mills"`
}
type ExchangeRepository interface {
	QuoteExchange(string, Amount) (ExchangeQuote, error)
	Exchange(context.Context, ExchangeCommand) (ExchangeResponse, error)
}

func ExchangeBinding(c ExchangeCommand) string {
	b, _ := json.Marshal(struct {
		Actor, Wallet string
		Quote         ExchangeQuote
		Remark        *string
	}{c.ActorID, c.WalletID, c.Quote, c.Remark})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidExchangeKey(key string) bool {
	if len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key {
		return false
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func (s *Service) Exchange(ctx context.Context, p Principal, req ExchangeRequest, key string) (ExchangeResponse, error) {
	var out ExchangeResponse
	if e := s.ready(p); e != nil {
		return out, e
	}
	if e := s.permission(ctx, p, PermissionTransactions); e != nil {
		return out, e
	}
	store, ok := s.Store.(ExchangeRepository)
	if !ok {
		return out, ErrUnavailable
	}
	payments, ok := s.Payments.(PaymentAuthorizer)
	if !ok {
		return out, ErrUnavailable
	}
	if !ValidExchangeKey(key) || req.Remark != nil && (!utf8.ValidString(*req.Remark) || utf8.RuneCountInString(*req.Remark) > 4096 || strings.ContainsRune(*req.Remark, 0)) {
		return out, ErrInvalid
	}
	id := ""
	if req.WalletID != nil {
		id = *req.WalletID
		if !ValidID(id) {
			return out, ErrInvalid
		}
	}
	wallet, e := s.Get(ctx, p, id)
	if e != nil {
		return out, e
	}
	if e = s.accessible(ctx, p, wallet, Editor); e != nil {
		return out, e
	}
	quote, e := store.QuoteExchange(req.Currency, req.Amount)
	if e != nil {
		return out, e
	}
	if wallet.ID == quote.TreasuryWalletID || p.AccountID == quote.TreasuryAccountID {
		return out, ErrForbidden
	}
	command := ExchangeCommand{ActorID: p.AccountID, WalletID: wallet.ID, Owner: wallet.Owner(), IdempotencyKey: key, Quote: quote, Remark: req.Remark}
	command.Fingerprint = ExchangeBinding(command)
	pin := ""
	if req.PINCode != nil {
		pin = *req.PINCode
	}
	if len(pin) > 256 {
		return out, ErrPIN
	}
	ctx, e = payments.PreparePayment(ctx, p, pin, PaymentExchange, command.Fingerprint)
	if e != nil {
		return out, e
	}
	return store.Exchange(ctx, command)
}
