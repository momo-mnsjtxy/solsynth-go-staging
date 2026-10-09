// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

type SponsorFulfillment interface {
	PrepareSponsor(context.Context, *sql.DB, *sql.Tx, string, c.Order, string, time.Time) error
	FulfillSponsor(context.Context, *sql.DB, *sql.Tx, string, c.Order, time.Time) error
}

func (r *Repository) WithSponsorship(f SponsorFulfillment) *Repository { r.sponsorship = f; return r }
func sponsorError(e error) error {
	switch {
	case errors.Is(e, s.ErrRetry):
		return c.ErrRetry
	case errors.Is(e, s.ErrConflict):
		return c.ErrConflict
	case errors.Is(e, s.ErrNotFound):
		return c.ErrNotFound
	case errors.Is(e, s.ErrForbidden):
		return c.ErrForbidden
	case errors.Is(e, s.ErrInvalid):
		return c.ErrInvalid
	case errors.Is(e, s.ErrUnavailable):
		return c.ErrUnavailable
	}
	return e
}
func (r *Repository) sponsorBoundary(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string) error {
	if r == nil || r.db != db || db == nil || tx == nil || r.guard == nil {
		return c.ErrUnavailable
	}
	_, e := r.guard.Authorize(ctx, db, tx, actor, "posts.sponsor")
	return e
}
func (r *Repository) SponsorRequest(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, key, fp, resource string) (string, error) {
	if e := r.sponsorBoundary(ctx, db, tx, actor); e != nil {
		return "", e
	}
	if len(key) > 128 || len(fp) != 64 {
		return "", c.ErrInvalid
	}
	if resource == "" {
		return idem(ctx, tx, "sponsor:"+actor, key, "sponsor", fp)
	}
	if !authority.ValidID(resource) {
		return "", c.ErrInvalid
	}
	return resource, recordIdem(ctx, tx, "sponsor:"+actor, key, "sponsor", fp, resource)
}
func (r *Repository) CreateSponsorOrder(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, purchase, post, payee string, amount wallet.Amount, expiry, at time.Time) (out c.Order, e error) {
	if e = r.sponsorBoundary(ctx, db, tx, actor); e != nil {
		return
	}
	if !authority.ValidID(purchase) || !authority.ValidID(post) || !authority.ValidID(payee) || amount.Cmp(s.Minimum()) < 0 || !expiry.After(at) || expiry.Sub(at) > 24*time.Hour {
		return out, c.ErrInvalid
	}
	oid, e := id()
	if e != nil {
		return out, e
	}
	product, remark := s.Product, "Sponsor public post"
	meta := map[string]json.RawMessage{}
	for k, v := range map[string]any{"account_id": actor, "post_id": post, "purchase_id": purchase, "amount": amount.String()} {
		b, x := json.Marshal(v)
		if x != nil {
			return out, x
		}
		meta[k] = b
	}
	out = c.Order{ID: oid, Status: c.Unpaid, Currency: "golds", Amount: amount, AppIdentifier: "internal", ProductIdentifier: &product, Remarks: &remark, Meta: meta, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: at, UpdatedAt: at, ExpiredAt: expiry}
	return out, insertOrder(ctx, tx, out, "", actor, "", "")
}
