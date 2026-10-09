// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Authenticator must derive principals from verified authority state. Header
// names identifying users, unsigned claims and fixture account headers are not
// authentication. The handler never interprets them.
type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}
type Handler struct {
	Service        *Service
	Authentication Authenticator
}

func NewHandler(service *Service, authentication Authenticator) http.Handler {
	return &Handler{service, authentication}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, e error) {
	status, code, message := 500, "WALLET_INTERNAL_ERROR", "Wallet operation failed."
	switch {
	case errors.Is(e, ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", ErrUnauthorized.Error()
	case errors.Is(e, ErrForbidden):
		status, code, message = 403, "FORBIDDEN", ErrForbidden.Error()
	case errors.Is(e, ErrPIN):
		status, code, message = 403, "WALLET_PIN_REQUIRED", ErrPIN.Error()
	case errors.Is(e, ErrUnavailable):
		status, code, message = 503, "WALLET_ADAPTER_UNAVAILABLE", ErrUnavailable.Error()
	case errors.Is(e, ErrNotFound):
		status, code, message = 404, "WALLET_NOT_FOUND", ErrNotFound.Error()
	case errors.Is(e, ErrConflict), errors.Is(e, ErrIdempotency):
		status, code, message = 409, "WALLET_CONFLICT", e.Error()
	case errors.Is(e, ErrInvalid), errors.Is(e, ErrAmount), errors.Is(e, ErrInsufficient), errors.Is(e, ErrQuota):
		status, code, message = 400, "WALLET_INVALID_REQUEST", e.Error()
	}
	writeJSON(w, status, apiError{code, message, status})
}
func decode(w http.ResponseWriter, r *http.Request, out any, optional bool) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if e := d.Decode(out); e != nil {
		if optional && e == io.EOF {
			return nil
		}
		return ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	pinStatus := path == "/api/accounts/me/pin-status" || path == "/stargate/accounts/me/pin-status"
	if !pinStatus && path != "/api/wallets" && !strings.HasPrefix(path, "/api/wallets/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(path, "/api/wallets/transfer/requests/") {
		id := strings.TrimPrefix(path, "/api/wallets/transfer/requests/")
		if !ValidID(id) {
			fail(w, ErrNotFound)
			return
		}
		if h.Service == nil {
			fail(w, ErrUnavailable)
			return
		}
		repo, ok := h.Service.Store.(TransferRequestRepository)
		if !ok {
			fail(w, ErrUnavailable)
			return
		}
		out, e := repo.PublicTransferRequest(r.Context(), id)
		if e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	if h.publicFund(w, r, path) {
		return
	}
	if h.Authentication == nil {
		fail(w, ErrUnavailable)
		return
	}
	p, e := h.Authentication.Authenticate(r)
	if e != nil {
		fail(w, e)
		return
	}
	if h.Service == nil {
		fail(w, ErrUnavailable)
		return
	}
	if e = h.Service.ready(p); e != nil {
		fail(w, e)
		return
	}
	ctx := r.Context()
	if pinStatus {
		if r.Method != "GET" {
			http.NotFound(w, r)
			return
		}
		reader, ok := h.Service.Payments.(PINStatusReader)
		if !ok {
			fail(w, ErrUnavailable)
			return
		}
		status, e := reader.Status(ctx, p)
		if e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, status)
		return
	}
	suffix := strings.TrimPrefix(path, "/api/wallets")
	if h.reportingHTTP(w, r, p, suffix) {
		return
	}
	if h.fundHTTP(w, r, p, suffix) {
		return
	}
	var value any
	switch {
	case r.Method == "GET" && suffix == "":
		value, e = h.Service.Get(ctx, p, "")
	case r.Method == "GET" && suffix == "/all":
		value, e = h.Service.List(ctx, p)
	case r.Method == "POST" && suffix == "":
		var request CreateRequest
		if e = decode(w, r, &request, true); e == nil {
			value, e = h.Service.Create(ctx, p, request)
		}
	case r.Method == "POST" && suffix == "/transfer/requests":
		request := CreateTransferRequest{ExpirationHours: 24}
		if e = decode(w, r, &request, false); e == nil {
			value, e = h.Service.CreateTransferRequest(ctx, p, request)
		}
	case r.Method == "POST" && suffix == "/exchange":
		var request ExchangeRequest
		if len(r.Header.Values("Idempotency-Key")) != 1 {
			e = ErrInvalid
			break
		}
		if e = decode(w, r, &request, false); e == nil {
			value, e = h.Service.Exchange(ctx, p, request, r.Header.Get("Idempotency-Key"))
		}
	case r.Method == "POST" && suffix == "/transfer":
		var request TransferRequest
		if len(r.Header.Values("Idempotency-Key")) > 1 {
			e = ErrInvalid
			break
		}
		if e = decode(w, r, &request, false); e == nil {
			value, e = h.Service.Transfer(ctx, p, request, r.Header.Get("Idempotency-Key"))
		}
	case r.Method == "GET" && (suffix == "/transactions" || suffix == "/transactions/pending"):
		q := TransactionQuery{WalletID: r.URL.Query().Get("wallet"), Direction: r.URL.Query().Get("direction"), Take: 20, PendingOnly: suffix == "/transactions/pending"}
		for key, target := range map[string]*int{"offset": &q.Offset, "take": &q.Take} {
			if raw, ok := r.URL.Query()[key]; ok {
				if len(raw) != 1 {
					e = ErrInvalid
					break
				}
				*target, e = strconv.Atoi(raw[0])
				if e != nil {
					e = ErrInvalid
					break
				}
			}
		}
		if e != nil {
			break
		}
		if raw := r.URL.Query().Get("type"); raw != "" {
			n, x := strconv.Atoi(raw)
			if x != nil {
				switch strings.ToLower(raw) {
				case "transfer":
					n = 0
				default:
					e = ErrInvalid
				}
			}
			q.Type = &n
		}
		if e != nil {
			break
		}
		var page TransactionPage
		page, e = h.Service.Transactions(ctx, p, q)
		if e == nil {
			w.Header().Set("X-Total", strconv.FormatInt(page.Total, 10))
			value = page.Items
		}
	default:
		parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
		switch {
		case r.Method == "GET" && len(parts) == 1 && ValidID(parts[0]):
			value, e = h.Service.Get(ctx, p, parts[0])
		case r.Method == "POST" && len(parts) == 2 && ValidID(parts[0]) && parts[1] == "default":
			value, e = h.Service.SetDefault(ctx, p, parts[0])
		case r.Method == "POST" && len(parts) == 3 && ValidID(parts[0]) && parts[1] == "public-id" && (parts[2] == "enable" || parts[2] == "disable"):
			value, e = h.Service.PublicID(ctx, p, parts[0], parts[2] == "enable")
		case r.Method == "GET" && len(parts) == 2 && parts[0] == "transactions" && ValidID(parts[1]):
			value, e = h.Service.GetTransaction(ctx, p, parts[1])
		case r.Method == "POST" && len(parts) == 3 && parts[0] == "transactions" && ValidID(parts[1]) && (parts[2] == "confirm" || parts[2] == "reject"):
			value, e = h.Service.Transition(ctx, p, parts[1], Transition(parts[2]))
		default:
			http.NotFound(w, r)
			return
		}
	}
	if e != nil {
		fail(w, e)
		return
	}
	writeJSON(w, 200, value)
}
