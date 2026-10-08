// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import (
	"encoding/json"
	"math/big"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
)

// Prices retain their exact decimal representation. Only the complete order
// total is truncated to canonical wallet thousandths, matching PaymentService.
func ValidPrice(n json.Number) bool {
	if _, e := wallet.ParseAmount(string(n)); e != nil {
		return false
	}
	r, ok := new(big.Rat).SetString(string(n))
	return ok && r.Sign() > 0
}
func Total(items []OrderItem) (wallet.Amount, error) {
	r := new(big.Rat)
	for _, item := range items {
		if !ValidPrice(item.UnitPrice) || item.Quantity < 1 || item.Quantity > 1000 {
			return wallet.Amount{}, ErrInvalid
		}
		price, _ := new(big.Rat).SetString(string(item.UnitPrice))
		price.Mul(price, new(big.Rat).SetInt64(int64(item.Quantity)))
		r.Add(r, price)
	}
	n := new(big.Int).Mul(r.Num(), big.NewInt(1000))
	n.Quo(n, r.Denom())
	s := n.String()
	if len(s) < 4 {
		s = strings.Repeat("0", 4-len(s)) + s
	}
	return wallet.ParseAmount(s[:len(s)-3] + "." + s[len(s)-3:])
}

// CyclePrice is an independently authored bounded policy for the retained
// client's 1..12 cycle purchase control. It accepts whole cycles only and uses
// the exact product unit price before truncating the complete purchase once.
func CyclePrice(d Definition, requested *int) (int, wallet.Amount, error) {
	days := d.CycleDurationDays
	if requested != nil {
		days = *requested
	}
	if d.CycleDurationDays < 1 || days < 1 || days > 366 || days%d.CycleDurationDays != 0 || days/d.CycleDurationDays > 12 {
		return 0, wallet.Amount{}, ErrInvalid
	}
	price := d.UnitPrice
	if price == "" {
		price = json.Number(d.BasePrice.String())
	}
	total, e := Total([]OrderItem{{UnitPrice: price, Quantity: days / d.CycleDurationDays}})
	if e != nil || total.Sign() <= 0 {
		return 0, wallet.Amount{}, ErrInvalid
	}
	return days, total, nil
}
func (q *ItemRequest) UnmarshalJSON(b []byte) error {
	var raw struct {
		ProductIdentifier string `json:"product_identifier"`
		Quantity          *int   `json:"quantity"`
	}
	if e := json.Unmarshal(b, &raw); e != nil {
		return e
	}
	q.ProductIdentifier = raw.ProductIdentifier
	q.Quantity = 1
	if raw.Quantity != nil {
		q.Quantity = *raw.Quantity
	}
	return nil
}
