// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Report totals are null when unlike currencies cannot be added. Summary keeps
// exact per-currency values; a single currency retains the authored wire shape.
type CurrencySummary struct {
	Currency string `json:"currency"`
	Income   Amount `json:"income"`
	Spending Amount `json:"spending"`
	Net      Amount `json:"net"`
}
type TransactionSummary struct {
	Type       string                     `json:"type"`
	Currencies map[string]CurrencySummary `json:"currencies"`
}
type WalletOverview struct {
	AccountID     string                        `json:"account_id"`
	StartDate     *time.Time                    `json:"start_date"`
	EndDate       *time.Time                    `json:"end_date"`
	Summary       map[string]TransactionSummary `json:"summary"`
	TotalIncome   *Amount                       `json:"total_income"`
	TotalSpending *Amount                       `json:"total_spending"`
	NetTotal      *Amount                       `json:"net_total"`
}
type WalletStats struct {
	PeriodBegin        time.Time                     `json:"period_begin"`
	PeriodEnd          time.Time                     `json:"period_end"`
	TotalTransactions  int64                         `json:"total_transactions"`
	TotalOrders        int64                         `json:"total_orders"`
	IncomeCategories   map[string]*Amount            `json:"income_categories"`
	OutgoingCategories map[string]*Amount            `json:"outgoing_categories"`
	TotalIncome        *Amount                       `json:"total_income"`
	TotalOutgoing      *Amount                       `json:"total_outgoing"`
	Sum                *Amount                       `json:"sum"`
	Summary            map[string]TransactionSummary `json:"summary"`
}
type StatsQuery struct {
	Period              int
	Wallets, Currencies []string
}
type OverviewQuery struct{ StartDate, EndDate *time.Time }
type OrderHistoryQuery struct {
	TransactionQuery
	Status *int
}

// Orders remain commerce-owned documents, serialized only after the repository
// has applied the wallet-history disclosure policy. No client JSON is forwarded.
type OrderHistoryPage struct {
	Items []json.RawMessage
	Total int64
}
type ReportingRepository interface {
	WalletStats(context.Context, string, StatsQuery) (WalletStats, error)
	WalletOverview(context.Context, string, OverviewQuery) (WalletOverview, error)
	WalletOrders(context.Context, string, OrderHistoryQuery) (OrderHistoryPage, error)
}

func NormalizeStatsQuery(q StatsQuery) (StatsQuery, error) {
	if q.Period < 1 || q.Period > 3660 || len(q.Wallets) > 100 || len(q.Currencies) > 32 {
		return q, ErrInvalid
	}
	ids := map[string]bool{}
	for _, id := range q.Wallets {
		if !ValidID(id) {
			return q, ErrInvalid
		}
		ids[id] = true
	}
	q.Wallets = []string{}
	for id := range ids {
		q.Wallets = append(q.Wallets, id)
	}
	sort.Strings(q.Wallets)
	currencies := map[string]bool{}
	for _, c := range q.Currencies {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !utf8.ValidString(c) || utf8.RuneCountInString(c) > 128 || strings.ContainsAny(c, "\x00\r\n") {
			return q, ErrInvalid
		}
		currencies[strings.ToLower(c)] = true
	}
	if len(currencies) == 0 {
		currencies["points"] = true
	}
	q.Currencies = []string{}
	for c := range currencies {
		q.Currencies = append(q.Currencies, c)
	}
	sort.Strings(q.Currencies)
	return q, nil
}
func NormalizeOrderHistoryQuery(q OrderHistoryQuery) (OrderHistoryQuery, error) {
	if q.WalletID != "" && !ValidID(q.WalletID) || q.Offset < 0 || q.Take < 0 || q.Take > 100 || q.Status != nil && (*q.Status < 0 || *q.Status > 4) || q.Type != nil && (*q.Type < 0 || *q.Type > 1) {
		return q, ErrInvalid
	}
	switch strings.ToLower(strings.TrimSpace(q.Direction)) {
	case "":
		q.Direction = ""
	case "in", "income", "incoming":
		q.Direction = "in"
	case "out", "outcome", "outgoing":
		q.Direction = "out"
	default:
		return q, ErrInvalid
	}
	return q, nil
}
func ValidOverviewQuery(q OverviewQuery) bool {
	return (q.StartDate == nil || q.StartDate.Year() >= 1 && q.StartDate.Year() <= 9999) && (q.EndDate == nil || q.EndDate.Year() >= 1 && q.EndDate.Year() <= 9999) && (q.StartDate == nil || q.EndDate == nil || !q.StartDate.After(*q.EndDate))
}
func reportScalar(q url.Values, key string) (string, error) {
	v, ok := q[key]
	if !ok {
		return "", nil
	}
	if len(v) != 1 || v[0] == "" {
		return "", ErrInvalid
	}
	return v[0], nil
}
func reportInteger(q url.Values, key string, fallback int) (int, error) {
	s, e := reportScalar(q, key)
	if e != nil {
		return 0, e
	}
	if s == "" {
		return fallback, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil {
		return 0, ErrInvalid
	}
	return n, nil
}
func reportDate(q url.Values, key string) (*time.Time, error) {
	s, e := reportScalar(q, key)
	if e != nil || s == "" {
		return nil, e
	}
	v, e := time.Parse(time.RFC3339Nano, s)
	if e != nil {
		return nil, ErrInvalid
	}
	v = v.UTC()
	return &v, nil
}
func (h *Handler) reportingHTTP(w http.ResponseWriter, r *http.Request, p Principal, suffix string) bool {
	if r.Method != "GET" || (suffix != "/stats" && suffix != "/overview" && suffix != "/orders") {
		return false
	}
	repo, ok := h.Service.Store.(ReportingRepository)
	if !ok {
		fail(w, ErrUnavailable)
		return true
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		fail(w, ErrInvalid)
		return true
	}
	var value any
	switch suffix {
	case "/stats":
		n, x := reportInteger(q, "period", 30)
		if x != nil {
			e = x
			break
		}
		v, x := NormalizeStatsQuery(StatsQuery{Period: n, Wallets: q["wallets"], Currencies: q["currencies"]})
		if x != nil {
			e = x
			break
		}
		value, e = repo.WalletStats(r.Context(), p.AccountID, v)
	case "/overview":
		start, x := reportDate(q, "startDate")
		if x != nil {
			e = x
			break
		}
		end, x := reportDate(q, "endDate")
		if x != nil {
			e = x
			break
		}
		v := OverviewQuery{start, end}
		if !ValidOverviewQuery(v) {
			e = ErrInvalid
			break
		}
		value, e = repo.WalletOverview(r.Context(), p.AccountID, v)
	case "/orders":
		v := OrderHistoryQuery{}
		v.WalletID, e = reportScalar(q, "wallet")
		if e != nil {
			break
		}
		v.Direction, e = reportScalar(q, "direction")
		if e != nil {
			break
		}
		v.Offset, e = reportInteger(q, "offset", 0)
		if e != nil {
			break
		}
		v.Take, e = reportInteger(q, "take", 20)
		if e != nil {
			break
		}
		if raw, x := reportScalar(q, "type"); x != nil {
			e = x
			break
		} else if raw != "" {
			n, x := strconv.Atoi(raw)
			if x != nil {
				switch strings.ToLower(raw) {
				case "transfer":
					n = 0
				case "payment":
					n = 1
				default:
					e = ErrInvalid
				}
			}
			v.Type = &n
		}
		if e != nil {
			break
		}
		if raw, x := reportScalar(q, "status"); x != nil {
			e = x
			break
		} else if raw != "" {
			n, x := strconv.Atoi(raw)
			if x != nil {
				switch strings.ToLower(raw) {
				case "unpaid":
					n = 0
				case "paid":
					n = 1
				case "cancelled":
					n = 2
				case "finished":
					n = 3
				case "expired":
					n = 4
				default:
					e = ErrInvalid
				}
			}
			v.Status = &n
		}
		if e != nil {
			break
		}
		v, e = NormalizeOrderHistoryQuery(v)
		if e != nil {
			break
		}
		var page OrderHistoryPage
		page, e = repo.WalletOrders(r.Context(), p.AccountID, v)
		if e == nil {
			value = page.Items
			w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
		}
	}
	if e != nil {
		fail(w, e)
	} else {
		writeJSON(w, 200, value)
	}
	return true
}
