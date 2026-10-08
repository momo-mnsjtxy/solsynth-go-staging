// SPDX-License-Identifier: AGPL-3.0-only
// HTTP contracts derive from the owned Passport/Develop controllers and Solian.
package board

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf16"
)

const (
	SelfManage      = "accounts.profile.board.manage"
	AccountsView    = "accounts.view"
	BoardManage     = "accounts.board.manage"
	BoardCapability = "accounts.profile.board"
)

var (
	ErrUnauthenticated = errors.New("Authentication is required.")
	ErrForbidden       = errors.New("Permission denied.")
	ErrAccountNotFound = errors.New("Account not found.")
	ErrAppNotFound     = errors.New("App not found")
)

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Principal struct {
	Name      string
	AccountID string
	AppID     string
}

// Authority is mandatory. No JWT/secret/permission success is guessed here.
type Authority interface {
	Authenticate(*http.Request) (Principal, error)
	Require(context.Context, Principal, string) error
	PublicRead(context.Context) error
	AuthorizeApp(context.Context, Principal, string, string) error
}
type AccountResolver interface {
	Resolve(context.Context, string) (Account, error)
}
type AccountPurger interface {
	PurgeAccount(context.Context, Account) error
}
type Deleter interface {
	Delete(context.Context, string, string) error
}
type ManifestCatalog interface {
	Catalog
	Manifests(context.Context, string) ([]Manifest, error)
}
type HTTPOptions struct {
	Service      Service
	Authority    Authority
	Accounts     AccountResolver
	AccountCache AccountPurger
	Delete       Deleter
	// BodyLimit is a local safety bound, not claimed as the legacy host limit.
	BodyLimit int64
}
type httpHandler struct {
	HTTPOptions
	mux *http.ServeMux
}

func NewHTTPHandler(options HTTPOptions) http.Handler {
	h := &httpHandler{HTTPOptions: options, mux: http.NewServeMux()}
	if h.BodyLimit <= 0 {
		h.BodyLimit = 1 << 20
	}
	h.mux.HandleFunc("GET /api/accounts/me/board", h.self)
	h.mux.HandleFunc("PUT /api/accounts/me/board", h.self)
	h.mux.HandleFunc("GET /api/accounts/{name}/board", h.public)
	h.mux.HandleFunc("GET /api/admin/accounts/{identifier}/board", h.admin)
	h.mux.HandleFunc("PUT /api/admin/accounts/{identifier}/board", h.admin)
	h.mux.HandleFunc("POST /api/admin/accounts/{identifier}/board/items/{item}/payload", h.admin)
	h.mux.HandleFunc("DELETE /api/admin/accounts/{identifier}/board/items/{item}", h.admin)
	h.mux.HandleFunc("POST /api/private/apps/{app}/board/payload", h.app)
	h.mux.HandleFunc("GET /api/private/apps/{app}/board/widgets", h.app)
	return h
}

// Go's GET patterns also match HEAD, but the retained controllers declare only
// HttpGet. Reject that implicit method before authority/cache/repository work;
// the two board-layout paths additionally advertise their explicit PUT method.
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		_, pattern := h.mux.Handler(r)
		if strings.HasPrefix(pattern, "GET ") {
			allow := http.MethodGet
			probe := r.Clone(r.Context())
			probe.Method = http.MethodPut
			if _, p := h.mux.Handler(probe); strings.HasPrefix(p, "PUT ") {
				allow += ", " + http.MethodPut
			}
			w.Header().Set("Allow", allow)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Preserve ordinary 404/405 routing on other paths, without a HEAD body.
		h.mux.ServeHTTP(headResponseWriter{w}, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

type headResponseWriter struct{ http.ResponseWriter }

func (w headResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (h *httpHandler) principal(r *http.Request, permission string) (Principal, error) {
	if h.Authority == nil {
		return Principal{}, dependency("identity and permission authority")
	}
	p, err := h.Authority.Authenticate(r)
	if err != nil {
		return Principal{}, err
	}
	if err = h.Authority.Require(r.Context(), p, permission); err != nil {
		return Principal{}, err
	}
	return p, nil
}
func (h *httpHandler) resolve(ctx context.Context, id string) (Account, error) {
	if h.Accounts == nil {
		return Account{}, dependency("account resolver")
	}
	return h.Accounts.Resolve(ctx, id)
}
func (h *httpHandler) self(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r, SelfManage)
	if err != nil {
		respondError(w, err, "")
		return
	}
	if p.AccountID == "" {
		respondError(w, ErrUnauthenticated, "")
		return
	}
	if r.Method == http.MethodGet {
		h.list(w, r, p.AccountID)
		return
	}
	if h.AccountCache == nil {
		respondError(w, dependency("account cache purge"), "")
		return
	}
	input, err := h.layout(w, r)
	if err != nil {
		respondError(w, err, "PASSPORT_BOARD_REPLACE_FAILED")
		return
	}
	result, err := h.Service.Replace(r.Context(), p.AccountID, input)
	if err == nil {
		err = h.AccountCache.PurgeAccount(r.Context(), Account{ID: p.AccountID})
	}
	if err != nil {
		respondError(w, err, "PASSPORT_BOARD_REPLACE_FAILED")
		return
	}
	respondJSON(w, 200, result)
}
func (h *httpHandler) public(w http.ResponseWriter, r *http.Request) {
	if h.Authority == nil {
		respondError(w, dependency("public-route authority"), "")
		return
	}
	if err := h.Authority.PublicRead(r.Context()); err != nil {
		respondError(w, err, "")
		return
	}
	account, err := h.resolve(r.Context(), r.PathValue("name"))
	if err != nil {
		respondError(w, err, "")
		return
	}
	h.list(w, r, account.ID)
}
func (h *httpHandler) list(w http.ResponseWriter, r *http.Request, account string) {
	items, err := h.Service.List(r.Context(), account)
	if err != nil {
		respondError(w, err, "")
		return
	}
	respondJSON(w, 200, items)
}
func (h *httpHandler) admin(w http.ResponseWriter, r *http.Request) {
	item := r.PathValue("item")
	if item != "" {
		var ok bool
		item, ok = CanonicalUUID(item)
		if !ok {
			http.NotFound(w, r)
			return
		}
	}
	permission := BoardManage
	if r.Method == http.MethodGet {
		permission = AccountsView
	}
	if _, err := h.principal(r, permission); err != nil {
		respondError(w, err, "")
		return
	}
	account, err := h.resolve(r.Context(), r.PathValue("identifier"))
	if err != nil {
		respondError(w, err, "")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.list(w, r, account.ID)
	case http.MethodPut:
		input, err := h.layout(w, r)
		if err != nil {
			respondError(w, err, "PASSPORT_BOARD_REPLACE_FAILED")
			return
		}
		out, err := h.Service.Replace(r.Context(), account.ID, input)
		if err != nil {
			respondError(w, err, "PASSPORT_BOARD_REPLACE_FAILED")
			return
		}
		respondJSON(w, 200, out)
	case http.MethodPost:
		var input struct {
			Payload Payload `json:"payload"`
		}
		if err := h.decode(w, r, &input); err != nil {
			respondError(w, err, "PASSPORT_BOARD_UPDATE_FAILED")
			return
		}
		out, err := h.Service.UpdateAdminPayload(r.Context(), account.ID, item, input.Payload)
		if err != nil {
			respondError(w, err, "PASSPORT_BOARD_UPDATE_FAILED")
			return
		}
		respondJSON(w, 200, out)
	case http.MethodDelete:
		if h.Delete == nil {
			respondError(w, dependency("board deletion"), "")
			return
		}
		if err := h.Delete.Delete(r.Context(), account.ID, item); err != nil {
			respondError(w, err, "")
			return
		}
		// The owned admin controller does not purge account cache on delete.
		w.WriteHeader(http.StatusNoContent)
	}
}
func (h *httpHandler) app(w http.ResponseWriter, r *http.Request) {
	appID, ok := CanonicalUUID(r.PathValue("app"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if h.Authority == nil {
		respondError(w, dependency("app authority"), "")
		return
	}
	principal, err := h.Authority.Authenticate(r)
	if err != nil {
		respondError(w, err, "")
		return
	}
	if err = h.Authority.AuthorizeApp(r.Context(), principal, appID, ""); err != nil {
		respondError(w, err, "")
		return
	}
	if r.Method == http.MethodGet {
		catalog, ok := h.Service.Catalog.(ManifestCatalog)
		if !ok {
			respondError(w, dependency("widget manifest catalog"), "")
			return
		}
		out, err := catalog.Manifests(r.Context(), appID)
		if err != nil {
			respondError(w, err, "")
			return
		}
		respondJSON(w, 200, out)
		return
	}
	var input struct {
		AccountID string  `json:"account_id"`
		ItemID    *string `json:"board_item_id"`
		WidgetKey string  `json:"widget_key"`
		Payload   Payload `json:"payload"`
	}
	if err := h.decode(w, r, &input); err != nil {
		respondError(w, err, "DEV_APP_BOARD_PAYLOAD_INVALID")
		return
	}
	account, ok := CanonicalUUID(input.AccountID)
	if !ok {
		respondError(w, invalid("account_id is required."), "DEV_APP_BOARD_PAYLOAD_ACCOUNT_ID_REQUIRED")
		return
	}
	if err = h.Authority.AuthorizeApp(r.Context(), principal, appID, account); err != nil {
		respondError(w, err, "")
		return
	}
	if strings.TrimSpace(input.WidgetKey) == "" || len(utf16.Encode([]rune(input.WidgetKey))) > 128 {
		respondError(w, invalid("widget_key is required and must not exceed 128 characters."), "DEV_APP_BOARD_PAYLOAD_INVALID")
		return
	}
	item := ""
	if input.ItemID != nil && strings.TrimSpace(*input.ItemID) != "" {
		item, ok = CanonicalUUID(*input.ItemID)
		if !ok {
			respondError(w, invalid("Invalid board item id."), "DEV_APP_BOARD_PAYLOAD_RPC_ERROR")
			return
		}
	}
	if h.Service.Catalog == nil {
		respondError(w, dependency("catalog"), "")
		return
	}
	payload, err := h.Service.Catalog.Validate(r.Context(), appID, input.WidgetKey, input.Payload)
	if err != nil {
		respondError(w, err, "DEV_APP_BOARD_PAYLOAD_INVALID")
		return
	}
	widget, err := h.Service.Catalog.Lookup(r.Context(), appID, input.WidgetKey)
	if err != nil {
		respondError(w, err, "DEV_APP_BOARD_PAYLOAD_INVALID")
		return
	}
	if widget == nil {
		respondError(w, invalid("Board widget is not configured for this app."), "DEV_APP_BOARD_PAYLOAD_INVALID")
		return
	}
	out, err := h.Service.UpdateAppPayload(r.Context(), account, item, appID, widget.Key, payload)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respondJSON(w, 404, apiError{Code: "DEV_APP_NOT_FOUND", Message: err.Error(), Status: 404})
			return
		}
		respondError(w, err, "DEV_APP_BOARD_PAYLOAD_RPC_ERROR")
		return
	}
	respondJSON(w, 200, out)
}
func (h *httpHandler) decode(w http.ResponseWriter, r *http.Request, out any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return invalid("Content-Type must be application/json.")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, h.BodyLimit))
	d.UseNumber()
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return invalid("Invalid JSON request body.")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalid("A JSON request body is required.")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return invalid("Expected a single JSON value within the request size limit.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return invalid("Invalid JSON request body.")
	}
	return nil
}
func (h *httpHandler) layout(w http.ResponseWriter, r *http.Request) ([]Item, error) {
	var raw []json.RawMessage
	if err := h.decode(w, r, &raw); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(raw))
	for _, data := range raw {
		var input struct {
			ID      *string `json:"id"`
			Order   int32   `json:"order"`
			Kind    int32   `json:"kind"`
			Widget  *string `json:"widget_key"`
			App     *string `json:"custom_app_id"`
			Key     *string `json:"custom_app_widget_key"`
			Enabled bool    `json:"is_enabled"`
			Payload Payload `json:"payload"`
		}
		input.Enabled = true
		var props map[string]json.RawMessage
		if json.Unmarshal(data, &props) != nil || props == nil {
			return nil, invalid("Board items must be objects.")
		}
		for actual, value := range props {
			for _, key := range []string{"order", "kind", "is_enabled"} {
				if strings.EqualFold(actual, key) && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return nil, invalid(key + " must not be null.")
				}
			}
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if err := d.Decode(&input); err != nil {
			return nil, invalid("Invalid board item.")
		}
		item := Item{Order: input.Order, Kind: Kind(input.Kind), WidgetKey: input.Widget, CustomAppID: input.App, CustomAppWidgetKey: input.Key, IsEnabled: input.Enabled, Payload: input.Payload}
		if item.Payload == nil {
			item.Payload = Payload{}
		}
		for _, key := range []*string{item.WidgetKey, item.CustomAppWidgetKey} {
			if key != nil && len(utf16.Encode([]rune(*key))) > 256 {
				return nil, invalid("Board widget keys must not exceed 256 characters.")
			}
		}
		if input.ID != nil {
			var ok bool
			item.ID, ok = CanonicalUUID(*input.ID)
			if !ok {
				return nil, invalid("Invalid board item id.")
			}
		}
		if item.CustomAppID != nil {
			app, ok := CanonicalUUID(*item.CustomAppID)
			if !ok {
				return nil, invalid("Invalid custom app id.")
			}
			item.CustomAppID = &app
		}
		out = append(out, item)
	}
	return out, nil
}

// CanonicalUUID accepts the ordinary GUID textual forms used by .NET binders,
// without importing or depending on any excluded identity implementation.
func CanonicalUUID(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if len(v) == 38 && ((v[0] == '{' && v[37] == '}') || (v[0] == '(' && v[37] == ')')) {
		v = v[1:37]
	}
	if len(v) == 32 {
		v = v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
	}
	if len(v) != 36 {
		return "", false
	}
	v = strings.ToLower(v)
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return v, true
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func respondError(w http.ResponseWriter, err error, validationCode string) {
	status, code, message := 500, "INTERNAL_ERROR", "Board operation failed."
	switch {
	case errors.Is(err, ErrUnauthenticated):
		status, code, message = 401, "UNAUTHORIZED", err.Error()
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "FORBIDDEN", err.Error()
	case errors.Is(err, ErrAccountNotFound):
		status, code, message = 404, "PASSPORT_ACCOUNT_NOT_FOUND", err.Error()
	case errors.Is(err, ErrAppNotFound):
		status, code, message = 404, "DEV_APP_NOT_FOUND", err.Error()
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "PASSPORT_BOARD_ITEM_NOT_FOUND", err.Error()
	case errors.Is(err, ErrDependency):
		status, code, message = 503, "BOARD_DEPENDENCY_UNAVAILABLE", "Required board authority or adapter is unavailable."
	case errors.Is(err, ErrInvalidOperation):
		status, code, message = 400, validationCode, err.Error()
		if code == "" {
			code = "INVALID_REQUEST"
		}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status, code, message = 503, "BOARD_OPERATION_UNAVAILABLE", "Board operation was interrupted."
	}
	respondJSON(w, status, apiError{Code: code, Message: message, Status: status})
}
func respondJSON(w http.ResponseWriter, status int, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "JSON serialization failed.", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
