// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"encoding/json"
	"math/big"
	"regexp"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"time"
	"unicode/utf8"
)

var keyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func ValidKey(s string) bool { return keyRE.MatchString(s) }
func text(s string, n int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= n
}
func ExactPrice(n json.Number) (wallet.Amount, error) {
	v, e := wallet.ParseAmount(string(n))
	if e != nil || v.Sign() <= 0 {
		return v, ErrInvalid
	}
	a, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return v, ErrInvalid
	}
	b, _ := new(big.Rat).SetString(v.String())
	if a.Cmp(b) != 0 {
		return v, ErrInvalid
	}
	return v, nil
}
func ValidateDefinition(v DefinitionInput) (commerce.Definition, error) {
	if v.GroupIdentifier == "" {
		v.GroupIdentifier = v.Identifier
	}
	d := commerce.Definition{}
	if !ValidKey(v.Identifier) || !ValidKey(v.GroupIdentifier) || !text(v.DisplayName, 1024) || strings.TrimSpace(v.DisplayName) == "" || !ValidKey(v.Currency) || len(v.Currency) > 128 || v.CycleDurationDays < 1 || v.CycleDurationDays > 366 || v.FreeTrialDays < 0 || v.FreeTrialDays > 366 || !authority.ValidID(v.PayeeWalletID) {
		return d, ErrInvalid
	}
	price, e := ExactPrice(v.BasePrice)
	if e != nil {
		return d, e
	}
	d = commerce.Definition{Identifier: v.Identifier, GroupIdentifier: v.GroupIdentifier, DisplayName: v.DisplayName, Currency: v.Currency, BasePrice: price, PayeeWalletID: v.PayeeWalletID, CycleDurationDays: v.CycleDurationDays, FreeTrialDays: v.FreeTrialDays, AllowedPaymentMethods: v.AllowedPaymentMethods, ProviderMappings: v.ProviderMappings}
	if v.AllowGift != nil {
		d.AllowGift = *v.AllowGift
	}
	if v.AllowInternalWalletRenewal != nil {
		d.AllowInternalWalletRenewal = *v.AllowInternalWalletRenewal
	}
	if v.PaymentPolicy != nil {
		if v.AllowedPaymentMethods != nil || v.AllowInternalWalletRenewal != nil {
			return d, ErrInvalid
		}
		d.AllowedPaymentMethods = v.PaymentPolicy.AllowedMethods
		d.AllowInternalWalletRenewal = v.PaymentPolicy.AllowInternalWalletRenewal
		for _, method := range d.AllowedPaymentMethods {
			if method == "solian.wallet" && !v.PaymentPolicy.AllowInternalWallet || method != "solian.wallet" && !v.PaymentPolicy.AllowExternal {
				return d, ErrInvalid
			}
		}
	}
	if v.GiftPolicy != nil {
		if v.AllowGift != nil {
			return d, ErrInvalid
		}
		d.AllowGift = v.GiftPolicy.AllowPurchase
	}
	if len(d.AllowedPaymentMethods) < 1 || len(d.AllowedPaymentMethods) > 16 {
		return d, ErrInvalid
	}
	seen := map[string]bool{}
	for _, method := range d.AllowedPaymentMethods {
		if !ValidKey(method) || seen[method] {
			return d, ErrInvalid
		}
		seen[method] = true
	}
	if d.AllowInternalWalletRenewal && !seen["solian.wallet"] {
		return d, ErrInvalid
	}
	if len(d.ProviderMappings) > 16 {
		return d, ErrInvalid
	}
	for provider, ids := range d.ProviderMappings {
		if !ValidKey(provider) || len(ids) > 100 {
			return d, ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if !text(id, 256) || strings.TrimSpace(id) == "" || seen[id] {
				return d, ErrInvalid
			}
			seen[id] = true
		}
	}
	if d.ProviderMappings == nil {
		d.ProviderMappings = map[string][]string{}
	}
	return d, nil
}
func NormalizeQuery(q Query, now time.Time) (Query, error) {
	if q.Take == 0 {
		q.Take = 20
	}
	if q.Take < 1 || q.Take > 100 || q.Offset < 0 || q.Offset > 100000 || q.Status != nil && (*q.Status < 0 || *q.Status > 4) || q.Currency != "" && (!ValidKey(q.Currency) || len(q.Currency) > 128) || q.AppID != "" && !authority.ValidID(q.AppID) || q.ProductIdentifier != "" && !ValidKey(q.ProductIdentifier) {
		return q, ErrInvalid
	}
	from, to := now.UTC().Add(-30*24*time.Hour), now.UTC()
	if q.To != nil {
		to = q.To.UTC()
	}
	if q.From != nil {
		from = q.From.UTC()
	} else if q.To != nil {
		from = to.Add(-30 * 24 * time.Hour)
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return q, ErrInvalid
	}
	q.From = &from
	q.To = &to
	return q, nil
}
