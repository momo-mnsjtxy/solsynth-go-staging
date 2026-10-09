// SPDX-License-Identifier: AGPL-3.0-only
package apps

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
)

// DeveloperStats is the authored controller's aggregate projection. Individual
// private app/project documents are never included in this public response.
type DeveloperStats struct {
	TotalCustomApps int64 `json:"total_custom_apps"`
}

type StatisticsRepository interface {
	DeveloperStatistics(context.Context, string) (DeveloperStats, error)
}

// CustomOrderRequest follows the retained private CustomAppController request,
// separately from the app-credential product-catalog CreateOrder contract.
type CustomOrderRequest struct {
	Identifier      string                     `json:"identifier"`
	Currency        string                     `json:"currency"`
	Amount          CustomOrderAmount          `json:"amount"`
	ExpirationHours *int                       `json:"expiration_hours"`
	Remarks         *string                    `json:"remarks"`
	Meta            map[string]json.RawMessage `json:"meta"`
}

type CustomOrderAmount string

func (a *CustomOrderAmount) UnmarshalJSON(raw []byte) error {
	if a == nil || len(raw) == 0 || raw[0] == '"' || string(raw) == "null" {
		return ErrInvalid
	}
	var n json.Number
	if e := json.Unmarshal(raw, &n); e != nil {
		return ErrInvalid
	}
	*a = CustomOrderAmount(n)
	return nil
}

func (a CustomOrderAmount) MarshalJSON() ([]byte, error) {
	return json.Marshal(json.Number(a))
}

type CustomOrderStore interface {
	CreateCustomOrder(context.Context, Scope, CustomOrderRequest, string) (commerce.Order, error)
}

// ValidateCustomOrder admits only exactly representable positive mills. The
// ledger's truncation policy must not silently change a manager's stated price.
func ValidateCustomOrder(q CustomOrderRequest) (wallet.Amount, int, error) {
	hours := 24
	if q.ExpirationHours != nil {
		hours = *q.ExpirationHours
	}
	if strings.TrimSpace(q.Identifier) == "" || len(q.Identifier) > 1024 || strings.ContainsRune(q.Identifier, '\x00') || strings.TrimSpace(q.Currency) != q.Currency || strings.ContainsAny(q.Currency, "\x00\r\n") || q.Currency == "" || len(q.Currency) > 64 || hours < 1 || hours > 720 || q.Remarks != nil && len(*q.Remarks) > 4096 {
		return wallet.Amount{}, 0, ErrInvalid
	}
	amount, e := wallet.ParseAmount(string(q.Amount))
	if e != nil || amount.Sign() <= 0 {
		return wallet.Amount{}, 0, ErrInvalid
	}
	original, valid := new(big.Rat).SetString(string(q.Amount))
	if !valid {
		return wallet.Amount{}, 0, ErrInvalid
	}
	exact, _ := new(big.Rat).SetString(amount.String())
	if original.Cmp(exact) != 0 {
		return wallet.Amount{}, 0, ErrInvalid
	}
	if len(q.Meta) > 0 {
		raw, e := json.Marshal(q.Meta)
		if e != nil || len(raw) > 16384 {
			return wallet.Amount{}, 0, ErrInvalid
		}
		for _, key := range []string{"subscription_id", "gift_id", "app_subscription", "fulfillment", "required_scopes", "address_contact_id", "address_snapshot"} {
			if _, exists := q.Meta[key]; exists {
				return wallet.Amount{}, 0, ErrInvalid
			}
		}
	}
	return amount, hours, nil
}

func managementError(e error) error {
	switch {
	case errors.Is(e, commerce.ErrInvalid), errors.Is(e, wallet.ErrAmount):
		return ErrInvalid
	case errors.Is(e, commerce.ErrNotFound):
		return ErrNotFound
	case errors.Is(e, commerce.ErrForbidden):
		return ErrForbidden
	case errors.Is(e, commerce.ErrConflict), errors.Is(e, commerce.ErrIdempotency):
		return ErrConflict
	default:
		return e
	}
}

func (s *Service) serveManagement(w http.ResponseWriter, r *http.Request, p []string, q url.Values) bool {
	stats := len(p) == 4 && p[1] == "developers" && p[3] == "stats"
	custom := len(p) == 5 && p[1] == "private" && p[2] == "apps" && p[4] == "orders"
	if !stats && !custom {
		return false
	}
	if stats {
		if r.Method != http.MethodGet {
			method(w, "GET")
			return true
		}
		if len(q) != 0 || len(p[2]) > 256 {
			failure(w, ErrInvalid)
			return true
		}
		repo, ok := s.Repository.(StatisticsRepository)
		if !ok {
			failure(w, ErrUnavailable)
			return true
		}
		out, e := repo.DeveloperStatistics(r.Context(), p[2])
		if e != nil {
			failure(w, e)
		} else {
			respond(w, http.StatusOK, out)
		}
		return true
	}
	if r.Method != http.MethodPost {
		method(w, "POST")
		return true
	}
	actor, e := s.actor(r)
	if e != nil {
		failure(w, e)
		return true
	}
	for key := range q {
		if key != "dev" && key != "proj" {
			failure(w, ErrInvalid)
			return true
		}
	}
	scope := Scope{Actor: actor, Developer: q.Get("dev"), Project: q.Get("proj"), App: p[3]}
	if scope.Developer == "" || len(scope.Developer) > 256 || !authority.ValidID(scope.Project) || !authority.ValidID(scope.App) || len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Get("Idempotency-Key")) > 128 {
		failure(w, ErrInvalid)
		return true
	}
	var req CustomOrderRequest
	if e = decode(w, r, &req); e != nil {
		failure(w, e)
		return true
	}
	if _, _, e = ValidateCustomOrder(req); e != nil {
		failure(w, e)
		return true
	}
	if s.CustomOrders == nil {
		failure(w, ErrUnavailable)
		return true
	}
	out, e := s.CustomOrders.CreateCustomOrder(r.Context(), scope, req, r.Header.Get("Idempotency-Key"))
	if e != nil {
		failure(w, managementError(e))
	} else {
		respond(w, http.StatusOK, out)
	}
	return true
}
