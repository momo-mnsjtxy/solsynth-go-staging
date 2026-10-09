// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// finalizePaidOrder keeps fulfillment and money atomic while giving every
// developer-owned order the same pending merchant-settlement lifecycle.
func finalizePaidOrder(ctx context.Context, tx *sql.Tx, o c.Order, now time.Time) error {
	app := strings.TrimPrefix(o.AppIdentifier, "developer.app:")
	if o.AppIdentifier != "developer.app:"+app {
		_, err := walletpg.CommerceSettle(ctx, tx, o.ID, false, now)
		return err
	}
	if !wallet.ValidID(app) || o.TransactionID == nil {
		return c.ErrConflict
	}
	settlementID, err := id()
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO commerce_v1.merchant_settlements(id,publisher_id,app_id,order_id,wallet_id,currency,amount,status,created_at)
		SELECT $1,d.publisher_id,a.id,o.id,o.payee_wallet_id,o.currency,o.amount,0,$4
		FROM commerce_v1.orders o
		JOIN owned_custom_apps a ON a.id=o.app_id
		JOIN owned_dev_projects p ON p.id=a.project_id
		JOIN owned_developers d ON d.id=p.developer_id
		JOIN wallet_v1.transactions t ON t.id=$3::uuid AND t.managed_by=o.id AND t.purpose='commerce' AND t.status=1 AND t.deleted_at IS NULL
		WHERE o.id=$2::uuid AND a.id=$5::uuid AND o.payee_wallet_id=t.payee_wallet_id AND o.currency=t.currency AND o.amount=t.amount
		ON CONFLICT(order_id)DO NOTHING`, settlementID, o.ID, *o.TransactionID, now, app)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 1 {
		return nil
	}
	var existingApp, walletID, currency, amount, transactionID string
	var status int
	err = tx.QueryRowContext(ctx, `SELECT s.app_id::text,s.wallet_id::text,s.currency,s.amount::text,s.status,t.id::text
		FROM commerce_v1.merchant_settlements s
		JOIN wallet_v1.transactions t ON t.managed_by=s.order_id AND t.purpose='commerce' AND t.deleted_at IS NULL
		WHERE s.order_id=$1::uuid FOR SHARE OF s,t`, o.ID).Scan(&existingApp, &walletID, &currency, &amount, &status, &transactionID)
	if err != nil {
		return err
	}
	if existingApp != app || walletID != o.PayeeWalletID || currency != o.Currency || amount != o.Amount.String() || status != 0 || transactionID != *o.TransactionID {
		return c.ErrConflict
	}
	return nil
}
