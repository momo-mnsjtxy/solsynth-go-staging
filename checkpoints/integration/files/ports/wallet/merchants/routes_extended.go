// SPDX-License-Identifier: AGPL-3.0-only
package merchants

import (
	"net/http"
	"net/url"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"strconv"
	"strings"
)

func adminQuery(r *http.Request) (AdminQuery, error) {
	qv, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return AdminQuery{}, ErrInvalid
	}
	q := AdminQuery{Take: 50}
	allowed := map[string]bool{"take": true, "offset": true, "wallet_id": true, "account_id": true, "currency": true, "identifier": true, "app_identifier": true, "product_identifier": true, "status": true, "type": true}
	for k, v := range qv {
		if !allowed[k] || len(v) != 1 {
			return q, ErrUnsupported
		}
	}
	if qv.Has("take") {
		q.Take, err = strconv.Atoi(qv.Get("take"))
	}
	if err == nil && qv.Has("offset") {
		q.Offset, err = strconv.Atoi(qv.Get("offset"))
	}
	if err != nil {
		return q, ErrInvalid
	}
	q.WalletID = qv.Get("wallet_id")
	q.AccountID = qv.Get("account_id")
	q.Currency = qv.Get("currency")
	q.Identifier = qv.Get("identifier")
	q.AppIdentifier = qv.Get("app_identifier")
	q.ProductIdentifier = qv.Get("product_identifier")
	for k, dst := range map[string]**int{"status": &q.Status, "type": &q.Type} {
		if qv.Has(k) {
			v, e := strconv.Atoi(qv.Get(k))
			if e != nil {
				return q, ErrInvalid
			}
			*dst = &v
		}
	}
	return q, nil
}

func handleExtended(w http.ResponseWriter, r *http.Request, repo Repository, auth Auth, actor string) bool {
	admin, adminOK := repo.(AdminRepository)
	settlements, settlementOK := repo.(SettlementRepository)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var out any
	var err error
	status := http.StatusOK
	handled := true
	switch {
	case r.URL.Path == "/api/subscriptions/billing" && r.Method == http.MethodGet:
		if !adminOK {
			err = ErrUnavailable
			break
		}
		var q AdminQuery
		q, err = adminQuery(r)
		if err == nil {
			var page AdminPage[InboundOrder]
			page, err = admin.Billing(r.Context(), actor, q)
			out = page.Items
			if err == nil {
				w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
			}
		}
	case len(parts) >= 4 && strings.Join(parts[:3], "/") == "api/admin/payments":
		if !adminOK {
			err = ErrUnavailable
			break
		}
		switch {
		case len(parts) == 4 && parts[3] == "transactions" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "wallets.transactions.manage")
			var q AdminQuery
			if err == nil {
				q, err = adminQuery(r)
			}
			if err == nil {
				var page AdminPage[wallet.Transaction]
				page, err = admin.AdminTransactions(r.Context(), actor, q)
				out = page.Items
				if err == nil {
					w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
				}
			}
		case len(parts) == 5 && parts[3] == "transactions" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "wallets.transactions.manage")
			if err == nil {
				out, err = admin.AdminTransaction(r.Context(), actor, parts[4])
			}
		case len(parts) == 4 && parts[3] == "orders" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "orders.view")
			var q AdminQuery
			if err == nil {
				q, err = adminQuery(r)
			}
			if err == nil {
				var page AdminPage[Order]
				page, err = admin.AdminOrders(r.Context(), actor, q)
				out = page.Items
				if err == nil {
					w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
				}
			}
		case len(parts) == 5 && parts[3] == "orders" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "orders.view")
			if err == nil {
				out, err = admin.AdminOrder(r.Context(), actor, parts[4])
			}
		case len(parts) == 6 && parts[3] == "orders" && parts[5] == "refund" && r.Method == http.MethodPost:
			err = auth.Authorize(r, "orders.pay")
			if err == nil {
				err = empty(w, r)
			}
			if err == nil {
				out, err = admin.RefundOrder(r.Context(), actor, parts[4], r.Header.Get("Idempotency-Key"))
			}
		case len(parts) == 4 && parts[3] == "inbound-orders" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "orders.view")
			var q AdminQuery
			if err == nil {
				q, err = adminQuery(r)
			}
			if err == nil {
				var page AdminPage[InboundOrder]
				page, err = admin.AdminInboundOrders(r.Context(), actor, q)
				out = page.Items
				if err == nil {
					w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
				}
			}
		case len(parts) == 5 && parts[3] == "inbound-orders" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "orders.view")
			if err == nil {
				out, err = admin.AdminInboundOrder(r.Context(), actor, parts[4])
			}
		case len(parts) == 4 && parts[3] == "balance" && r.Method == http.MethodPost:
			err = auth.Authorize(r, "wallets.balance.modify")
			var v BalanceAdjustment
			if err == nil {
				err = decode(w, r, &v)
			}
			if err == nil {
				out, err = admin.AdjustBalance(r.Context(), actor, v, r.Header.Get("Idempotency-Key"))
			}
		default:
			handled = false
		}
	case len(parts) >= 3 && strings.Join(parts[:3], "/") == "api/admin/subscriptions" && !(len(parts) >= 4 && parts[3] == "catalog"):
		if !adminOK {
			err = ErrUnavailable
			break
		}
		err = auth.Authorize(r, "subscriptions.order.manage")
		switch {
		case len(parts) == 3 && r.Method == http.MethodGet:
			var q AdminQuery
			if err == nil {
				q, err = adminQuery(r)
			}
			if err == nil {
				var page AdminPage[commerce.Subscription]
				page, err = admin.AdminSubscriptions(r.Context(), actor, q)
				out = page.Items
				if err == nil {
					w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
				}
			}
		case len(parts) == 5 && parts[3] == "maintenance" && parts[4] == "quarantine" && r.Method == http.MethodGet:
			limit := 100
			if v := r.URL.Query().Get("limit"); v != "" {
				limit, _ = strconv.Atoi(v)
			}
			if err == nil {
				out, err = admin.MaintenanceFailures(r.Context(), actor, r.URL.Query().Get("after"), limit)
			}
		case len(parts) == 7 && parts[3] == "maintenance" && parts[4] == "quarantine" && parts[6] == "retry" && r.Method == http.MethodPost:
			if err == nil {
				err = empty(w, r)
			}
			if err == nil {
				err = admin.RetryMaintenance(r.Context(), actor, parts[5])
				status = http.StatusNoContent
			}
		case len(parts) == 5 && parts[3] == "maintenance" && parts[4] == "process-due" && r.Method == http.MethodPost:
			var v struct {
				BatchSize int `json:"batch_size"`
			}
			if err == nil && r.ContentLength != 0 {
				err = decode(w, r, &v)
			}
			if v.BatchSize == 0 {
				v.BatchSize = 100
			}
			if err == nil {
				var n int
				n, err = admin.RunMaintenance(r.Context(), actor, v.BatchSize)
				out = map[string]int{"affected_count": n}
			}
		default:
			handled = false
		}
	case len(parts) >= 4 && strings.Join(parts[:3], "/") == "api/admin/wallet-products":
		if !adminOK {
			err = ErrUnavailable
			break
		}
		switch {
		case len(parts) == 4 && parts[3] == "golds-resupply-pack" && r.Method == http.MethodGet:
			err = auth.Authorize(r, "orders.view")
			if err == nil {
				out, err = admin.WalletProductSummary(r.Context(), actor)
			}
		case len(parts) == 6 && parts[3] == "orders" && parts[5] == "apply" && r.Method == http.MethodPost:
			err = auth.Authorize(r, "orders.pay")
			var v WalletProductApply
			if err == nil {
				err = decode(w, r, &v)
			}
			if err == nil {
				out, err = admin.ApplyWalletProduct(r.Context(), actor, parts[4], v, r.Header.Get("Idempotency-Key"))
			}
		default:
			handled = false
		}
	case len(parts) >= 4 && parts[0] == "api" && parts[1] == "merchants" && (parts[3] == "settlements" || parts[3] == "stats"):
		if !settlementOK {
			err = ErrUnavailable
			break
		}
		publisher := parts[2]
		switch {
		case parts[3] == "settlements" && len(parts) == 4 && r.Method == http.MethodGet:
			var q Query
			q, err = query(r, false)
			if err == nil {
				var n int64
				out, n, err = settlements.Settlements(r.Context(), actor, publisher, q)
				if err == nil {
					w.Header().Set("X-Total", strconv.FormatInt(n, 10))
				}
			}
		case parts[3] == "settlements" && len(parts) == 5 && parts[4] == "pending" && r.Method == http.MethodGet:
			out, err = settlements.PendingSettlements(r.Context(), actor, publisher)
		case parts[3] == "settlements" && len(parts) == 5 && parts[4] == "settle" && r.Method == http.MethodPost:
			err = auth.Authorize(r, "merchants.settlements.manage")
			if err == nil {
				err = empty(w, r)
			}
			if err == nil {
				out, err = settlements.Settle(r.Context(), actor, publisher)
			}
		case parts[3] == "stats" && len(parts) == 5 && parts[4] == "overview" && r.Method == http.MethodGet:
			out, err = settlements.SettlementOverview(r.Context(), actor, publisher)
		case parts[3] == "stats" && len(parts) == 5 && (parts[4] == "incoming" || parts[4] == "daily") && r.Method == http.MethodGet:
			var q Query
			q, err = query(r, false)
			if err == nil {
				out, err = settlements.SettlementReport(r.Context(), actor, publisher, q)
			}
		default:
			handled = false
		}
	default:
		handled = false
	}
	if !handled {
		return false
	}
	if err != nil {
		failure(w, err)
		return true
	}
	respond(w, status, out)
	return true
}
