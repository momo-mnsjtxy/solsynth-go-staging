// SPDX-License-Identifier: AGPL-3.0-only
// Derived from the pinned PostWatchController.cs; transport differences documented.
package sphere

import (
	"encoding/json"
	"io"
	"net/http"
)

// WatchAuth is an explicit trusted adapter to the real authentication and
// PostSubscriptionsManage permission provider. Never derive identity from an
// unverified header, query argument or submitted JSON account identifier.
type WatchAuth interface {
	AccountID(*http.Request) (string, error)
	CanManagePostSubscriptions(*http.Request, string) error
}

type watchFilters struct {
	Reactions *bool `json:"reactions"`
	Replies   *bool `json:"replies"`
	Chains    *bool `json:"chains"`
	Forwards  *bool `json:"forwards"`
	Edits     *bool `json:"edits"`
}

type watchRequest struct {
	Bookmark *watchFilters `json:"bookmark"`
	Reaction *watchFilters `json:"reaction"`
	Reply    *watchFilters `json:"reply"`
}

func watchJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func watchError(w http.ResponseWriter, status int, code, message string) {
	watchJSON(w, status, map[string]any{"code": code, "message": message, "status": status})
}

// NewWatchHandler serves only GET and PUT /api/posts/watch. Missing adapters
// fail closed with 503. A 1MiB request limit is a local transport safeguard, not
// an asserted upstream limit. GET/PUT body and success shape match the controller.
func NewWatchHandler(service *WatchService, auth WatchAuth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/posts/watch" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			w.Header().Set("Allow", "GET, PUT")
			watchError(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed.")
			return
		}
		if service == nil || service.Repository == nil || auth == nil {
			watchError(w, 503, "SERVICE_UNAVAILABLE", "Watch service dependencies are unavailable.")
			return
		}
		accountID, err := auth.AccountID(r)
		if err != nil || accountID == "" {
			watchError(w, 401, "UNAUTHORIZED", "Authentication is required.")
			return
		}
		if r.Method == http.MethodGet {
			preferences, err := service.GetEffectivePreferences(r.Context(), accountID)
			if err != nil {
				watchError(w, 500, "INTERNAL_ERROR", "Unable to load watch preferences.")
				return
			}
			watchJSON(w, 200, preferences)
			return
		}
		if err := auth.CanManagePostSubscriptions(r, accountID); err != nil {
			watchError(w, 403, "FORBIDDEN", "Post subscription management permission is required.")
			return
		}
		var request *watchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&request); err != nil || request == nil {
			watchError(w, 400, "INVALID_REQUEST", "Invalid watch preferences request.")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			watchError(w, 400, "INVALID_REQUEST", "Invalid watch preferences request.")
			return
		}
		updates := []WatchPreferenceUpdate{}
		for i, filters := range []*watchFilters{request.Bookmark, request.Reaction, request.Reply} {
			if filters == nil {
				continue
			}
			updates = append(updates, WatchPreferenceUpdate{Source: watchSources[i], NotifyReactions: filters.Reactions, NotifyReplies: filters.Replies, NotifyChains: filters.Chains, NotifyForwards: filters.Forwards, NotifyEdits: filters.Edits})
		}
		preferences, err := service.UpdatePreferences(r.Context(), accountID, updates)
		if err != nil {
			watchError(w, 500, "INTERNAL_ERROR", "Unable to update watch preferences.")
			return
		}
		watchJSON(w, 200, preferences)
	})
}
