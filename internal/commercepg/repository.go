// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
	"time"
)

//go:embed schema.sql
var SchemaSQL string

type AppClient struct{ AppID, PublisherID string }
type Product struct {
	ID, Identifier, Currency string
	Price                    json.Number
	Enabled                  bool
	StockMode                string
	StockQuantity            *int64
	Recurrence               string
	RequiresAddress          bool
}

// These adapters run inside the supplied transaction, locking canonical app
// credentials/ancestry and product rows through the commerce commit.
type Adapters struct {
	Client  func(context.Context, *sql.Tx, string, string, string) (AppClient, error)
	Product func(context.Context, *sql.Tx, string, string) (Product, error)
}
type Repository struct {
	namecards  NameCardFulfillment
	liveAwards LiveStreamAwardFulfillment
	db         *sql.DB
	guard      *authoritypg.TransactionGuard
	adapters   Adapters
	now        func() time.Time
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, adapters Adapters) *Repository {
	return &Repository{db: db, guard: guard, adapters: adapters, now: time.Now}
}
func (r *Repository) WithClock(now func() time.Time) *Repository {
	if now != nil {
		r.now = now
	}
	return r
}

var _ c.Store = (*Repository)(nil)

func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func jsonBytes(v any) ([]byte, error) { return json.Marshal(v) }
func notFound(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrNotFound
	}
	return e
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (r *Repository) begin(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, c.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		if r.guard == nil {
			tx.Rollback()
			return nil, c.ErrUnavailable
		}
		if permission == "" {
			_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
		} else {
			_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
		}
		if e != nil {
			tx.Rollback()
			return nil, e
		}
	}
	return tx, nil
}
func lock(ctx context.Context, tx *sql.Tx, key string) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "commerce:"+key)
	return e
}
func event(ctx context.Context, tx *sql.Tx, kind, idv string, v any, now time.Time) error {
	eid, e := id()
	if e != nil {
		return e
	}
	if g, ok := v.(c.Gift); ok {
		g.GiftCode = ""
		v = g
	}
	b, e := jsonBytes(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.outbox(event_id,event_type,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4,$5)`, eid, "commerce."+kind, idv, b, now)
	return e
}
func fingerprint(v any) (string, error) {
	b, e := jsonBytes(v)
	if e != nil {
		return "", e
	}
	f := sha256.Sum256(b)
	return hex.EncodeToString(f[:]), nil
}
func idem(ctx context.Context, tx *sql.Tx, owner, key, kind, fp string) (string, error) {
	if len(key) > 128 {
		return "", c.ErrInvalid
	}
	if key == "" {
		return "", nil
	}
	if e := lock(ctx, tx, "idem:"+owner+":"+key); e != nil {
		return "", e
	}
	var rid, prior, pk string
	e := tx.QueryRowContext(ctx, `SELECT resource_id::text,fingerprint,kind FROM commerce_v1.idempotency WHERE owner_key=$1 AND request_key=$2`, owner, key).Scan(&rid, &prior, &pk)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if prior != fp || pk != kind {
		return "", c.ErrIdempotency
	}
	return rid, nil
}
func recordIdem(ctx context.Context, tx *sql.Tx, owner, key, kind, fp, rid string) error {
	if key == "" {
		return nil
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO commerce_v1.idempotency(owner_key,request_key,fingerprint,resource_id,kind) VALUES($1,$2,$3,$4,$5)`, owner, key, fp, rid, kind)
	return e
}
func decode[T any](row *sql.Row) (T, error) {
	var v T
	var b []byte
	if e := row.Scan(&b); e != nil {
		return v, notFound(e)
	}
	e := json.Unmarshal(b, &v)
	return v, e
}
func order(ctx context.Context, tx *sql.Tx, idv string, locked bool) (c.Order, error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE"
	}
	return decode[c.Order](tx.QueryRowContext(ctx, `SELECT document FROM commerce_v1.orders WHERE id=$1`+suffix, idv))
}
func saveOrder(ctx context.Context, tx *sql.Tx, o c.Order) error {
	b, e := jsonBytes(o)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE commerce_v1.orders SET status=$2,payer_wallet_id=$3,transaction_id=$4,document=$5 WHERE id=$1`, o.ID, o.Status, o.PayerWalletID, o.TransactionID, b)
	return e
}
func insertOrder(ctx context.Context, tx *sql.Tx, o c.Order, app, actor, sub, gift string) error {
	b, e := jsonBytes(o)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.orders(id,app_id,actor_id,payer_wallet_id,payee_wallet_id,status,amount,currency,product_identifier,subscription_id,gift_id,created_at,expires_at,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, o.ID, nullable(app), nullable(actor), o.PayerWalletID, o.PayeeWalletID, o.Status, o.Amount, o.Currency, o.ProductIdentifier, nullable(sub), nullable(gift), o.CreatedAt, o.ExpiredAt, b)
	if e != nil {
		return e
	}
	return event(ctx, tx, "order.created", o.ID, o, o.CreatedAt)
}
func (r *Repository) client(ctx context.Context, tx *sql.Tx, credentials c.Credentials, scope string) (AppClient, error) {
	if r.adapters.Client == nil || credentials.ClientID == "" || credentials.ClientSecret == "" {
		return AppClient{}, c.ErrUnavailable
	}
	return r.adapters.Client(ctx, tx, credentials.ClientID, credentials.ClientSecret, scope)
}
func (r *Repository) CreateOrder(ctx context.Context, req c.CreateOrder, key string) (c.Order, error) {
	var o c.Order
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, req.Credentials, "orders.create")
	if e != nil {
		return o, e
	}
	credentials := req.Credentials
	req.ClientSecret = ""
	fp, e := fingerprint(req)
	if e != nil {
		return o, e
	}
	prior, e := idem(ctx, tx, "app:"+app.AppID, key, "order", fp)
	if e != nil {
		return o, e
	}
	if prior != "" {
		o, e = order(ctx, tx, prior, false)
		if e != nil {
			return o, e
		}
		return o, tx.Commit()
	}
	if req.DurationHours == 0 {
		req.DurationHours = 24
	}
	if req.DurationHours < 1 || req.DurationHours > 24*30 || len(req.Currency) > 128 || len(req.Items) > 100 || req.Remarks != nil && len(*req.Remarks) > 4096 {
		return o, c.ErrInvalid
	}
	if len(req.Meta) > 0 {
		b, e := jsonBytes(req.Meta)
		if e != nil || len(b) > 16384 {
			return o, c.ErrInvalid
		}
		for _, reserved := range []string{"subscription_id", "gift_id", "app_subscription", "fulfillment", "address_contact_id", "address_snapshot"} {
			if _, ok := req.Meta[reserved]; ok {
				return o, c.ErrInvalid
			}
		}
	}
	var payee string
	e = tx.QueryRowContext(ctx, `SELECT m.wallet_id::text FROM commerce_v1.merchants m JOIN wallet_v1.wallets w ON w.id=m.wallet_id JOIN owned_publishers p ON p.id=$2 WHERE m.app_id=$1 AND w.deleted_at IS NULL AND p.deleted_at IS NULL AND ((p.account_id IS NOT NULL AND p.account_id=w.account_id) OR (p.realm_id IS NOT NULL AND p.realm_id=w.realm_id)) FOR SHARE OF m,w,p`, app.AppID, app.PublisherID).Scan(&payee)
	if e != nil {
		return o, notFound(e)
	}
	if req.PayeeWalletID != nil && *req.PayeeWalletID != payee {
		return o, c.ErrForbidden
	}
	now := r.now().UTC()
	oid, e := id()
	if e != nil {
		return o, e
	}
	o = c.Order{ID: oid, Status: c.Unpaid, Currency: req.Currency, Amount: req.Amount, AppIdentifier: "developer.app:" + app.AppID, ProductIdentifier: req.ProductIdentifier, Remarks: req.Remarks, Meta: req.Meta, PayeeWalletID: payee, Items: []c.OrderItem{}, CreatedAt: now, UpdatedAt: now, ExpiredAt: now.Add(time.Duration(req.DurationHours) * time.Hour)}
	if o.Meta == nil {
		o.Meta = map[string]json.RawMessage{}
	}
	type selection struct {
		p        Product
		quantity int
	}
	items := []selection{}
	if len(req.Items) > 0 {
		if r.adapters.Product == nil {
			return o, c.ErrUnavailable
		}
		totals := map[string]int{}
		for _, item := range req.Items {
			if item.ProductIdentifier == "" || item.Quantity < 1 || item.Quantity > 1000 {
				return o, c.ErrInvalid
			}
			totals[item.ProductIdentifier] += item.Quantity
			if totals[item.ProductIdentifier] > 1000 {
				return o, c.ErrInvalid
			}
		}
		names := make([]string, 0, len(totals))
		for name := range totals {
			names = append(names, name)
		}
		sort.Strings(names)
		o.Amount = wallet.Amount{}
		o.Currency = ""
		o.ProductIdentifier = nil
		for _, name := range names {
			p, e := r.adapters.Product(ctx, tx, app.AppID, name)
			if e != nil {
				return o, e
			}
			if !p.Enabled || !c.ValidPrice(p.Price) || p.RequiresAddress || p.Recurrence != "" && p.Recurrence != "none" {
				return o, c.ErrUnavailable
			}
			if o.Currency == "" {
				o.Currency = p.Currency
			}
			if p.Currency != o.Currency {
				return o, c.ErrInvalid
			}
			if p.StockMode != "unlimited" {
				if p.StockMode != "manual" || p.StockQuantity == nil || *p.StockQuantity < 0 {
					return o, c.ErrUnavailable
				}
				var used int64
				e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(i.quantity),0) FROM commerce_v1.order_items i JOIN commerce_v1.orders o ON o.id=i.order_id WHERE i.product_id=$1 AND (o.status IN (1,3) OR o.status=0 AND o.expires_at>$2)`, p.ID, now).Scan(&used)
				if e != nil {
					return o, e
				}
				if used+int64(totals[name]) > *p.StockQuantity {
					return o, c.ErrStock
				}
			}
			o.Items = append(o.Items, c.OrderItem{ProductIdentifier: name, Quantity: totals[name], UnitPrice: p.Price, Currency: p.Currency})
			items = append(items, selection{p, totals[name]})
		}
	}
	if len(o.Items) > 0 {
		o.Amount, e = c.Total(o.Items)
		if e != nil {
			return o, e
		}
	}
	if _, e = r.client(ctx, tx, credentials, "orders.create"); e != nil {
		return o, e
	}
	if o.Amount.Sign() <= 0 || strings.TrimSpace(o.Currency) == "" || len(o.Currency) > 128 {
		return o, c.ErrInvalid
	}
	if e = insertOrder(ctx, tx, o, app.AppID, "", "", ""); e != nil {
		return o, e
	}
	for _, item := range items {
		_, e = tx.ExecContext(ctx, `INSERT INTO commerce_v1.order_items(order_id,product_id,identifier,quantity) VALUES($1,$2,$3,$4)`, o.ID, item.p.ID, item.p.Identifier, item.quantity)
		if e != nil {
			return o, e
		}
	}
	if e = recordIdem(ctx, tx, "app:"+app.AppID, key, "order", fp, o.ID); e != nil {
		return o, e
	}
	return o, tx.Commit()
}
func ownOrder(ctx context.Context, tx *sql.Tx, actor string, o c.Order) error {
	var ok bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commerce_v1.orders o LEFT JOIN wallet_v1.wallets w ON w.id=o.payer_wallet_id WHERE o.id=$1 AND (o.actor_id=$2 OR w.account_id=$2))`, o.ID, actor).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return c.ErrNotFound
	}
	return nil
}
func (r *Repository) GetOrder(ctx context.Context, actor, idv string, public bool) (c.Order, error) {
	var o c.Order
	if !wallet.ValidID(idv) {
		return o, c.ErrNotFound
	}
	if !public && !wallet.ValidID(actor) {
		return o, c.ErrForbidden
	}
	tx, e := r.begin(ctx, actor, "")
	if e != nil {
		return o, e
	}
	defer tx.Rollback()
	o, e = order(ctx, tx, idv, false)
	if e != nil {
		return o, e
	}
	if !public {
		if e = ownOrder(ctx, tx, actor, o); e != nil {
			return o, e
		}
	} else {
		o.Meta = map[string]json.RawMessage{}
		o.PayerWalletID = nil
		o.TransactionID = nil
	}
	if e = enrichOrder(ctx, tx, &o); e != nil {
		return o, e
	}

	if o.Status == c.Unpaid && !o.ExpiredAt.After(r.now()) {
		o.Status = c.Expired
	}
	return o, tx.Commit()
}
func bounds(q c.PageQuery) c.PageQuery {
	if q.Offset < 0 {
		q.Offset = 0
	}
	if q.Take <= 0 {
		q.Take = 20
	}
	if q.Take > 200 {
		q.Take = 200
	}
	return q
}
func (r *Repository) ListOrders(ctx context.Context, actor string, q c.PageQuery) ([]c.Order, int, error) {
	out := []c.Order{}
	q = bounds(q)
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	const where = ` FROM commerce_v1.orders o LEFT JOIN wallet_v1.wallets w ON w.id=o.payer_wallet_id WHERE (o.actor_id=$1 OR w.account_id=$1) AND ($2::integer IS NULL OR o.status=$2) AND ($3='' OR o.document->>'app_identifier'=$3) AND ($4='' OR o.product_identifier=$4)`
	var total int
	e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, actor, q.Status, q.AppIdentifier, q.ProductIdentifier).Scan(&total)
	if e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT o.document`+where+` ORDER BY o.created_at DESC,o.id OFFSET $5 LIMIT $6`, actor, q.Status, q.AppIdentifier, q.ProductIdentifier, q.Offset, q.Take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var b []byte
		var o c.Order
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &o)
		}
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, o)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) Metrics(ctx context.Context, creds c.Credentials, start, end *time.Time) (c.Metrics, error) {
	m := c.Metrics{ProductIncomingAmounts: map[string]wallet.Amount{}, ProductOrderCounts: map[string]int{}}
	tx, e := r.begin(ctx, "", "")
	if e != nil {
		return m, e
	}
	defer tx.Rollback()
	app, e := r.client(ctx, tx, creds, "orders.metrics.read")
	if e != nil {
		return m, e
	}
	m.AppIdentifier = "developer.app:" + app.AppID
	// The source aggregates mixed currencies. This policy refuses to produce a
	// dimensionally invalid monetary total instead of silently summing currencies.
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.orders WHERE app_id=$1 AND ($2::timestamptz IS NULL OR created_at >= $2) AND ($3::timestamptz IS NULL OR created_at <= $3) ORDER BY created_at,id`, app.AppID, start, end)
	if e != nil {
		return m, e
	}
	currency := ""
	for rows.Next() {
		var b []byte
		var o c.Order
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &o)
		}
		if e != nil {
			rows.Close()
			return m, e
		}
		if currency == "" {
			currency = o.Currency
		}
		if currency != o.Currency {
			rows.Close()
			return m, c.ErrConflict
		}
		m.TotalOrders++
		switch o.Status {
		case c.Unpaid:
			m.UnpaidOrders++
		case c.Paid:
			m.PaidOrders++
		case c.Cancelled:
			m.CancelledOrders++
		case c.Finished:
			m.FinishedOrders++
		case c.Expired:
			m.ExpiredOrders++
		}
		m.TotalIncomingAmount, e = m.TotalIncomingAmount.Add(o.Amount)
		if e != nil {
			rows.Close()
			return m, e
		}
		if o.Status == c.Paid || o.Status == c.Finished {
			m.PaidIncomingAmount, e = m.PaidIncomingAmount.Add(o.Amount)
			if e != nil {
				rows.Close()
				return m, e
			}
		}
		name := ""
		if o.ProductIdentifier != nil {
			name = *o.ProductIdentifier
		}
		m.ProductIncomingAmounts[name], e = m.ProductIncomingAmounts[name].Add(o.Amount)
		if e != nil {
			rows.Close()
			return m, e
		}
		m.ProductOrderCounts[name]++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return m, e
	}
	return m, tx.Commit()
}
func failure(e error) error {
	var f *authority.Failure
	if errors.As(e, &f) {
		return e
	}
	return fmt.Errorf("commerce persistence: %w", e)
}
