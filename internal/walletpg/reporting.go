// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// RealmReadPolicy follows the authority-account, realm-lifecycle, wallet lock
// order. It rechecks current membership in the caller's ReadCommitted tx.
type RealmReadPolicy interface {
	AuthorizeRealmRead(context.Context, *sql.Tx, string, string) error
}

func (r *Repository) WithRealmReadPolicy(p RealmReadPolicy) *Repository { r.realmReads = p; return r }
func reportError(e error) error {
	var f *authority.Failure
	if errors.As(e, &f) {
		if f.Status == 401 {
			return a.ErrUnauthorized
		}
		if f.Status == 403 || f.Status == 423 {
			return a.ErrForbidden
		}
		return a.ErrUnavailable
	}
	return e
}
func (r *Repository) reportAuthority(ctx context.Context, tx *sql.Tx, actor string) error {
	if r.authority == nil {
		return nil
	}
	_, e := r.authority.Authenticate(ctx, r.db, tx, actor)
	return reportError(e)
}
func (r *Repository) reportScope(ctx context.Context, tx *sql.Tx, actor string, ids []string) ([]string, error) {
	if !a.ValidID(actor) || len(ids) > 100 {
		return nil, a.ErrInvalid
	}
	if e := r.reportAuthority(ctx, tx, actor); e != nil {
		return nil, e
	}
	if len(ids) == 0 {
		var id string
		e := tx.QueryRowContext(ctx, `SELECT id::text FROM wallet_v1.wallets WHERE account_id=$1 AND deleted_at IS NULL ORDER BY is_primary DESC,created_at,id LIMIT 1`, actor).Scan(&id)
		if e != nil {
			return nil, mapErr(e)
		}
		ids = []string{id}
	}
	seen := map[string]bool{}
	realmIDs := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if !a.ValidID(id) {
			return nil, a.ErrInvalid
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	for _, id := range out {
		w, e := wallet(ctx, tx, id, false)
		if e != nil {
			return nil, e
		}
		if w.AccountID != nil {
			if *w.AccountID != actor {
				return nil, a.ErrNotFound
			}
		} else if w.RealmID != nil {
			realmIDs[*w.RealmID] = true
		} else {
			return nil, a.ErrNotFound
		}
	}
	realms := []string{}
	for id := range realmIDs {
		realms = append(realms, id)
	}
	sort.Strings(realms)
	for _, id := range realms {
		if r.realmReads != nil {
			if e := r.realmReads.AuthorizeRealmRead(ctx, tx, actor, id); e != nil {
				return nil, e
			}
		} else {
			var role string
			e := tx.QueryRowContext(ctx, `SELECT role FROM wallet_v1.realm_grants WHERE account_id=$1 AND realm_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>statement_timestamp()) FOR SHARE`, actor, id).Scan(&role)
			if errors.Is(e, sql.ErrNoRows) {
				return nil, a.ErrNotFound
			}
			if e != nil {
				return nil, e
			}
			if role != "viewer" && role != "editor" {
				return nil, a.ErrNotFound
			}
		}
	}
	// Current lifecycle and immutable ownership are checked again after locks.
	for _, id := range out {
		w, e := scanWallet(tx.QueryRowContext(ctx, `SELECT `+walletCols+` FROM wallet_v1.wallets WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id))
		if e != nil {
			return nil, e
		}
		if w.AccountID != nil && *w.AccountID != actor || w.RealmID != nil && !realmIDs[*w.RealmID] {
			return nil, a.ErrNotFound
		}
	}
	return out, nil
}

func (r *Repository) finishReport(ctx context.Context, tx *sql.Tx, actor string, ids []string) error {
	// Scope can expire while a later wallet lock or the report query waits.
	// Reuse the same selected IDs and transaction so a previously valid realm
	// grant or realm-owner lifecycle observation cannot authorize a stale read.
	if _, e := r.reportScope(ctx, tx, actor, ids); e != nil {
		return e
	}
	if e := r.reportAuthority(ctx, tx, actor); e != nil {
		return e
	}
	return tx.Commit()
}

type reportGroup struct {
	Type     string   `json:"type"`
	Currency string   `json:"currency"`
	Income   a.Amount `json:"income"`
	Spending a.Amount `json:"spending"`
}

// Every amount is a settled canonical ledger event. Holds, cancelled operations
// and refunded reservations have no realized flow. Internal selected-wallet
// transfers appear on both sides and net to zero, once per transaction.
const reportSelection = `WITH selected AS (
 SELECT * FROM wallet_v1.transactions WHERE deleted_at IS NULL
 AND (payer_wallet_id::text=ANY($1::text[]) OR payee_wallet_id::text=ANY($1::text[]))
 AND ($2::timestamptz IS NULL OR created_at >= $2) AND ($3::timestamptz IS NULL OR created_at <= $3)
 AND ($4::text[] IS NULL OR lower(currency)=ANY($4::text[]))
), grouped AS (
 SELECT CASE WHEN purpose='commerce' THEN 'Payment' ELSE 'Transfer' END AS kind,currency,
 sum(CASE WHEN payee_wallet_id::text=ANY($1::text[]) THEN amount ELSE 0 END) AS income,
 sum(CASE WHEN payer_wallet_id::text=ANY($1::text[]) THEN amount ELSE 0 END) AS spending
 FROM selected WHERE status=2 GROUP BY 1,currency
)
SELECT (SELECT count(*) FROM selected),
 (SELECT count(*) FROM commerce_v1.orders WHERE payee_wallet_id::text=ANY($1::text[])
 AND ($2::timestamptz IS NULL OR created_at >= $2) AND ($3::timestamptz IS NULL OR created_at <= $3)
 AND ($4::text[] IS NULL OR lower(currency)=ANY($4::text[]))),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('type',kind,'currency',currency,'income',income,'spending',spending) ORDER BY kind,currency) FROM grouped),'[]'::jsonb)`

func reportSummary(groups []reportGroup) (map[string]a.TransactionSummary, *a.Amount, *a.Amount, *a.Amount, error) {
	out := map[string]a.TransactionSummary{}
	currencies := map[string]bool{}
	var income, spending a.Amount
	for _, g := range groups {
		s, ok := out[g.Type]
		if !ok {
			s = a.TransactionSummary{Type: g.Type, Currencies: map[string]a.CurrencySummary{}}
		}
		if _, exists := s.Currencies[g.Currency]; exists {
			return nil, nil, nil, nil, a.ErrUnavailable
		}
		net, e := g.Income.Sub(g.Spending)
		if e != nil {
			return nil, nil, nil, nil, e
		}
		s.Currencies[g.Currency] = a.CurrencySummary{Currency: g.Currency, Income: g.Income, Spending: g.Spending, Net: net}
		out[g.Type] = s
		currencies[g.Currency] = true
	}
	if len(currencies) > 1 {
		return out, nil, nil, nil, nil
	}
	for _, g := range groups {
		var e error
		income, e = income.Add(g.Income)
		if e != nil {
			return nil, nil, nil, nil, e
		}
		spending, e = spending.Add(g.Spending)
		if e != nil {
			return nil, nil, nil, nil, e
		}
	}
	net, e := income.Sub(spending)
	return out, &income, &spending, &net, e
}
func (r *Repository) WalletStats(ctx context.Context, actor string, q a.StatsQuery) (out a.WalletStats, e error) {
	q, e = a.NormalizeStatsQuery(q)
	if e != nil {
		return out, e
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	ids, e := r.reportScope(ctx, tx, actor, q.Wallets)
	if e != nil {
		return out, e
	}
	out.PeriodEnd = r.now().UTC()
	out.PeriodBegin = out.PeriodEnd.Add(-time.Duration(q.Period) * 24 * time.Hour)
	var raw []byte
	e = tx.QueryRowContext(ctx, reportSelection, ids, out.PeriodBegin, out.PeriodEnd, q.Currencies).Scan(&out.TotalTransactions, &out.TotalOrders, &raw)
	if e != nil {
		return out, e
	}
	var groups []reportGroup
	if e = json.Unmarshal(raw, &groups); e != nil {
		return out, e
	}
	out.Summary, out.TotalIncome, out.TotalOutgoing, out.Sum, e = reportSummary(groups)
	if e != nil {
		return out, e
	}
	out.IncomeCategories = map[string]*a.Amount{}
	out.OutgoingCategories = map[string]*a.Amount{}
	for key, s := range out.Summary {
		if len(s.Currencies) > 1 {
			out.IncomeCategories[key] = nil
			out.OutgoingCategories[key] = nil
			continue
		}
		for _, v := range s.Currencies {
			income, spending := v.Income, v.Spending
			if income.Sign() != 0 {
				out.IncomeCategories[key] = &income
			}
			if spending.Sign() != 0 {
				out.OutgoingCategories[key] = &spending
			}
		}
	}
	if e = r.finishReport(ctx, tx, actor, ids); e != nil {
		return a.WalletStats{}, e
	}
	return out, nil
}
func (r *Repository) WalletOverview(ctx context.Context, actor string, q a.OverviewQuery) (out a.WalletOverview, e error) {
	if !a.ValidOverviewQuery(q) {
		return out, a.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	ids, e := r.reportScope(ctx, tx, actor, nil)
	if e != nil {
		return out, e
	}
	out.AccountID = actor
	out.StartDate = q.StartDate
	out.EndDate = q.EndDate
	var raw []byte
	var transactions, orders int64
	e = tx.QueryRowContext(ctx, reportSelection, ids, q.StartDate, q.EndDate, nil).Scan(&transactions, &orders, &raw)
	if e != nil {
		return out, e
	}
	var groups []reportGroup
	if e = json.Unmarshal(raw, &groups); e != nil {
		return out, e
	}
	out.Summary, out.TotalIncome, out.TotalSpending, out.NetTotal, e = reportSummary(groups)
	if e != nil {
		return out, e
	}
	if e = r.finishReport(ctx, tx, actor, ids); e != nil {
		return a.WalletOverview{}, e
	}
	return out, nil
}

const orderHistorySQL = `WITH matching AS (
 SELECT o.id,o.created_at,o.document || jsonb_build_object('id',o.id,'status',o.status,'amount',o.amount,'currency',o.currency,'payer_wallet_id',o.payer_wallet_id,'payee_wallet_id',o.payee_wallet_id,'transaction_id',o.transaction_id,'created_at',o.created_at,'expired_at',o.expires_at) AS document,
 jsonb_build_object('id',t.id,'type',CASE WHEN t.purpose='commerce' THEN 1 ELSE 0 END,'payer_wallet_id',t.payer_wallet_id,'payee_wallet_id',t.payee_wallet_id,'currency',t.currency,'amount',t.amount,'remarks',t.remarks,'status',t.status,'is_frozen',t.is_frozen,'require_confirmation',t.require_confirmation,'frozen_at',t.frozen_at,'expires_at',t.expires_at,'confirmed_at',t.confirmed_at,'confirmation_received_at',t.confirmation_received_at,'created_at',t.created_at,'updated_at',t.updated_at,'deleted_at',t.deleted_at) AS transaction
 FROM commerce_v1.orders o JOIN wallet_v1.transactions t ON t.id=o.transaction_id AND t.purpose='commerce' AND t.managed_by=o.id
 WHERE t.deleted_at IS NULL AND(t.payer_wallet_id=$1 OR t.payee_wallet_id=$1)
 AND($2='' OR($2='in' AND t.payee_wallet_id=$1)OR($2='out' AND t.payer_wallet_id=$1))
 AND($3::integer IS NULL OR $3=1)AND($4::integer IS NULL OR o.status=$4)
), page AS (SELECT * FROM matching ORDER BY created_at DESC,id DESC OFFSET $5 LIMIT $6)
SELECT (SELECT count(*) FROM matching),COALESCE((SELECT jsonb_agg(jsonb_build_object('order',document,'transaction',transaction) ORDER BY created_at DESC,id DESC) FROM page),'[]'::jsonb)`

func (r *Repository) WalletOrders(ctx context.Context, actor string, q a.OrderHistoryQuery) (out a.OrderHistoryPage, e error) {
	out.Items = []json.RawMessage{}
	q, e = a.NormalizeOrderHistoryQuery(q)
	if e != nil {
		return out, e
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var ids []string
	if q.WalletID != "" {
		ids = []string{q.WalletID}
	}
	ids, e = r.reportScope(ctx, tx, actor, ids)
	if e != nil {
		return out, e
	}
	var raw []byte
	e = tx.QueryRowContext(ctx, orderHistorySQL, ids[0], q.Direction, q.Type, q.Status, q.Offset, q.Take).Scan(&out.Total, &raw)
	if e != nil {
		return out, e
	}
	var items []struct {
		Order       c.Order       `json:"order"`
		Transaction a.Transaction `json:"transaction"`
	}
	if e = json.Unmarshal(raw, &items); e != nil {
		return out, e
	}
	for _, v := range items {
		// A realm viewer or beneficiary may read ledger history, not a purchaser's
		// arbitrary private metadata, coupon state or shipping/fulfillment snapshot.
		v.Order.Meta = map[string]json.RawMessage{}
		v.Order.Coupon = nil
		b, x := json.Marshal(struct {
			c.Order
			Transaction a.Transaction `json:"transaction"`
		}{v.Order, v.Transaction})
		if x != nil {
			return out, x
		}
		out.Items = append(out.Items, b)
	}
	if e = r.finishReport(ctx, tx, actor, ids); e != nil {
		return a.OrderHistoryPage{}, e
	}
	return out, nil
}

var _ a.ReportingRepository = (*Repository)(nil)
