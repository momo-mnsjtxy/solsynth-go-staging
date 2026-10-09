// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
)

type PaymentAuthorization interface {
	LockPayment(context.Context, *sql.DB, *sql.Tx, string, string, string) error
	CheckPayment(context.Context, *sql.DB, *sql.Tx, string, string, string) error
}
type RenewalPaymentPolicy interface {
	AllowRenewal(context.Context, *sql.DB, *sql.Tx, string) (bool, error)
}

func (r *Repository) WithPaymentAuthorization(p PaymentAuthorization) *Repository {
	r.payments = p
	return r
}
