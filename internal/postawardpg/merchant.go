// SPDX-License-Identifier: AGPL-3.0-only
package postawardpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/postawards"
)

func manager(ctx context.Context, tx *sql.Tx, actor, pub string) error {
	var yes bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_members WHERE publisher_id=$1::uuid AND account_id=$2::uuid AND role>=100 AND joined_at IS NOT NULL AND revoked_at IS NULL)`, pub, actor).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return a.ErrForbidden
	}
	return nil
}
func (r *Repository) walletMatches(ctx context.Context, tx *sql.Tx, wallet string, s scope) error {
	var yes bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL AND ((account_id=NULLIF($2::text,'')::uuid AND realm_id IS NULL)OR(realm_id=NULLIF($3::text,'')::uuid AND account_id IS NULL)))`, wallet, s.Owner, s.Realm).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes || wallet == r.cfg.TreasuryWalletID {
		return a.ErrWallet
	}
	return nil
}
func (r *Repository) binding(ctx context.Context, tx *sql.Tx, s scope) (string, error) {
	var wallet *string
	e := tx.QueryRowContext(ctx, `SELECT wallet_id::text FROM owned_publisher_payout_wallets WHERE publisher_id=$1::uuid FOR SHARE NOWAIT`, s.Publisher).Scan(&wallet)
	if errors.Is(e, sql.ErrNoRows) || e == nil && wallet == nil {
		return "", a.ErrWallet
	}
	if e != nil {
		return "", e
	}
	return *wallet, r.walletMatches(ctx, tx, *wallet, s)
}
func (r *Repository) merchantTx(ctx context.Context, actor, selector, permission string) (*sql.Tx, scope, error) {
	var s scope
	if r == nil {
		return nil, s, a.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, s, e
	}
	perms := []string{}
	if permission != "" {
		perms = append(perms, permission)
	}
	s, e = r.lockScope(ctx, tx, actor, selector, "", perms...)
	if e == nil {
		e = manager(ctx, tx, actor, s.Publisher)
	}
	if e != nil {
		tx.Rollback()
		return nil, s, mapped(e)
	}
	return tx, s, nil
}
func merchant(ctx context.Context, tx *sql.Tx, pub string) (out a.Merchant, e error) {
	out.PublisherID = pub
	e = tx.QueryRowContext(ctx, `SELECT wallet_id::text,updated_at FROM owned_publisher_payout_wallets WHERE publisher_id=$1::uuid`, pub).Scan(&out.PaymentWalletID, &out.UpdatedAt)
	return out, mapped(e)
}
func (r *Repository) Merchant(ctx context.Context, actor, selector string) (out a.Merchant, e error) {
	tx, s, e := r.merchantTx(ctx, actor, selector, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	out, e = merchant(ctx, tx, s.Publisher)
	if e != nil {
		return out, e
	}
	return out, r.commit(ctx, tx, actor)
}
func (r *Repository) BindWallet(ctx context.Context, actor, selector string, q a.WalletPatch) (out a.Merchant, e error) {
	defer func() { e = mapped(e) }()
	tx, s, e := r.merchantTx(ctx, actor, selector, "merchants.manage")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if q.WalletID != nil {
		if !authority.ValidID(*q.WalletID) {
			return out, a.ErrInvalid
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, *q.WalletID).Scan(&held); e != nil {
			return out, e
		}
		if e = r.walletMatches(ctx, tx, *q.WalletID, s); e != nil {
			return out, e
		}
		if s.Owner != "" && s.Owner != actor {
			return out, a.ErrForbidden
		}
		if s.Realm != "" {
			var owner bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id AND r.account_id=m.account_id WHERE m.realm_id=$1::uuid AND m.account_id=$2::uuid AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL)`, s.Realm, actor).Scan(&owner); e != nil {
				return out, e
			}
			if !owner {
				return out, a.ErrForbidden
			}
		}
	}
	at := r.cfg.Now()
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_payout_wallets(publisher_id,wallet_id,updated_at)VALUES($1::uuid,$2::uuid,$3::timestamptz)ON CONFLICT(publisher_id)DO UPDATE SET wallet_id=EXCLUDED.wallet_id,updated_at=EXCLUDED.updated_at`, s.Publisher, q.WalletID, at)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, actor, s.Publisher, "", "", "wallet", at); e != nil {
		return out, e
	}
	return a.Merchant{PublisherID: s.Publisher, PaymentWalletID: q.WalletID, UpdatedAt: at}, r.commit(ctx, tx, actor, "merchants.manage")
}
