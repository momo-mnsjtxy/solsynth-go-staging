// SPDX-License-Identifier: AGPL-3.0-only
package messager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// A synthetic fixture identity is fixed at construction, never trusted from a
// header, cookie, query string, or body. This is not production authentication.
type syncHandler struct {
	service   *SyncService
	accountID string
	synthetic bool
	identity  SyncIdentity
}

func NewSyncHandler(service *SyncService) http.Handler { return syncHandler{service: service} }
func NewSyntheticProjectionSyncHandler(service *SyncService, fixtureAccountID string) (http.Handler, error) {
	if fixtureAccountID != "" && ValidateSyncID(fixtureAccountID) != nil {
		return nil, ErrSyncInvalidRequest
	}
	return syncHandler{service: service, accountID: strings.ToLower(fixtureAccountID), synthetic: true}, nil
}
func syncFailure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{code})
}
func syncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSyncUnauthorized):
		syncFailure(w, 401, "sync_identity_required")
	case errors.Is(err, ErrSyncForbidden):
		syncFailure(w, 403, "sync_membership_required")
	case errors.Is(err, ErrSyncRoomNotFound), errors.Is(err, ErrSyncMessageNotFound):
		syncFailure(w, 404, "sync_not_found")
	case errors.Is(err, ErrSyncInvalidRequest):
		syncFailure(w, 400, "sync_invalid_request")
	default:
		syncFailure(w, 503, "sync_unavailable")
	}
}
func (h syncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.synthetic && h.identity == nil {
		syncFailure(w, 503, "sync_policy_unverified")
		return
	}

	if h.identity != nil {
		account, err := h.identity.Account(r)
		if err != nil {
			syncError(w, err)
			return
		}
		if account != "" && ValidateSyncID(account) != nil {
			syncError(w, ErrSyncUnauthorized)
			return
		}
		h.accountID = account
	}
	if r.URL.Path == "/api/chat/unread" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			syncFailure(w, 405, "sync_method_not_allowed")
			return
		}
		for name := range fixtureHeaders(h.synthetic, r) {
			if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "X-Test-Account") || strings.EqualFold(name, "X-Account-ID") {
				syncFailure(w, 403, "sync_external_identity_unsupported")
				return
			}
		}
		if r.URL.RawQuery != "" {
			syncError(w, ErrSyncInvalidRequest)
			return
		}
		total, err := h.service.UnreadTotal(r.Context(), h.accountID)
		if err != nil {
			syncError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Solsynth-Projection", h.projectionVersion())
		_ = json.NewEncoder(w).Encode(total)
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if (len(parts) != 5 && len(parts) != 6) || parts[0] != "" || parts[1] != "api" || parts[2] != "chat" || ValidateSyncID(parts[3]) != nil || (parts[4] != "sync" && parts[4] != "messages") || len(parts) == 6 && (parts[4] != "messages" || ValidateSyncID(parts[5]) != nil) {
		syncFailure(w, 404, "sync_route_not_found")
		return
	}
	method := http.MethodGet
	if parts[4] == "sync" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		syncFailure(w, 405, "sync_method_not_allowed")
		return
	}
	for name := range fixtureHeaders(h.synthetic, r) {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "X-Test-Account") || strings.EqualFold(name, "X-Account-ID") {
			syncFailure(w, 403, "sync_external_identity_unsupported")
			return
		}
	}
	room := strings.ToLower(parts[3])
	var result any
	var total *int32
	var err error
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		syncError(w, ErrSyncInvalidRequest)
		return
	}
	if parts[4] == "sync" {
		if h.accountID == "" {
			syncError(w, ErrSyncUnauthorized)
			return
		}
		if len(query) != 0 {
			syncError(w, ErrSyncInvalidRequest)
			return
		}
		var request SyncRequest
		request, err = decodeSyncRequest(http.MaxBytesReader(w, r.Body, 1<<20))
		if err == nil {
			var response SyncResponse
			response, err = h.service.Sync(r.Context(), SyncQuery{RoomID: room, AccountID: h.accountID, Request: request})
			result = response
			total = &response.TotalCount
		}
	} else if len(parts) == 6 {
		if len(query) != 0 {
			syncError(w, ErrSyncInvalidRequest)
			return
		}
		result, err = h.service.GetMessage(r.Context(), MessageQuery{RoomID: room, AccountID: h.accountID, MessageID: strings.ToLower(parts[5])})
	} else {
		var take, offset int32
		take, offset, err = messagePagination(query)
		if err == nil {
			var page MessagePage
			page, err = h.service.ListMessages(r.Context(), MessageListQuery{RoomID: room, AccountID: h.accountID, Take: take, Offset: offset})
			result = page.Messages
			total = &page.TotalCount
		}
	}
	if err != nil {
		syncError(w, err)
		return
	}
	// Complete encoding before setting status/total: malformed stored JSON cannot
	// produce a partial successful response or expose database error details.
	data, err := json.Marshal(result)
	if err != nil {
		syncError(w, ErrSyncUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Solsynth-Projection", h.projectionVersion())
	if total != nil {
		w.Header().Set("X-Total", strconv.FormatInt(int64(*total), 10))
	}
	w.WriteHeader(200)
	_, _ = w.Write(append(data, '\n'))
}
func messagePagination(query url.Values) (int32, int32, error) {
	take, offset := int32(20), int32(0)
	for key, values := range query {
		if len(values) != 1 || (key != "take" && key != "offset") {
			return 0, 0, ErrSyncInvalidRequest
		}
		value, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || value < 0 || key == "take" && value > SyncPageLimit {
			return 0, 0, ErrSyncInvalidRequest
		}
		if key == "take" {
			take = int32(value)
		} else {
			offset = int32(value)
		}
	}
	return take, offset, nil
}
func decodeSyncRequest(reader io.Reader) (SyncRequest, error) {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return SyncRequest{}, ErrSyncInvalidRequest
		}
		key, ok := token.(string)
		if !ok {
			return SyncRequest{}, ErrSyncInvalidRequest
		}
		if _, exists := fields[key]; exists {
			return SyncRequest{}, ErrSyncInvalidRequest
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return SyncRequest{}, ErrSyncInvalidRequest
		}
		fields[key] = raw
	}
	if _, err = decoder.Token(); err != nil {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	stamp, ok := fields["last_sync_timestamp"]
	if !ok || bytes.Equal(bytes.TrimSpace(stamp), []byte("null")) {
		return SyncRequest{}, ErrSyncInvalidRequest
	}

	// Nullable selector arrays are supported, but null numeric elements or null
	// ranges are not silently rewritten to zero and then broadened to time mode.
	for _, name := range []string{"missing_sequences", "missing_sequence_ranges"} {
		raw, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return SyncRequest{}, ErrSyncInvalidRequest
		}
		for _, item := range items {
			if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
				return SyncRequest{}, ErrSyncInvalidRequest
			}
			if name == "missing_sequence_ranges" {
				var bounds map[string]json.RawMessage
				if json.Unmarshal(item, &bounds) != nil {
					return SyncRequest{}, ErrSyncInvalidRequest
				}
				for _, bound := range []string{"start_sequence", "end_sequence"} {
					if value, ok := bounds[bound]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
						return SyncRequest{}, ErrSyncInvalidRequest
					}
				}
			}
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request SyncRequest
	if decoder.Decode(&request) != nil {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	if request.LastSyncMessageID != nil && ValidateSyncID(*request.LastSyncMessageID) != nil {
		return SyncRequest{}, ErrSyncInvalidRequest
	}
	return request, nil
}
