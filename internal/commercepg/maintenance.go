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
// The result counts committed transitions or quarantine decisions. The limit
// also bounds stale-candidate retries; corrupt subscriptions never move money.
func (r *Repository) ProcessDue(ctx context.Context, limit int) (int, error) {
	return r.processDue(ctx, "", limit)
}

func (r *Repository) OperatorProcessDue(ctx context.Context, actor string, limit int) (int, error) {
	if !wallet.ValidID(actor) {
		return 0, c.ErrForbidden
	}
	return r.processDue(ctx, actor, limit)
}

func (r *Repository) processDue(ctx context.Context, actor string, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, c.ErrInvalid
	}
	if r == nil || r.db == nil {
		return 0, c.ErrUnavailable
	}
	count, budget := 0, limit
	for _, kind := range []string{"order", "gift", "subscription"} {
		for budget > 0 {
			did, e := r.dueOne(ctx, actor, kind)
			if errors.Is(e, errMaintenanceAdvanced) {
				budget--
				continue
			}
			if e != nil {
				return count, e
			}
			if !did {
				break
			}
			budget--
			count++
		}
	}
	return count, nil
}
func (r *Repository) maintenanceCommit(ctx context.Context, tx *sql.Tx, actor string) error {
	if actor != "" {
		if _, e := r.guard.Authorize(ctx, r.db, tx, actor, "subscriptions.order.manage"); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (r *Repository) dueOne(ctx context.Context, actor, kind string) (bool, error) {
	now := r.now().UTC()
	permission := ""
	if actor != "" {
		permission = "subscriptions.order.manage"
	}
	tx, e := r.begin(ctx, actor, permission)
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
		return true, r.maintenanceCommit(ctx, tx, actor)
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
		return true, r.maintenanceCommit(ctx, tx, actor)
	}
	// Locate a candidate, then acquire the same account row before the subscription
	// row used by request writers. A stale candidate is rechecked after both locks.
	var sid, accountID string
	e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.account_id::text FROM commerce_v1.subscriptions s WHERE s.status=1 AND s.ended_at<=$1::timestamptz AND NOT EXISTS(SELECT 1 FROM commerce_v1.subscription_maintenance_quarantine q WHERE q.subscription_id=s.id AND q.resolved_at IS NULL) ORDER BY s.ended_at,s.id LIMIT 1`, now).Scan(&sid, &accountID)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	accountEligible, e := renewalAccountEligible(ctx, tx, accountID, true)
	if e != nil {
		return false, e
	}
	paymentAllowed := true
	if r.payments != nil {
		policy, ok := r.payments.(RenewalPaymentPolicy)
		if !ok {
			return false, c.ErrUnavailable
		}
		paymentAllowed, e = policy.AllowRenewal(ctx, r.db, tx, accountID)
		if e != nil {
			return false, e
		}
	}
	row, e := lockedSubscriptionProjection(ctx, tx, sid)
	if e != nil {
		return false, e
	}
	if row.AccountID != accountID {
		if e = r.maintenanceCommit(ctx, tx, actor); e != nil {
			return false, e
		}
		return false, errMaintenanceAdvanced
	}
	var quarantined bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.subscription_maintenance_quarantine WHERE subscription_id=$1::uuid AND resolved_at IS NULL)`, sid).Scan(&quarantined); e != nil {
		return false, e
	}
	if quarantined {
		if e = r.maintenanceCommit(ctx, tx, actor); e != nil {
			return false, e
		}
		return false, errMaintenanceAdvanced
	}
	s, reason := row.validate()
	if reason != "" {
		if e = quarantineSubscription(ctx, tx, row, reason, r.now().UTC()); e != nil {
			return false, e
		}
		return true, r.maintenanceCommit(ctx, tx, actor)
	}
	if s.Status != c.SubscriptionActive || s.EndedAt.After(now) {
		if e = r.maintenanceCommit(ctx, tx, actor); e != nil {
			return false, e
		}
		return false, errMaintenanceAdvanced
	}
	renew, days := row.Renew, row.Days
	d, e := definitionForOrder(ctx, tx, s.Identifier)
	if e != nil && !errors.Is(e, c.ErrNotFound) && !errors.Is(e, c.ErrConflict) {
		return false, e
	}
	canRenew := e == nil && accountEligible && paymentAllowed && renew && d.AllowInternalWalletRenewal && s.PaymentMethod == "solian.wallet" && s.PaymentDetails.PayerWalletID != nil && s.RenewalAt != nil && s.PaymentDetails.Currency == d.Currency
	renewalPrice := s.FinalPrice
	if s.IsFreeTrial {
		if s.BasePrice.Cmp(d.BasePrice) != 0 || s.PaymentDetails.Currency != d.Currency || s.GroupIdentifier != d.GroupIdentifier {
			canRenew = false
		} else {
			_, renewalPrice, e = c.CyclePrice(d, &days)
			if e != nil {
				canRenew = false
			}
		}
	}
	if canRenew && !s.IsFreeTrial {
		x := subscriptionEconomics(ctx, tx, s, d)
		if x != nil && !errors.Is(x, c.ErrConflict) && !errors.Is(x, c.ErrForbidden) && !errors.Is(x, c.ErrNotFound) {
			return false, x
		}
		canRenew = x == nil
	}
	if canRenew && !s.IsFreeTrial {
		old, e := order(ctx, tx, s.PaymentDetails.OrderID, false)
		if e != nil {
			return false, e
		}
		canRenew = s.Coupon == nil && old.Coupon == nil && old.PayeeWalletID == d.PayeeWalletID && old.Amount.Cmp(s.FinalPrice) == 0 && old.Currency == s.PaymentDetails.Currency && old.Status == c.Finished && old.TransactionID != nil && old.PayerWalletID != nil && *old.PayerWalletID == *s.PaymentDetails.PayerWalletID
	}
	if canRenew {
		if _, e = tx.ExecContext(ctx, `SAVEPOINT renewal_attempt`); e != nil {
			return false, e
		}
		o, e := r.resourceOrder(ctx, tx, accountID, d, s.ID, "", s.PaymentDetails.PayerWalletID, renewalPrice, nil)
		if e != nil && !errors.Is(e, c.ErrStock) {
			return false, e
		}
		payer := *s.PaymentDetails.PayerWalletID
		var tr wallet.Transaction
		if e == nil {
			tr, e = walletpg.CommerceHold(ctx, tx, o.ID, accountID, payer, o.PayeeWalletID, o.Currency, o.Amount, now)
		}
		if errors.Is(e, wallet.ErrInsufficient) || errors.Is(e, c.ErrStock) {
			if _, e = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT renewal_attempt`); e != nil {
				return false, e
			}
			canRenew = false
		} else if e != nil {
			return false, e
		} else {
			// The hold can wait on wallet rows until a scheduled account punishment
			// becomes active. Re-read against PostgreSQL's wall clock while retaining
			// the account lock, and undo the entire attempt before expiring it.
			accountEligible, e = renewalAccountEligible(ctx, tx, accountID, false)
			if e != nil {
				return false, e
			}
			if !accountEligible {
				if _, e = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT renewal_attempt`); e != nil {
					return false, e
				}
				canRenew = false
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
				s.FinalPrice = renewalPrice
				s.IsFreeTrial = false
				s.UpdatedAt = now
				if e = finalizePaidOrder(ctx, tx, o, now); e != nil {
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
				if e = recordStellarSupportMonths(ctx, tx, o.ID, now); e != nil {
					return false, e
				}
				if _, e = recurringConsent(ctx, tx, accountID, d, s.ConsentVersion); e != nil {
					return false, e
				}
				return true, r.maintenanceCommit(ctx, tx, actor)
			}
		}
	}
	s.Status = c.SubscriptionExpired
	s.IsAutoRenewal = false
	s.IsActive = false
	s.RenewalAt = nil
	s.UpdatedAt = now
	if e = saveSubscription(ctx, tx, s); e != nil {
		return false, e
	}
	if e = event(ctx, tx, "subscription.expired", s.ID, s, now); e != nil {
		return false, e
	}
	return true, r.maintenanceCommit(ctx, tx, actor)
}

func renewalAccountEligible(ctx context.Context, tx *sql.Tx, accountID string, lock bool) (bool, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`+suffix, accountID).Scan(&eligible)
	return eligible, err
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
	receiptID, x := id()
	if x != nil {
		return s, x
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.provider_receipts(id,provider,external_id,fingerprint,subscription_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, receiptID, p.Provider, p.ExternalID, fp, s.ID, now)
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
