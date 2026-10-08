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
