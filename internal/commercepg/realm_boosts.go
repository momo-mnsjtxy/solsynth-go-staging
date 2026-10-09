// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	b "solsynth.local/go-migration/ports/passport/realmboosts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type RealmBoostFulfillment interface {
	PrepareRealmBoost(context.Context, *sql.DB, *sql.Tx, string, c.Order, string, time.Time) error
	FulfillRealmBoost(context.Context, *sql.DB, *sql.Tx, string, c.Order, time.Time) error
}

func (r *Repository) WithRealmBoosts(f RealmBoostFulfillment) *Repository {
	r.realmBoosts = f
	return r
}
func realmBoostError(e error) error {
	switch {
	case errors.Is(e, b.ErrRetry):
		return c.ErrRetry
	case errors.Is(e, b.ErrConflict):
		return c.ErrConflict
	case errors.Is(e, b.ErrNotFound):
		return c.ErrNotFound
	case errors.Is(e, b.ErrForbidden):
		return c.ErrForbidden
	case errors.Is(e, b.ErrInvalid), errors.Is(e, b.ErrCurrency):
		return c.ErrInvalid
	case errors.Is(e, b.ErrUnavailable):
		return c.ErrUnavailable
	}
	return e
}
func (r *Repository) realmBoostBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil {
		return c.ErrUnavailable
	}
	_, e := r.guard.Authorize(ctx, db, tx, actor, "realms.boosts.manage")
	return e
}
func (r *Repository) RealmBoostRequest(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, key, fp, resource string) (string, error) {
	if e := r.realmBoostBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if len(key) > 128 || len(fp) != 64 {
		return "", c.ErrInvalid
	}
	if resource == "" {
		return idem(ctx, tx, "realm-boost:"+actor, key, "realm-boost", fp)
	}
	if !authority.ValidID(resource) {
		return "", c.ErrInvalid
	}
	return resource, recordIdem(ctx, tx, "realm-boost:"+actor, key, "realm-boost", fp, resource)
}
func (r *Repository) CreateRealmBoostOrder(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, realm, payee, currency string, shares int64, amount wallet.Amount, expiry, at time.Time) (out c.Order, e error) {
	if e = r.realmBoostBoundary(ctx, db, tx, actor); e != nil {
		return
	}
	expected, e := b.Price(shares, currency)
	if e != nil || expected.Cmp(amount) != 0 || !authority.ValidID(purchase) || !authority.ValidID(realm) || !authority.ValidID(payee) || !expiry.After(at) || expiry.Sub(at) > 24*time.Hour {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := b.Product, "Boost realm"
	meta := map[string]json.RawMessage{}
	for k, v := range map[string]any{"account_id": actor, "realm_id": realm, "purchase_id": purchase, "shares": shares, "currency": currency, "amount": amount.String()} {
		raw, x := json.Marshal(v)
		if x != nil {
			return out, x
		}
		meta[k] = raw
	}
	out = c.Order{ID: oid, Status: c.Unpaid, Currency: currency, Amount: amount, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: meta, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: at, UpdatedAt: at, ExpiredAt: expiry}
	return out, insertOrder(ctx, tx, out, "", actor, "", "")
}

// RealmBoostPerkLevel selects the highest explicitly mapped currently eligible
// internal subscription. No upstream identifier/selection policy is inferred.
// Actor authority serializes subscriptions; canonical row/document validation,
// enabled definitions and paid active time exclude queued, test and stale grants.
func (r *Repository) RealmBoostPerkLevel(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, levels map[string]int, now time.Time) (int, error) {
	if e := r.realmBoostBoundary(ctx, db, tx, actor); e != nil {
		return 0, e
	}
	if levels == nil {
		return 0, c.ErrUnavailable
	}
	if len(levels) == 0 {
		return 0, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND status=1 AND begun_at<=$2::timestamptz AND ended_at>$2::timestamptz ORDER BY id LIMIT 1001`, actor, now)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if len(ids) > 1000 {
		return 0, c.ErrUnavailable
	}
	result := 0
	for _, id := range ids {
		row, e := lockedSubscriptionProjection(ctx, tx, id)
		if e != nil {
			return 0, e
		}
		s, reason := row.validate()
		if reason != "" {
			return 0, c.ErrConflict
		}
		level := levels[s.Identifier]
		if level < 1 || level > 3 || s.AccountID != actor || s.AppIdentifier != nil || !s.IsActive || s.IsTesting || s.Status != c.SubscriptionActive || s.BegunAt.After(now) || !s.EndedAt.After(now) {
			continue
		}
		d, e := definition(ctx, tx, s.Identifier)
		if errors.Is(e, c.ErrNotFound) {
			continue
		}
		if e != nil {
			return 0, e
		}
		if d.AppIdentifier != nil || d.ProductID != "" {
			continue
		}
		if level > result {
			result = level
		}
	}
	return result, nil
}
