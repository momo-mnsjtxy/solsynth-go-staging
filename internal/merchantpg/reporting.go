// SPDX-License-Identifier: AGPL-3.0-only
package merchantpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"strconv"
	"strings"
)

// Scope is canonical publisher ancestry, never a shared payout wallet. Keeping
// soft-deleted app ancestry in historical reads prevents payout rebinding or
// app archival from transferring an order's reporting audience.
const orderFrom = ` FROM commerce_v1.orders o LEFT JOIN owned_custom_apps a ON a.id=o.app_id LEFT JOIN owned_dev_projects p ON p.id=a.project_id LEFT JOIN owned_developers d ON d.id=p.developer_id`
const orderWhere = ` WHERE (($1<>'' AND d.publisher_id::text=$1) OR($1='' AND o.actor_id::text=$2)) AND o.created_at>=$3 AND o.created_at<$4 AND ($5::integer IS NULL OR o.status=$5) AND($6='' OR o.currency=$6) AND($7='' OR o.app_id::text=$7) AND($8='' OR o.product_identifier=$8 OR EXISTS(SELECT 1 FROM commerce_v1.order_items i WHERE i.order_id=o.id AND i.identifier=$8))`

func (r *Repository) reportScope(ctx context.Context, tx *sql.Tx, actor, publisher string) (string, error) {
	if publisher == "" {
		return "", nil
	}
	p, e := r.publisher(ctx, tx, actor, publisher)
	return p.ID, e
}
func queryArgs(actor, pub string, q merchants.Query) []any {
	return []any{pub, actor, q.From, q.To, q.Status, q.Currency, q.AppID, q.ProductIdentifier}
}
func (r *Repository) Orders(ctx context.Context, actor, publisher string, q merchants.Query) ([]merchants.Order, int64, error) {
	q, e := merchants.NormalizeQuery(q, r.now())
	if e != nil {
		return nil, 0, e
	}
	permission := ""
	if publisher == "" {
		permission = "orders.view"
	}
	tx, e := r.begin(ctx, actor, permission)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	pub, e := r.reportScope(ctx, tx, actor, publisher)
	if e != nil {
		return nil, 0, e
	}
	args := append(queryArgs(actor, pub, q), q.Take, q.Offset)
	rows, e := tx.QueryContext(ctx, `WITH selected AS(SELECT o.id,o.document,o.created_at`+orderFrom+orderWhere+`) SELECT (SELECT count(*) FROM selected),p.document FROM (SELECT 1) seed LEFT JOIN LATERAL(SELECT document FROM selected ORDER BY created_at DESC,id LIMIT $9 OFFSET $10) p ON true`, args...)
	if e != nil {
		return nil, 0, e
	}
	out := []merchants.Order{}
	var count int64
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&count, &raw); e != nil {
			rows.Close()
			return nil, 0, e
		}
		if raw == nil {
			continue
		}
		var o merchants.Order
		if e = json.Unmarshal(raw, &o); e != nil {
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
	return out, count, tx.Commit()
}
func addDecimal(a, b json.Number) (json.Number, error) {
	one, ok := new(big.Rat).SetString(string(a))
	if !ok {
		return "", merchants.ErrUnavailable
	}
	two, ok := new(big.Rat).SetString(string(b))
	if !ok {
		return "", merchants.ErrUnavailable
	}
	return json.Number(one.Add(one, two).FloatString(3)), nil
}
func (r *Repository) Report(ctx context.Context, actor, publisher string, q merchants.Query) (merchants.Report, error) {
	q, e := merchants.NormalizeQuery(q, r.now())
	if e != nil {
		return merchants.Report{}, e
	}
	permission := ""
	if publisher == "" {
		permission = "orders.view"
	}
	tx, e := r.begin(ctx, actor, permission)
	if e != nil {
		return merchants.Report{}, e
	}
	defer tx.Rollback()
	pub, e := r.reportScope(ctx, tx, actor, publisher)
	if e != nil {
		return merchants.Report{}, e
	}
	out := merchants.Report{Basis: "orders", From: *q.From, To: *q.To, ByCurrency: map[string]merchants.CurrencyTotals{}, Daily: []merchants.Daily{}}
	rows, e := tx.QueryContext(ctx, `SELECT o.currency,o.status,(o.created_at AT TIME ZONE 'UTC')::date::text,count(*),sum(o.amount)::text`+orderFrom+orderWhere+` GROUP BY o.currency,o.status,(o.created_at AT TIME ZONE 'UTC')::date ORDER BY 3,1,2`, queryArgs(actor, pub, q)...)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var d merchants.Daily
		var amount string
		if e = rows.Scan(&d.Currency, &d.Status, &d.Date, &d.Count, &amount); e != nil {
			rows.Close()
			return merchants.Report{}, e
		}
		if strings.ContainsAny(amount, "eE") {
			rows.Close()
			return merchants.Report{}, merchants.ErrUnavailable
		}
		d.Amount = json.Number(amount)
		out.Daily = append(out.Daily, d)
		out.Count += d.Count
		bucket, ok := out.ByCurrency[d.Currency]
		if !ok {
			bucket = merchants.CurrencyTotals{Total: merchants.Total{Amount: json.Number("0.000")}, ByStatus: map[string]merchants.Total{}}
		}
		bucket.Count += d.Count
		bucket.Amount, e = addDecimal(bucket.Amount, d.Amount)
		if e != nil {
			rows.Close()
			return merchants.Report{}, e
		}
		key := strconv.Itoa(int(d.Status))
		status, ok := bucket.ByStatus[key]
		if !ok {
			status.Amount = json.Number("0.000")
		}
		status.Count += d.Count
		status.Amount, e = addDecimal(status.Amount, d.Amount)
		if e != nil {
			rows.Close()
			return merchants.Report{}, e
		}
		bucket.ByStatus[key] = status
		out.ByCurrency[d.Currency] = bucket
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return merchants.Report{}, e
	}
	return out, tx.Commit()
}
