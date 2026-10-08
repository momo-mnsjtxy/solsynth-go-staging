// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

// ReservePostAwardShare uses the canonical hold primitive only after verifying a
// matching confirmed original payment and fixed internal distribution order.
// It reserves already-credited treasury funds; it cannot mint a nil-payer credit.
func ReservePostAwardShare(ctx context.Context, tx *sql.Tx, sourceOrder, sourceTransaction, distributionOrder, treasuryActor, treasury, beneficiary string, share accounts.Amount, now time.Time) (accounts.Transaction, error) {
	var out accounts.Transaction
	if tx == nil {
		return out, accounts.ErrInvalid
	}
	for _, id := range []string{sourceOrder, sourceTransaction, distributionOrder, treasuryActor, treasury, beneficiary} {
		if !accounts.ValidID(id) {
			return out, accounts.ErrInvalid
		}
	}
	var valid bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders original JOIN owned_post_award_purchases purchase ON purchase.order_id=original.id JOIN wallet_v1.transactions paid ON paid.id=$2::uuid JOIN commerce_v1.orders distribution ON distribution.id=$3::uuid JOIN wallet_v1.wallets treasury ON treasury.id=$5::uuid WHERE original.id=$1::uuid AND original.app_id IS NULL AND original.product_identifier='posts.award' AND original.subscription_id IS NULL AND original.gift_id IS NULL AND paid.managed_by=original.id AND paid.purpose='commerce' AND paid.status=2 AND paid.confirmed_at IS NOT NULL AND paid.deleted_at IS NULL AND paid.actor_id=original.actor_id AND paid.amount=original.amount AND paid.currency=original.currency AND paid.payee_wallet_id=original.payee_wallet_id AND original.payee_wallet_id=treasury.id AND purchase.id::text=original.document->'meta'->>'purchase_id' AND purchase.fulfilled_at IS NULL AND purchase.account_id=original.actor_id AND purchase.amount=original.amount AND original.currency='points' AND purchase.treasury_wallet_id=treasury.id AND purchase.treasury_account_id=$4::uuid AND purchase.beneficiary_wallet_id=$6::uuid AND purchase.share_amount=$7::numeric AND distribution.document->'meta'->>'purchase_id'=purchase.id::text AND treasury.account_id=$4::uuid AND treasury.realm_id IS NULL AND treasury.deleted_at IS NULL AND distribution.app_id IS NULL AND distribution.product_identifier='posts.award.distribution' AND distribution.actor_id=treasury.account_id AND distribution.payer_wallet_id=treasury.id AND distribution.payee_wallet_id=$6::uuid AND distribution.status=1 AND distribution.amount=$7::numeric AND distribution.amount=purchase.share_amount AND purchase.attitude=0 AND distribution.currency=original.currency AND distribution.document->'meta'->>'source_order_id'=original.id::text)`, sourceOrder, sourceTransaction, distributionOrder, treasuryActor, treasury, beneficiary, share).Scan(&valid)
	if e != nil {
		return out, e
	}
	if !valid {
		return out, accounts.ErrForbidden
	}
	var currency string
	if e = tx.QueryRowContext(ctx, `SELECT currency FROM commerce_v1.orders WHERE id=$1::uuid`, distributionOrder).Scan(&currency); e != nil {
		return out, e
	}
	return CommerceHold(ctx, tx, distributionOrder, treasuryActor, treasury, beneficiary, currency, share, now)
}
