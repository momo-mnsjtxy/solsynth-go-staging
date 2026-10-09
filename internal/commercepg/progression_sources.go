// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"time"
)

const stellarProgramGroup = "solian.stellar"

// recordStellarSupportMonths derives every field from the funded canonical
// order/subscription/wallet rows. External-provider and testing subscriptions
// cannot produce this internal-wallet progression source.
func recordStellarSupportMonths(ctx context.Context, tx *sql.Tx, orderID string, at time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.stellar_support_months(id,order_id,subscription_id,account_id,transaction_id,payer_wallet_id,payee_wallet_id,currency,amount,confirmed_at,cycle_days,month_index,credited_months,occurred_at)
	 SELECT gen_random_uuid(),o.id,s.id,s.account_id,t.id,t.payer_wallet_id,t.payee_wallet_id,t.currency,t.amount,t.confirmed_at,s.cycle_days,month_index,credited,t.confirmed_at
	 FROM commerce_v1.orders o
	 JOIN commerce_v1.subscriptions s ON s.id=o.subscription_id
	 JOIN commerce_v1.definitions d ON d.identifier=s.identifier
	 JOIN wallet_v1.transactions t ON t.id=o.transaction_id
	 JOIN wallet_v1.wallets payer ON payer.id=t.payer_wallet_id
	 JOIN wallet_v1.wallets payee ON payee.id=t.payee_wallet_id
	 CROSS JOIN LATERAL (SELECT GREATEST(1,floor((s.cycle_days+2)::numeric/30)::integer) credited) duration
	 CROSS JOIN LATERAL generate_series(1,duration.credited) month_index
	 WHERE o.id=$1 AND o.status=3 AND o.app_id IS NULL AND o.gift_id IS NULL AND o.actor_id=s.account_id AND o.product_identifier=s.identifier AND o.document->>'app_identifier'='internal'
	  AND s.document->>'app_identifier' IS NULL AND lower(s.document->>'group_identifier')=$3
	  AND s.document->>'payment_method'='solian.wallet'
	  AND COALESCE((s.document->>'is_testing')::boolean,false)=false
	  AND COALESCE((s.document->>'is_free_trial')::boolean,false)=false
	  AND o.payer_wallet_id=s.payer_wallet_id AND o.payee_wallet_id=d.payee_wallet_id
	  AND t.managed_by=o.id AND t.purpose='commerce' AND t.status=2 AND t.deleted_at IS NULL AND t.confirmed_at=$2
	  AND t.actor_id=o.actor_id AND t.payer_wallet_id=o.payer_wallet_id AND t.payee_wallet_id=o.payee_wallet_id AND t.currency=o.currency AND t.amount=o.amount
	  AND payer.account_id=o.actor_id AND payer.realm_id IS NULL AND payer.deleted_at IS NULL AND payee.deleted_at IS NULL
	 ON CONFLICT(order_id,month_index) DO NOTHING`, orderID, at, stellarProgramGroup)
	return e
}
