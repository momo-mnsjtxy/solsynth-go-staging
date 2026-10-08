// SPDX-License-Identifier: AGPL-3.0-only
// Licensed source contracts and independent policy: recurring-products.md.
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/develop/apps"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
	"time"
)

func productDefinitionID(id string) string { return "app-product:" + id }

func recurringDefinition(p apps.Product, payee string) (c.Definition, error) {
	days := map[string]int{"weekly": 7, "monthly": 30, "yearly": 365}[p.Recurrence]
	if !wallet.ValidID(p.ID) || !wallet.ValidID(p.AppID) || days == 0 || !p.State.IsEnabled || p.State.StockMode != "unlimited" || p.Fulfillment != nil && p.Fulfillment.IsAddressRequired {
		return c.Definition{}, c.ErrUnavailable
	}
	price, e := c.Total([]c.OrderItem{{UnitPrice: p.Price, Quantity: 1}})
	if e != nil || price.Sign() <= 0 {
		return c.Definition{}, c.ErrInvalid
	}
	app := "developer.app:" + p.AppID
	group := productDefinitionID(p.ID)
	if p.GroupIdentifier != nil && *p.GroupIdentifier != "" {
		group = app + ":" + *p.GroupIdentifier
	}
	scopes := []string{}
	if p.Fulfillment != nil {
		scopes = append(scopes, p.Fulfillment.RequiredScopes...)
	}
	sort.Strings(scopes)
	d := c.Definition{AppIdentifier: &app, ProductID: p.ID, ProductIdentifier: p.Identifier, UnitPrice: p.Price, Identifier: productDefinitionID(p.ID), GroupIdentifier: group, DisplayName: p.DisplayName, Currency: p.Currency, BasePrice: price, PayeeWalletID: payee, CycleDurationDays: days, AllowInternalWalletRenewal: true, AllowedPaymentMethods: []string{"solian.wallet"}, ProviderMappings: map[string][]string{}, RequiredScopes: scopes}
	d.ProductTerms, e = recurringTerms(d)
	return d, e
}

func recurringTerms(d c.Definition) (string, error) {
	return fingerprint(struct {
		App                                  *string
		Product, SKU, Group, Currency, Payee string
		Price                                json.Number
		Days                                 int
		Scopes                               []string
	}{d.AppIdentifier, d.ProductID, d.ProductIdentifier, d.GroupIdentifier, d.Currency, d.PayeeWalletID, d.UnitPrice, d.CycleDurationDays, d.RequiredScopes})
}

func productPayee(ctx context.Context, tx *sql.Tx, app string) (string, error) {
	var payee string
	e := tx.QueryRowContext(ctx, `SELECT m.wallet_id::text FROM commerce_v1.merchants m JOIN wallet_v1.wallets w ON w.id=m.wallet_id JOIN owned_custom_apps a ON a.id=m.app_id JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id WHERE m.app_id=$1::uuid AND w.deleted_at IS NULL AND pub.deleted_at IS NULL AND ((pub.account_id IS NOT NULL AND pub.account_id=w.account_id) OR (pub.realm_id IS NOT NULL AND pub.realm_id=w.realm_id)) FOR SHARE OF m`, app).Scan(&payee)
	return payee, notFound(e)
}

// SyncProduct is called inside owned product writes and on the first checkout.
// Missing payout or unsupported fulfillment keeps metadata editable but does
// not advertise a purchasable definition. Existing subscriptions are retained.
func (r *Repository) SyncProduct(ctx context.Context, tx *sql.Tx, p apps.Product, deleted bool) error {
	key := productDefinitionID(p.ID)
	payee, e := productPayee(ctx, tx, p.AppID)
	if e != nil && !errors.Is(e, c.ErrNotFound) {
		return e
	}
	d, valid := recurringDefinition(p, payee)
	if deleted || e != nil || valid != nil {
		_, e = tx.ExecContext(ctx, `UPDATE commerce_v1.definitions SET enabled=false WHERE identifier=$1 AND document->>'product_id'=$2`, key, p.ID)
		return e
	}
	b, e := jsonBytes(d)
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.definitions(identifier,document,payee_wallet_id,enabled) VALUES($1,$2::jsonb,$3::uuid,true) ON CONFLICT(identifier) DO UPDATE SET document=EXCLUDED.document,payee_wallet_id=EXCLUDED.payee_wallet_id,enabled=true WHERE commerce_v1.definitions.document->>'product_id'=EXCLUDED.document->>'product_id' AND commerce_v1.definitions.document->>'app_identifier'=EXCLUDED.document->>'app_identifier'`, d.Identifier, b, payee)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n != 1 {
		return c.ErrConflict
	}
	return e
}

// Call after locking app ancestry and before locking a definition row. Product
// updates use the same product -> definition ordering, avoiding lock inversion.
func lockDefinitionProduct(ctx context.Context, tx *sql.Tx, d c.Definition) (c.Definition, error) {
	if d.ProductID == "" {
		return d, nil
	}
	if !wallet.ValidID(d.ProductID) || d.AppIdentifier == nil {
		return d, c.ErrUnavailable
	}
	app := strings.TrimPrefix(*d.AppIdentifier, "developer.app:")
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT document FROM owned_app_products WHERE id=$1::uuid AND app_id=$2::uuid AND deleted_at IS NULL FOR SHARE`, d.ProductID, app).Scan(&raw)
	if e != nil {
		return d, notFound(e)
	}
	var p apps.Product
	if e = json.Unmarshal(raw, &p); e != nil {
		return d, e
	}
	if p.ID != d.ProductID || p.AppID != app || !p.State.IsEnabled {
		return d, c.ErrNotFound
	}
	payee, e := productPayee(ctx, tx, app)
	if e != nil {
		return d, e
	}
	current, e := recurringDefinition(p, payee)
	if e != nil {
		return d, c.ErrNotFound
	}
	return current, nil
}

// Reuses the consent record shared by owned apps and OAuth. Holding the account,
// app and consent rows makes revoke/payment linearizable. Recheck after waits:
// statement_timestamp is too early when a lock was contested.
func recurringConsent(ctx context.Context, tx *sql.Tx, actor string, d c.Definition, version int64) (int64, error) {
	if d.ProductID == "" {
		return 0, nil
	}
	if d.AppIdentifier == nil {
		return 0, c.ErrUnavailable
	}
	app := strings.TrimPrefix(*d.AppIdentifier, "developer.app:")
	var v int64
	e := tx.QueryRowContext(ctx, `SELECT version FROM owned_app_consents WHERE account_id=$1::uuid AND app_id=$2::uuid FOR SHARE`, actor, app).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, c.ErrForbidden
	}
	if e != nil {
		return 0, e
	}
	raw, e := jsonBytes(d.RequiredScopes)
	if e != nil {
		return 0, e
	}
	var allowed bool
	e = tx.QueryRowContext(ctx, `SELECT c.revoked_at IS NULL AND c.expires_at>clock_timestamp() AND c.scopes @> $3::jsonb AND COALESCE(a.oauth_config->'allowed_scopes','[]'::jsonb) @> $3::jsonb FROM owned_app_consents c JOIN owned_custom_apps a ON a.id=c.app_id WHERE c.account_id=$1::uuid AND c.app_id=$2::uuid`, actor, app, raw).Scan(&allowed)
	if e != nil {
		return 0, e
	}
	if !allowed || version != 0 && version != v {
		return 0, c.ErrForbidden
	}
	return v, nil
}

func recurringSnapshot(o c.Order) (c.Definition, bool, error) {
	b, ok := o.Meta["app_subscription"]
	if !ok {
		return c.Definition{}, false, nil
	}
	var d c.Definition
	if e := json.Unmarshal(b, &d); e != nil {
		return d, true, c.ErrConflict
	}
	if d.ProductID == "" || d.AppIdentifier == nil || *d.AppIdentifier != o.AppIdentifier || len(o.Items) != 1 || o.Items[0].Quantity != 1 || o.Items[0].ProductIdentifier != d.ProductIdentifier || d.Identifier != productDefinitionID(d.ProductID) {
		return d, true, c.ErrConflict
	}
	d.PayeeWalletID = o.PayeeWalletID
	var e error
	d.ProductTerms, e = recurringTerms(d)
	return d, true, e
}

func prepareRecurringOrder(ctx context.Context, tx *sql.Tx, actor string, o c.Order) (c.Definition, bool, error) {
	snapshot, ok, e := recurringSnapshot(o)
	if e != nil || !ok {
		return snapshot, ok, e
	}
	d, e := definition(ctx, tx, snapshot.Identifier)
	if e != nil {
		return d, true, e
	}
	if d.ProductTerms != snapshot.ProductTerms || d.BasePrice.Cmp(o.Amount) != 0 || d.Currency != o.Currency || d.PayeeWalletID != o.PayeeWalletID {
		return d, true, c.ErrConflict
	}
	_, e = recurringConsent(ctx, tx, actor, d, 0)
	return d, true, e
}

func (r *Repository) fulfillRecurringOrder(ctx context.Context, tx *sql.Tx, actor string, o *c.Order, d c.Definition, now time.Time) error {
	version, e := recurringConsent(ctx, tx, actor, d, 0)
	if e != nil {
		return e
	}
	sid, e := id()
	if e != nil {
		return e
	}
	// A merchant-created order cannot authorize standing charges for its buyer.
	s := c.Subscription{ID: sid, AccountID: actor, Identifier: d.Identifier, AppIdentifier: d.AppIdentifier, ProductTerms: d.ProductTerms, ConsentVersion: version, CycleDurationDays: d.CycleDurationDays, GroupIdentifier: d.GroupIdentifier, DisplayName: d.DisplayName, Status: c.SubscriptionUnpaid, PaymentMethod: "solian.wallet", PaymentDetails: c.PaymentDetails{Currency: d.Currency, PayerWalletID: o.PayerWalletID}, BasePrice: d.BasePrice, FinalPrice: d.BasePrice, BegunAt: now, EndedAt: now.Add(time.Duration(d.CycleDurationDays) * 24 * time.Hour), CreatedAt: now, UpdatedAt: now}
	if e = insertSubscription(ctx, tx, s, d.CycleDurationDays, false); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET subscription_id=$2::uuid,product_identifier=$3 WHERE id=$1::uuid AND subscription_id IS NULL`, o.ID, s.ID, d.Identifier); e != nil {
		return e
	}
	o.ProductIdentifier = &d.Identifier
	o.Meta["subscription_id"], _ = jsonBytes(s.ID)
	return r.activateSubscription(ctx, tx, s.ID, *o, now)
}

// The original subscription contract is immutable even if the catalog changes.
func subscriptionEconomics(ctx context.Context, tx *sql.Tx, s c.Subscription, d c.Definition) error {
	var days int
	if e := tx.QueryRowContext(ctx, `SELECT cycle_days FROM commerce_v1.subscriptions WHERE id=$1::uuid`, s.ID).Scan(&days); e != nil {
		return e
	}
	_, price, e := c.CyclePrice(d, &days)
	if e != nil {
		return c.ErrConflict
	}
	if s.BasePrice.Cmp(d.BasePrice) != 0 || s.FinalPrice.Cmp(price) != 0 || s.PaymentDetails.Currency != d.Currency || s.GroupIdentifier != d.GroupIdentifier {
		return c.ErrConflict
	}
	if d.ProductID != "" {
		if s.ConsentVersion < 1 || s.ProductTerms != d.ProductTerms || !sameDefinitionApp(s.AppIdentifier, d.AppIdentifier) {
			return c.ErrConflict
		}
		_, e = recurringConsent(ctx, tx, s.AccountID, d, s.ConsentVersion)
		return e
	}
	return nil
}

// Hide app definitions after catalog disable/deletion or ancestry suspension.
const catalogVisible = `(d.document->>'product_id' IS NULL OR EXISTS(SELECT 1 FROM owned_app_products p JOIN owned_custom_apps a ON a.id=p.app_id JOIN owned_dev_projects proj ON proj.id=a.project_id JOIN owned_developers dev ON dev.id=proj.developer_id JOIN owned_publishers pub ON pub.id=dev.publisher_id WHERE p.id::text=d.document->>'product_id' AND p.deleted_at IS NULL AND p.document->'state'->>'is_enabled'='true' AND a.status=1 AND a.deleted_at IS NULL AND proj.deleted_at IS NULL AND pub.deleted_at IS NULL))`

func subscriptionAccess(ctx context.Context, tx *sql.Tx, s c.Subscription, now time.Time) (c.Subscription, error) {
	s.IsActive = s.Status == c.SubscriptionActive && !s.IsTesting && s.EndedAt.After(now)
	if !s.IsActive || s.AppIdentifier == nil && !strings.HasPrefix(s.Identifier, "app-product:") {
		return s, nil
	}
	d, e := definition(ctx, tx, s.Identifier)
	if e == nil {
		_, e = recurringConsent(ctx, tx, s.AccountID, d, s.ConsentVersion)
	}
	if errors.Is(e, c.ErrNotFound) || errors.Is(e, c.ErrForbidden) || errors.Is(e, c.ErrConflict) {
		s.IsActive = false
		return s, nil
	}
	return s, e
}
