// SPDX-License-Identifier: AGPL-3.0-only
package namecardpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/namecards"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// FulfillNameCard accepts only a stored purchase with a matching settled
// canonical order transaction. The trusted commerce host owns commit/rollback.
func (r *Repository) FulfillNameCard(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, o commerce.Order, now time.Time) error {
	if r == nil || r.db != db || tx == nil || !authority.ValidID(actor) || o.AppIdentifier != "internal" || o.ProductIdentifier == nil || *o.ProductIdentifier != namecards.Product || o.TransactionID == nil {
		return namecards.ErrUnavailable
	}
	var purchaseID string
	if json.Unmarshal(o.Meta["purchase_id"], &purchaseID) != nil || !authority.ValidID(purchaseID) {
		return namecards.ErrUse
	}
	p, e := purchase(ctx, tx, actor, purchaseID, true)
	if e != nil {
		return e
	}
	if p.OrderID != o.ID || p.Amount.Cmp(o.Amount) != 0 {
		return namecards.ErrUse
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_name_card_purchases p JOIN commerce_v1.orders o ON o.id=p.order_id JOIN wallet_v1.transactions t ON t.id=$3::uuid WHERE p.id=$1::uuid AND p.account_id=$2::uuid AND o.actor_id=p.account_id AND o.app_id IS NULL AND o.product_identifier=$4::text AND o.subscription_id IS NULL AND o.gift_id IS NULL AND o.amount=p.amount AND o.currency=p.points_currency AND o.payee_wallet_id=p.payee_wallet_id AND t.managed_by=o.id AND t.purpose='commerce' AND t.actor_id=p.account_id AND t.amount=p.amount AND t.currency=p.points_currency AND t.payee_wallet_id=p.payee_wallet_id AND t.status=2 AND t.confirmed_at IS NOT NULL AND t.deleted_at IS NULL)`, p.ID, actor, *o.TransactionID, namecards.Product).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return namecards.ErrUse
	}
	if p.FulfilledAt != nil {
		var match bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_name_card_receipts WHERE purchase_id=$1::uuid AND order_id=$2::uuid AND transaction_id=$3::uuid)`, p.ID, o.ID, *o.TransactionID).Scan(&match)
		if e != nil {
			return e
		}
		if !match {
			return namecards.ErrUse
		}
		return nil
	}
	now = now.UTC().Truncate(time.Microsecond)
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_name_card_receipts(order_id,purchase_id,transaction_id,fulfilled_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::timestamptz)`, o.ID, p.ID, *o.TransactionID, now); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_name_card_purchases SET fulfilled_at=$2::timestamptz,updated_at=$2::timestamptz WHERE id=$1::uuid`, p.ID, now); e != nil {
		return e
	}
	return audit(ctx, tx, actor, p.ID, "card.fulfilled", now)
}
func (r *Repository) Use(ctx context.Context, actor string, q namecards.UseRequest, key string) (out namecards.Purchase, e error) {
	if e = namecards.ValidateUse(&q); e != nil {
		return
	}
	if !namecards.ValidKey(key) {
		return out, namecards.ErrInvalid
	}
	if r.commerce == nil {
		return out, namecards.ErrUnavailable
	}
	if q.Target == "account" && q.TargetID != nil && *q.TargetID != actor {
		return out, namecards.ErrInvalid
	}
	e = r.within(ctx, actor, func(tx *sql.Tx, now time.Time) error {
		fp := fingerprint(q)
		prior, e := r.commerce.NameCardRequest(ctx, r.db, tx, actor, key, "use", fp, "")
		if e != nil {
			return e
		}
		if prior != "" {
			out, e = purchase(ctx, tx, actor, prior, false)
			return e
		}
		p, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_name_card_purchases WHERE account_id=$1::uuid AND fulfilled_at IS NOT NULL AND consumed_at IS NULL ORDER BY created_at,id LIMIT 1 FOR UPDATE`, actor))
		if errors.Is(e, namecards.ErrNotFound) {
			return namecards.ErrUse
		}
		if e != nil {
			return e
		}
		var valid bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_name_card_receipts r JOIN commerce_v1.orders o ON o.id=r.order_id JOIN wallet_v1.transactions t ON t.id=r.transaction_id WHERE r.purchase_id=$1::uuid AND o.id=$2::uuid AND o.actor_id=$3::uuid AND o.status=3 AND t.status=2 AND t.managed_by=o.id AND t.deleted_at IS NULL)`, p.ID, p.OrderID, actor).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return namecards.ErrUse
		}
		// Record the request while its original proof is still valid. A rename
		// error rolls this back; account rename deliberately advances the version.
		if _, e = r.commerce.NameCardRequest(ctx, r.db, tx, actor, key, "use", fp, p.ID); e != nil {
			return e
		}
		var old string
		switch q.Target {
		case "account":
			if r.accounts == nil {
				return namecards.ErrUnavailable
			}
			old, e = r.accounts.RenameAccountForCard(ctx, r.db, tx, actor, q.NewName, now)
		case "publisher":
			if r.accounts == nil {
				return namecards.ErrUnavailable
			}
			old, e = r.accounts.RenamePublisherForCard(ctx, r.db, tx, actor, *q.TargetID, q.NewName, now)
		case "realm":
			if r.realms == nil {
				return namecards.ErrUnavailable
			}
			old, e = r.realms.RenameRealmForCard(ctx, r.db, tx, actor, *q.TargetID, q.NewName, now)
		}
		if e != nil {
			return e
		}
		if old == q.NewName {
			return namecards.ErrUse
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_name_card_purchases SET consumed_at=$2::timestamptz,target_type=$3::text,target_id=$4::uuid,old_name=$5::text,new_name=$6::text,updated_at=$2::timestamptz WHERE id=$1::uuid`, p.ID, now, q.Target, q.TargetID, old, q.NewName); e != nil {
			return e
		}
		if e = audit(ctx, tx, actor, p.ID, "card.consumed", now); e != nil {
			return e
		}
		out, e = purchase(ctx, tx, actor, p.ID, false)
		return e
	})
	return
}
