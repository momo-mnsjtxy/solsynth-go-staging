// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"reflect"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
	"time"
)

//go:embed coupon_schema.sql
var CouponSchemaSQL string

func (r *Repository) WithCouponPolicy(p c.CouponPolicy) *Repository { r.couponPolicy = p; return r }

var _ c.CouponStore = (*Repository)(nil)

func couponRow(ctx context.Context, tx *sql.Tx, app, code string) (c.Coupon, error) {
	var out c.Coupon
	var b []byte
	var enabled bool
	var rowID, publisher, payee, identifier string
	e := tx.QueryRowContext(ctx, `SELECT id::text,publisher_id::text,payee_wallet_id::text,identifier,document,enabled FROM commerce_v1.coupons WHERE app_id=$1::uuid AND (id::text=$2 OR identifier=$2) FOR UPDATE`, app, code).Scan(&rowID, &publisher, &payee, &identifier, &b, &enabled)
	if e != nil {
		return out, notFound(e)
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return out, e
	}
	out.Enabled = enabled
	if out.AppID != app || out.ID != rowID || out.PublisherID != publisher || out.PayeeWalletID != payee || out.Identifier != identifier || out.Terms.Validate() != nil {
		return out, c.ErrConflict
	}
	return out, nil
}

// Keep canonical publisher/project/app and merchant binding stable through the
// transaction. Owner enablement uses fresh observations, never a new owner lock
// after the buyer account lock (which would invert existing account/app writers).
func couponIssuer(ctx context.Context, tx *sql.Tx, app, publisher, payee string) error {
	if !wallet.ValidID(app) || !wallet.ValidID(publisher) || !wallet.ValidID(payee) {
		return c.ErrForbidden
	}
	ref := "developer.app:" + app
	if e := lockDefinitionApp(ctx, tx, &ref); e != nil {
		return e
	}
	// Acquire the merchant lock before the final owner/punishment observation:
	// even a quote replay may have waited for a merchant configuration writer.
	current, e := productPayee(ctx, tx, app)
	if e != nil {
		return e
	}
	if current != payee {
		return c.ErrForbidden
	}
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_custom_apps a JOIN owned_dev_projects p ON p.id=a.project_id JOIN owned_developers d ON d.id=p.developer_id JOIN owned_publishers pub ON pub.id=d.publisher_id LEFT JOIN owned_realms realm ON realm.id=pub.realm_id JOIN owned_authority_accounts owner ON owner.id=COALESCE(pub.account_id,realm.account_id) WHERE a.id=$1::uuid AND d.publisher_id=$2::uuid AND a.status=1 AND a.deleted_at IS NULL AND p.deleted_at IS NULL AND pub.deleted_at IS NULL AND COALESCE(a.oauth_config->'allowed_scopes','[]'::jsonb) @> '["coupons.manage"]'::jsonb AND (pub.realm_id IS NULL OR realm.deleted_at IS NULL) AND NOT owner.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=owner.id AND punishment.kind='disable_account' AND (punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND (punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())))`, app, publisher).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return c.ErrForbidden
	}
	return nil
}
func (r *Repository) CreateCoupon(ctx context.Context, q c.CreateCoupon, key string) (out c.Coupon, e error) {
	if !r.couponPolicy.Valid() {
		return out, c.ErrUnavailable
	}
	if !c.ValidCouponCode(q.Identifier) || q.Terms.Validate() != nil {
		return out, c.ErrInvalid
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, q.Credentials, "coupons.manage")
	if e != nil {
		return out, e
	}
	payee, e := productPayee(ctx, tx, app.AppID)
	if e != nil {
		return out, e
	}
	if e = couponIssuer(ctx, tx, app.AppID, app.PublisherID, payee); e != nil {
		return out, e
	}
	credentials := q.Credentials
	q.ClientSecret = ""
	fp, e := fingerprint(q)
	if e != nil {
		return out, e
	}
	prior, e := idem(ctx, tx, "app:"+app.AppID, key, "coupon", fp)
	if e != nil {
		return out, e
	}
	if prior != "" {
		out, e = couponRow(ctx, tx, app.AppID, prior)
		if e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	products := append([]string(nil), q.Terms.Products...)
	sort.Strings(products)
	for _, name := range products {
		if q.Terms.Scope == "order" {
			if r.adapters.Product == nil {
				return out, c.ErrUnavailable
			}
			p, err := r.adapters.Product(ctx, tx, app.AppID, name)
			if err != nil {
				return out, err
			}
			if !p.Enabled || p.Currency != q.Terms.Currency {
				return out, c.ErrForbidden
			}
		} else {
			d, err := definition(ctx, tx, name)
			if err != nil {
				return out, err
			}
			if d.AppIdentifier == nil || *d.AppIdentifier != "developer.app:"+app.AppID || d.PayeeWalletID != payee || d.Currency != q.Terms.Currency {
				return out, c.ErrForbidden
			}
			if q.Terms.Scope == "gift" && (!d.AllowGift || d.ProductID != "") {
				return out, c.ErrUnavailable
			}
		}
	}
	if e = lock(ctx, tx, "coupon-code:"+app.AppID+":"+q.Identifier); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.coupons WHERE app_id=$1::uuid AND identifier=$2)`, app.AppID, q.Identifier).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		return out, c.ErrConflict
	}
	now := r.now().UTC()
	if !q.Terms.ExpiresAt.After(now) {
		return out, c.ErrInvalid
	}
	cid, e := id()
	if e != nil {
		return out, e
	}
	out = c.Coupon{ID: cid, Identifier: q.Identifier, AppID: app.AppID, PublisherID: app.PublisherID, PayeeWalletID: payee, Terms: q.Terms, Policy: r.couponPolicy, CreatedAt: now, Enabled: true}
	b, e := jsonBytes(out)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.coupons(id,app_id,publisher_id,payee_wallet_id,identifier,enabled,document) VALUES($1,$2,$3,$4,$5,true,$6)`, cid, app.AppID, app.PublisherID, payee, q.Identifier, b)
	if e != nil {
		return out, e
	}
	if _, e = r.client(ctx, tx, credentials, "coupons.manage"); e != nil {
		return out, e
	}
	if e = recordIdem(ctx, tx, "app:"+app.AppID, key, "coupon", fp, cid); e != nil {
		return out, e
	}
	// Coupon audience/code are private issuer data, so the event carries only ID.
	if e = event(ctx, tx, "coupon.created", cid, map[string]string{"id": cid}, now); e != nil {
		return out, e
	}
	if e = couponIssuer(ctx, tx, app.AppID, app.PublisherID, payee); e != nil {
		return out, e
	}
	if !out.Terms.ExpiresAt.After(r.now().UTC()) {
		return out, c.ErrConflict
	}
	return out, tx.Commit()
}
func (r *Repository) RevokeCoupon(ctx context.Context, cid string, q c.RevokeCoupon) (out c.Coupon, e error) {
	if !r.couponPolicy.Valid() {
		return out, c.ErrUnavailable
	}
	if !wallet.ValidID(cid) {
		return out, c.ErrNotFound
	}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, q.Credentials, "coupons.manage")
	if e != nil {
		return out, e
	}
	out, e = couponRow(ctx, tx, app.AppID, cid)
	if e != nil {
		return out, e
	}
	if out.PublisherID != app.PublisherID {
		return out, c.ErrForbidden
	}
	if !out.Enabled {
		return out, tx.Commit()
	}
	out.Enabled = false
	if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.coupons SET enabled=false WHERE id=$1`, cid); e != nil {
		return out, e
	}
	if _, e = r.client(ctx, tx, q.Credentials, "coupons.manage"); e != nil {
		return out, e
	}
	if e = event(ctx, tx, "coupon.revoked", cid, map[string]string{"id": cid}, r.now().UTC()); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func couponApp(d c.Definition) (string, error) {
	if d.AppIdentifier == nil {
		return "", c.ErrUnavailable
	}
	app := strings.TrimPrefix(*d.AppIdentifier, "developer.app:")
	if *d.AppIdentifier != "developer.app:"+app || !wallet.ValidID(app) {
		return "", c.ErrUnavailable
	}
	return app, nil
}
func (r *Repository) quoteCoupon(ctx context.Context, tx *sql.Tx, actor, app, payee, scope, currency, code string, products []string, gross wallet.Amount) (*c.CouponQuote, error) {
	if !r.couponPolicy.Valid() {
		return nil, c.ErrUnavailable
	}
	if code == "" || len(code) > 64 {
		return nil, c.ErrInvalid
	}
	checkoutTerms, e := couponCheckoutTerms(ctx, tx, app, payee, scope, products)
	if e != nil {
		return nil, e
	}
	coupon, e := couponRow(ctx, tx, app, code)
	if e != nil {
		return nil, e
	}
	if coupon.Policy != r.couponPolicy {
		return nil, c.ErrUnavailable
	}
	if e = couponIssuer(ctx, tx, app, coupon.PublisherID, payee); e != nil {
		return nil, e
	}
	quote, e := c.QuoteCoupon(coupon, actor, app, payee, scope, currency, products, gross, r.now().UTC())
	if e != nil {
		return nil, e
	}
	quote.CheckoutTermsHash = checkoutTerms
	if e = couponCapacity(ctx, tx, coupon, actor); e != nil {
		return nil, e
	}
	return quote, nil
}
func couponCapacity(ctx context.Context, tx *sql.Tx, coupon c.Coupon, actor string) error {
	var total, mine int
	if e := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE account_id=$2::uuid) FROM commerce_v1.coupon_redemptions WHERE coupon_id=$1::uuid`, coupon.ID, actor).Scan(&total, &mine); e != nil {
		return e
	}
	if total >= coupon.Terms.MaxUses || mine >= coupon.Terms.MaxUsesPerAccount {
		return c.ErrConflict
	}
	return nil
}
func orderProducts(o c.Order) []string {
	out := []string{}
	for _, i := range o.Items {
		out = append(out, i.ProductIdentifier)
	}
	return out
}
func (r *Repository) ApplyOrderCoupon(ctx context.Context, actor, oid, code string, preview bool) (o c.Order, e error) {
	if !r.couponPolicy.Valid() {
		return o, c.ErrUnavailable
	}
	if !wallet.ValidID(oid) {
		return o, c.ErrNotFound
	}
	tx, e := r.userTx(ctx, actor, "orders.pay")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	peek, e := order(ctx, tx, oid, false)
	if e != nil {
		return o, e
	}
	if e = lockOrderApp(ctx, tx, peek); e != nil {
		return o, e
	}
	if e = lockCouponOrderProducts(ctx, tx, peek); e != nil {
		return o, e
	}
	o, e = order(ctx, tx, oid, true)
	if e != nil {
		return o, e
	}
	var owner, sub, gift sql.NullString
	if e = tx.QueryRowContext(ctx, `SELECT actor_id::text,subscription_id::text,gift_id::text FROM commerce_v1.orders WHERE id=$1`, oid).Scan(&owner, &sub, &gift); e != nil {
		return o, e
	}
	if owner.Valid && owner.String != actor {
		return o, c.ErrNotFound
	}
	if sub.Valid || gift.Valid || o.Status != c.Unpaid || !o.ExpiredAt.After(r.now().UTC()) {
		return o, c.ErrConflict
	}
	app := strings.TrimPrefix(o.AppIdentifier, "developer.app:")
	if !wallet.ValidID(app) || o.AppIdentifier != "developer.app:"+app || len(o.Items) == 0 {
		return o, c.ErrUnavailable
	}
	if o.Coupon != nil {
		if o.Coupon.AccountID != actor {
			return o, c.ErrForbidden
		}
		if code != o.Coupon.Identifier && code != o.Coupon.CouponID {
			return o, c.ErrConflict
		}
		if e = r.validateCouponQuote(ctx, tx, actor, o, false); e != nil {
			return o, e
		}
		return o, tx.Commit()
	}
	quote, e := r.quoteCoupon(ctx, tx, actor, app, o.PayeeWalletID, "order", o.Currency, code, orderProducts(o), o.Amount)
	if e != nil {
		return o, e
	}
	o.Coupon = quote
	o.Amount = quote.Net
	if !preview {
		if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET actor_id=$2,amount=$3 WHERE id=$1`, oid, actor, o.Amount); e != nil {
			return o, e
		}
		if e = saveOrder(ctx, tx, o); e != nil {
			return o, e
		}
		if e = event(ctx, tx, "order.quoted", oid, o, r.now().UTC()); e != nil {
			return o, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "orders.pay"); e != nil {
		return o, e
	}
	if e = r.validateCouponQuote(ctx, tx, actor, o, false); e != nil {
		return o, e
	}
	if !o.ExpiredAt.After(r.now().UTC()) || !quote.ExpiresAt.After(r.now().UTC()) {
		return o, c.ErrConflict
	}
	return o, tx.Commit()
}

// Called inside the same transaction as the wallet hold, before payment and
// again after wallet/fulfillment waits. The locked coupon serializes all uses.
func (r *Repository) validateCouponQuote(ctx context.Context, tx *sql.Tx, actor string, o c.Order, redeemed bool) error {
	q := o.Coupon
	if q == nil {
		return nil
	}
	if !r.couponPolicy.Valid() || q.Policy != r.couponPolicy {
		return c.ErrUnavailable
	}
	if q.AccountID != actor || q.PayeeWalletID != o.PayeeWalletID || o.AppIdentifier != "developer.app:"+q.AppID || q.Currency != o.Currency || q.Net.Cmp(o.Amount) != 0 {
		return c.ErrConflict
	}
	switch q.Scope {
	case "order":
		if !reflect.DeepEqual(q.Products, orderProducts(o)) {
			return c.ErrConflict
		}
		gross, e := c.Total(o.Items)
		if e != nil || gross.Cmp(q.Gross) != 0 {
			return c.ErrConflict
		}
	case "subscription", "gift":
		if o.ProductIdentifier == nil || len(q.Products) != 1 || q.Products[0] != *o.ProductIdentifier {
			return c.ErrConflict
		}
		if q.Scope == "subscription" && len(o.Meta["subscription_id"]) == 0 || q.Scope == "gift" && len(o.Meta["gift_id"]) == 0 {
			return c.ErrConflict
		}
	default:
		return c.ErrConflict
	}
	checkoutTerms, e := couponCheckoutTerms(ctx, tx, q.AppID, q.PayeeWalletID, q.Scope, q.Products)
	if e != nil {
		return e
	}
	if q.CheckoutTermsHash == "" || q.CheckoutTermsHash != checkoutTerms {
		return c.ErrConflict
	}
	coupon, e := couponRow(ctx, tx, q.AppID, q.CouponID)
	if e != nil {
		return e
	}
	if e = couponIssuer(ctx, tx, q.AppID, q.PublisherID, q.PayeeWalletID); e != nil {
		return e
	}
	actual, e := c.QuoteCoupon(coupon, actor, q.AppID, q.PayeeWalletID, q.Scope, q.Currency, q.Products, q.Gross, r.now().UTC())
	if e != nil {
		return e
	}
	actual.CheckoutTermsHash = checkoutTerms
	if !reflect.DeepEqual(q, actual) {
		return c.ErrConflict
	}
	if redeemed {
		var b []byte
		e = tx.QueryRowContext(ctx, `SELECT quote FROM commerce_v1.coupon_redemptions WHERE order_id=$1 AND coupon_id=$2 AND account_id=$3`, o.ID, q.CouponID, actor).Scan(&b)
		if e != nil {
			return notFound(e)
		}
		var saved c.CouponQuote
		if e = json.Unmarshal(b, &saved); e != nil {
			return e
		}
		if !reflect.DeepEqual(q, &saved) {
			return c.ErrConflict
		}
		return nil
	}
	return couponCapacity(ctx, tx, coupon, actor)
}
func redeemCoupon(ctx context.Context, tx *sql.Tx, o c.Order, now time.Time) error {
	if o.Coupon == nil {
		return nil
	}
	q := o.Coupon
	b, e := jsonBytes(q)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.coupon_redemptions(order_id,coupon_id,account_id,gross,discount,net,currency,quote,redeemed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, o.ID, q.CouponID, q.AccountID, q.Gross, q.Discount, q.Net, q.Currency, b, now)
	return e
}

// Prevent silently changing a saved subscription/gift coupon into a fresh quote.
func sameCoupon(a, b *c.CouponQuote) bool { return reflect.DeepEqual(a, b) }

// CouponsReady is required only when the operator explicitly enabled this policy.
func (r *Repository) CouponsReady(ctx context.Context) error {
	if r.couponPolicy.Empty() {
		return nil
	}
	if !r.couponPolicy.Valid() || r.db == nil {
		return c.ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRowContext(ctx, `SELECT to_regclass('commerce_v1.coupons') IS NOT NULL AND to_regclass('commerce_v1.coupon_redemptions') IS NOT NULL`).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return c.ErrUnavailable
	}
	return nil
}
