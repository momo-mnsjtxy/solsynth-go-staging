// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"crypto/sha256"
	"encoding/hex"
)

const PaymentTransfer = "wallet.transfer"
const PaymentOrder = "commerce.order.pay"

func OrderPaymentBinding(id string, payer *string) string {
	wallet := ""
	if payer != nil {
		wallet = *payer
	}
	h := sha256.Sum256([]byte("owned-order-payment-v1\x00" + id + "\x00" + wallet))
	return hex.EncodeToString(h[:])
}
