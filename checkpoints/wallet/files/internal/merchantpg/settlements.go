// SPDX-License-Identifier: AGPL-3.0-only
package merchantpg

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"solsynth.local/go-migration/internal/walletpg"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/merchants"
)

func scanSettlement(row interface{ Scan(...any) error }) (merchants.Settlement, error) {
	var v merchants.Settlement
	err := row.Scan(&v.ID, &v.PublisherID, &v.AppID, &v.OrderID, &v.WalletID, &v.Currency, &v.Amount, &v.Status, &v.CreatedAt, &v.SettledAt)
	return v, mapped(err)
}

func (r *Repository) Settlements(ctx context.Context, actor, publisher string, q merchants.Query) ([]merchants.Settlement, int64, error) {
	q, err := merchants.NormalizeQuery(q, r.now())
	if err != nil {
		return nil, 0, err
	}
	tx, err := r.begin(ctx, actor, "")
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	pub, err := r.publisher(ctx, tx, actor, publisher)
	if err != nil {
		return nil, 0, err
	}
	where := `publisher_id=$1::uuid AND created_at>=$2 AND created_at<$3 AND($4::integer IS NULL OR status=$4) AND($5='' OR currency=$5) AND($6='' OR app_id::text=$6)`
	args := []any{pub.ID, q.From, q.To, q.Status, q.Currency, q.AppID}
	var total int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM commerce_v1.merchant_settlements WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id::text,publisher_id::text,app_id::text,order_id::text,wallet_id::text,currency,amount::text,status,created_at,settled_at FROM commerce_v1.merchant_settlements WHERE `+where+` ORDER BY created_at DESC,id OFFSET $7 LIMIT $8`, append(args, q.Offset, q.Take)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []merchants.Settlement{}
	for rows.Next() {
		v, e := scanSettlement(rows)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if _, err = r.publisher(ctx, tx, actor, publisher); err != nil {
		return nil, 0, err
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, actor); err != nil {
		return nil, 0, err
	}
	return out, total, tx.Commit()
}

func (r *Repository) PendingSettlements(ctx context.Context, actor, publisher string) (merchants.SettlementTotals, error) {
	tx, err := r.begin(ctx, actor, "")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pub, err := r.publisher(ctx, tx, actor, publisher)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT currency,count(*),sum(amount)::text FROM commerce_v1.merchant_settlements WHERE publisher_id=$1::uuid AND status=0 GROUP BY currency ORDER BY currency`, pub.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := merchants.SettlementTotals{}
	for rows.Next() {
		var currency, amount string
		var count int64
		if err = rows.Scan(&currency, &count, &amount); err != nil {
			return nil, err
		}
		out[currency] = merchants.Total{Count: count, Amount: json.Number(amount)}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if _, err = r.publisher(ctx, tx, actor, publisher); err != nil {
		return nil, err
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, actor); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (r *Repository) Settle(ctx context.Context, actor, publisher string) ([]wallet.Transaction, error) {
	tx, err := r.begin(ctx, actor, "merchants.settlements.manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pub, err := r.publisher(ctx, tx, actor, publisher)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id::text,order_id::text FROM commerce_v1.merchant_settlements WHERE publisher_id=$1::uuid AND status=0 ORDER BY id LIMIT 200 FOR UPDATE`, pub.ID)
	if err != nil {
		return nil, err
	}
	type item struct{ id, order string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.order); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	orders := make([]string, 0, len(items))
	for _, item := range items {
		orders = append(orders, item.order)
	}
	if err = walletpg.LockCommerceBatch(ctx, tx, orders); err != nil {
		return nil, err
	}
	out := []wallet.Transaction{}
	now := r.now().UTC()
	for _, v := range items {
		tr, e := walletpg.CommerceSettle(ctx, tx, v.order, false, now)
		if e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE commerce_v1.merchant_settlements SET status=1,settled_at=$2,settled_by=$3::uuid WHERE id=$1::uuid AND status=0`, v.id, now, actor); e != nil {
			return nil, e
		}
		out = append(out, tr)
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, "merchants.settlements.manage"); err != nil {
		return nil, err
	}
	if _, err = r.publisher(ctx, tx, actor, publisher); err != nil {
		return nil, err
	}
	if err = audit(ctx, tx, actor, "publisher:"+pub.ID, "settlements.settle", now); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (r *Repository) SettlementOverview(ctx context.Context, actor, publisher string) (merchants.SettlementOverview, error) {
	out := merchants.SettlementOverview{Pending: merchants.SettlementTotals{}, Settled: merchants.SettlementTotals{}, ThisMonth: merchants.SettlementTotals{}}
	tx, err := r.begin(ctx, actor, "")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	pub, err := r.publisher(ctx, tx, actor, publisher)
	if err != nil {
		return out, err
	}
	month := time.Date(r.now().UTC().Year(), r.now().UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := tx.QueryContext(ctx, `SELECT currency,status,created_at>=$2::timestamptz,count(*),sum(amount)::text FROM commerce_v1.merchant_settlements WHERE publisher_id=$1::uuid GROUP BY currency,status,created_at>=$2::timestamptz`, pub.ID, month)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var currency, amount string
		var status int
		var current bool
		var count int64
		if err = rows.Scan(&currency, &status, &current, &count, &amount); err != nil {
			return out, err
		}
		total := merchants.Total{Count: count, Amount: json.Number(amount)}
		out.TotalAllTime += count
		if status == 0 {
			out.TotalPending += count
			if err = mergeTotal(out.Pending, currency, total); err != nil {
				return out, err
			}
		}
		if status == 1 {
			out.TotalSettled += count
			if err = mergeTotal(out.Settled, currency, total); err != nil {
				return out, err
			}
		}
		if current {
			if err = mergeTotal(out.ThisMonth, currency, total); err != nil {
				return out, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if _, err = r.publisher(ctx, tx, actor, publisher); err != nil {
		return out, err
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, actor); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func mergeTotal(dst merchants.SettlementTotals, currency string, v merchants.Total) error {
	old := dst[currency]
	if old.Amount == "" {
		old.Amount = "0.000"
	}
	var err error
	old.Amount, err = addDecimal(old.Amount, v.Amount)
	old.Count += v.Count
	dst[currency] = old
	return err
}

func (r *Repository) SettlementReport(ctx context.Context, actor, publisher string, q merchants.Query) (merchants.Report, error) {
	q, err := merchants.NormalizeQuery(q, r.now())
	if err != nil {
		return merchants.Report{}, err
	}
	tx, err := r.begin(ctx, actor, "")
	if err != nil {
		return merchants.Report{}, err
	}
	defer tx.Rollback()
	pub, err := r.publisher(ctx, tx, actor, publisher)
	if err != nil {
		return merchants.Report{}, err
	}
	out := merchants.Report{Basis: "settlements", From: *q.From, To: *q.To, ByCurrency: map[string]merchants.CurrencyTotals{}, Daily: []merchants.Daily{}}
	rows, err := tx.QueryContext(ctx, `SELECT currency,status,(created_at AT TIME ZONE 'UTC')::date::text,count(*),sum(amount)::text FROM commerce_v1.merchant_settlements WHERE publisher_id=$1::uuid AND created_at>=$2 AND created_at<$3 AND($4='' OR currency=$4) GROUP BY currency,status,(created_at AT TIME ZONE 'UTC')::date ORDER BY 3,1,2`, pub.ID, q.From, q.To, q.Currency)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d merchants.Daily
		var amount string
		if err = rows.Scan(&d.Currency, &d.Status, &d.Date, &d.Count, &amount); err != nil {
			return out, err
		}
		d.Amount = json.Number(amount)
		out.Daily = append(out.Daily, d)
		out.Count += d.Count
		b := out.ByCurrency[d.Currency]
		if b.ByStatus == nil {
			b = merchants.CurrencyTotals{Total: merchants.Total{Amount: "0.000"}, ByStatus: map[string]merchants.Total{}}
		}
		b.Count += d.Count
		b.Amount, err = addDecimal(b.Amount, d.Amount)
		if err != nil {
			return out, err
		}
		k := strconv.Itoa(int(d.Status))
		s := b.ByStatus[k]
		if s.Amount == "" {
			s.Amount = "0.000"
		}
		s.Count += d.Count
		s.Amount, err = addDecimal(s.Amount, d.Amount)
		if err != nil {
			return out, err
		}
		b.ByStatus[k] = s
		out.ByCurrency[d.Currency] = b
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if _, err = r.publisher(ctx, tx, actor, publisher); err != nil {
		return out, err
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, actor); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
