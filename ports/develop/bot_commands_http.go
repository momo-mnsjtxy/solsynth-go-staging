// SPDX-License-Identifier: AGPL-3.0-only
package develop

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type botCommandsHandler struct {
	service   *BotCommandsService
	candidate bool
}

func NewBotCommandsHandler(service *BotCommandsService) http.Handler {
	return botCommandsHandler{service: service}
}
func NewSyntheticCompatibilityBotCommandsHandler(service *BotCommandsService) http.Handler {
	return botCommandsHandler{service: service, candidate: true}
}

func (h botCommandsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{code})
	}
	if !h.candidate {
		fail(503, "bot_commands_policy_unverified")
		return
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "api" || parts[2] != "bots" || parts[4] != "commands" || ValidateBotCommandID(parts[3]) != nil {
		fail(404, "bot_commands_route_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(405, "bot_commands_method_not_allowed")
		return
	}
	for key := range r.Header {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "X-Test-Account") {
			fail(403, "bot_commands_identity_unsupported")
			return
		}
	}
	commands, err := h.service.Read(r.Context(), parts[3])
	if errors.Is(err, ErrBotNotFound) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Status  int    `json:"status"`
		}{"DEV_BOT_ACCOUNT_NOT_FOUND", "Bot not found", 404})
		return
	}
	if err != nil {
		fail(503, "bot_commands_unavailable")
		return
	}
	data, err := json.Marshal(commands)
	if err != nil {
		fail(503, "bot_commands_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}
