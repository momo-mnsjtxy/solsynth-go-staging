// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

// ProcessDue is bounded, durable, and restart-safe. It creates no external
// payments. Renewal consumes the explicitly authorized auto_renew row, original
// payer, price and payee; cancellation/account disabling serialize before it.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, c.ErrInvalid
	}
	if r == nil || r.db == nil {
		return 0, c.ErrUnavailable
	}
	count := 0
	for _, kind := range []string{"order", "gift", "subscription"} {
		for count < limit {
			did, e := r.dueOne(ctx, kind)
			if e != nil {
				return count, e
			}
			if !did {
				break
			}
			count++
		}
	}
	return count, nil
}
func (r *Repository) dueOne(ctx context.Context, kind string) (bool, error) {
	now := r.now().UTC()
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if kind == "order" {
		o, e := decode[c.Order](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.orders WHERE status=0 AND expires_at<=$1 ORDER BY expires_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
		if errors.Is(e, c.ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		o.Status = c.Expired
		o.UpdatedAt = now
		if e = saveOrder(ctx, tx, o); e != nil {
			return false, e
		}
		if e = event(ctx, tx, "order.expired", o.ID, o, now); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	if kind == "gift" {
		g, e := decode[c.Gift](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.gifts WHERE status IN(0,1) AND expires_at<=$1 ORDER BY expires_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now))
		if errors.Is(e, c.ErrNotFound) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if e = r.cancelGift(ctx, tx, &g, c.GiftExpired, now); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	// Locate a candidate, then acquire the same account row before the subscription
	// row used by request writers. A stale candidate is rechecked after both locks.
	var sid, actor string
	e = tx.QueryRowContext(ctx, `SELECT id::text,account_id::text FROM commerce_v1.subscriptions WHERE status=1 AND ended_at<=$1 ORDER BY ended_at,id LIMIT 1`, now).Scan(&sid, &actor)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var disabled bool
	e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor).Scan(&disabled)
	if e != nil {
		return false, e
	}
	paymentAllowed := true
	if r.payments != nil {
		policy, ok := r.payments.(RenewalPaymentPolicy)
		if !ok {
			return false, c.ErrUnavailable
		}
		paymentAllowed, e = policy.AllowRenewal(ctx, r.db, tx, actor)
		if e != nil {
			return false, e
		}
	}
	s, e := subscriptionRow(ctx, tx, sid, true)
	if e != nil {
		return false, e
	}
	if s.Status != c.SubscriptionActive || s.EndedAt.After(now) {
		return false, tx.Commit()
	}
	var renew bool
	var days int
	e = tx.QueryRowContext(ctx, `SELECT auto_renew,cycle_days FROM commerce_v1.subscriptions WHERE id=$1`, sid).Scan(&renew, &days)
	if e != nil {
		return false, e
	}
	d, e := definition(ctx, tx, s.Identifier)
	if e != nil && !errors.Is(e, c.ErrNotFound) {
		return false, e
	}
	canRenew := e == nil && !disabled && paymentAllowed && renew && d.AllowInternalWalletRenewal && s.PaymentMethod == "solian.wallet" && s.PaymentDetails.PayerWalletID != nil && s.RenewalAt != nil && s.FinalPrice.Cmp(d.BasePrice) == 0 && s.PaymentDetails.Currency == d.Currency
	if canRenew {
		old, e := order(ctx, tx, s.PaymentDetails.OrderID, false)
		if e != nil {
			return false, e
		}
		canRenew = old.PayeeWalletID == d.PayeeWalletID
	}
	if canRenew {
		if _, e = tx.ExecContext(ctx, `SAVEPOINT renewal_attempt`); e != nil {
			return false, e
		}
		o, e := r.resourceOrder(ctx, tx, actor, d, s.ID, "", s.PaymentDetails.PayerWalletID, s.FinalPrice)
		if e != nil {
			return false, e
		}
		payer := *s.PaymentDetails.PayerWalletID
		tr, e := walletpg.CommerceHold(ctx, tx, o.ID, actor, payer, o.PayeeWalletID, o.Currency, o.Amount, now)
		if errors.Is(e, wallet.ErrInsufficient) {
			if _, e = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT renewal_attempt`); e != nil {
				return false, e
			}
			canRenew = false
		} else if e != nil {
			return false, e
		} else {
			o.Status = c.Paid
			o.TransactionID = &tr.ID
			o.PayerWalletID = &payer
			o.UpdatedAt = now
			// Begin at the saved period boundary, so each renewal is exactly one cycle.
			nextBegin := s.EndedAt
			s.BegunAt = nextBegin
			s.EndedAt = nextBegin.Add(time.Duration(days) * 24 * time.Hour)
			s.RenewalAt = &s.EndedAt
			s.PaymentDetails.OrderID = o.ID
			s.UpdatedAt = now
			if _, e = walletpg.CommerceSettle(ctx, tx, o.ID, false, now); e != nil {
				return false, e
			}
			o.Status = c.Finished
			if e = saveOrder(ctx, tx, o); e != nil {
				return false, e
			}
			if e = saveSubscription(ctx, tx, s); e != nil {
				return false, e
			}
			if e = event(ctx, tx, "order.paid", o.ID, o, now); e != nil {
				return false, e
			}
			if e = event(ctx, tx, "subscription.renewed", s.ID, s, now); e != nil {
				return false, e
			}
			return true, tx.Commit()
		}
	}
	s.Status = c.SubscriptionExpired
	s.IsActive = false
	s.RenewalAt = nil
	s.UpdatedAt = now
	if e = saveSubscription(ctx, tx, s); e != nil {
		return false, e
	}
	if e = event(ctx, tx, "subscription.expired", s.ID, s, now); e != nil {
		return false, e
	}
	return true, tx.Commit()
}

// ApplyProvider receives only the result of an injected verification adapter.
// It grants subscription duration; it never creates wallet credit, fake money,
// checkout success, provider credentials, or a production provider client.
func (r *Repository) ApplyProvider(ctx context.Context, p c.VerifiedPurchase) (c.Subscription, error) {
	var s c.Subscription
	if (p.Provider != "paddle" && p.Provider != "afdian" && p.Provider != "apple") || p.ExternalID == "" || len(p.ExternalID) > 256 || !wallet.ValidID(p.AccountID) || p.DurationDays < 1 || p.DurationDays > 366 {
		return s, c.ErrInvalid
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return s, e
	}
	defer tx.Rollback()
	var disabled bool
	e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, p.AccountID).Scan(&disabled)
	if e != nil {
		return s, notFound(e)
	}
	if disabled {
		return s, c.ErrForbidden
	}
	if e = lock(ctx, tx, "provider:"+p.Provider+":"+p.ExternalID); e != nil {
		return s, e
	}
	fp, e := fingerprint(p)
	if e != nil {
		return s, e
	}
	var prior, priorfp string
	e = tx.QueryRowContext(ctx, `SELECT subscription_id::text,fingerprint FROM commerce_v1.provider_receipts WHERE provider=$1 AND external_id=$2`, p.Provider, p.ExternalID).Scan(&prior, &priorfp)
	if e == nil {
		if fp != priorfp {
			return s, c.ErrIdempotency
		}
		s, e = subscriptionRow(ctx, tx, prior, false)
		if e != nil {
			return s, e
		}
		return s, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return s, e
	}
	d, e := definition(ctx, tx, p.Identifier)
	if e != nil {
		return s, e
	}
	allowed := false
	for _, m := range d.AllowedPaymentMethods {
		if m == providerPaymentMethod(p.Provider) {
			allowed = true
		}
	}
	if !allowed {
		return s, c.ErrForbidden
	}
	sid, e := id()
	if e != nil {
		return s, e
	}
	now := r.now().UTC()
	begin := p.BegunAt.UTC()
	if begin.IsZero() {
		return s, c.ErrInvalid
	}
	if begin.After(now.Add(24*time.Hour)) || begin.Before(now.AddDate(-10, 0, 0)) {
		return s, c.ErrInvalid
	}
	begin, e = groupStart(ctx, tx, p.AccountID, d.GroupIdentifier, d.Identifier, "", begin, p.Testing)
	if e != nil {
		return s, e
	}

	s = c.Subscription{ID: sid, AccountID: p.AccountID, Identifier: d.Identifier, GroupIdentifier: d.GroupIdentifier, DisplayName: d.DisplayName, Status: c.SubscriptionActive, IsActive: true, IsTesting: p.Testing, PaymentMethod: providerPaymentMethod(p.Provider), PaymentDetails: c.PaymentDetails{Currency: d.Currency, OrderID: p.ExternalID}, BasePrice: d.BasePrice, FinalPrice: d.BasePrice, BegunAt: begin, EndedAt: begin.Add(time.Duration(p.DurationDays) * 24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if !s.EndedAt.After(now) {
		s.Status = c.SubscriptionExpired
		s.IsActive = false
	}
	if e = insertSubscription(ctx, tx, s, p.DurationDays, false); e != nil {
		return s, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.provider_receipts(provider,external_id,fingerprint,subscription_id) VALUES($1,$2,$3,$4)`, p.Provider, p.ExternalID, fp, s.ID)
	if e != nil {
		return s, e
	}
	if e = event(ctx, tx, "subscription.provider_applied", s.ID, s, now); e != nil {
		return s, e
	}
	return s, tx.Commit()
}

func providerPaymentMethod(provider string) string {
	if provider == "apple" {
		return "apple_store"
	}
	return provider
}
