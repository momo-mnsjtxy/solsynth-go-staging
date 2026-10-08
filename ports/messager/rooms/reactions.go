// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/messager"
	"strconv"
	"time"
)

type ReactionRequest struct {
	Symbol   string `json:"symbol"`
	Attitude int32  `json:"attitude"`
}
type Reaction struct {
	ID        string     `json:"id"`
	MessageID string     `json:"message_id"`
	SenderID  string     `json:"sender_id"`
	Symbol    string     `json:"symbol"`
	Attitude  int32      `json:"attitude"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
	Sender    *Member    `json:"sender,omitempty"`
}
type ReactionQuery struct {
	Symbol       string
	Order        string
	Take, Offset int
}
type ReactionRepository interface {
	React(context.Context, string, string, string, ReactionRequest) (*Reaction, error)
	RemoveReaction(context.Context, string, string, string, string) error
	ListReactions(context.Context, string, string, string, ReactionQuery) ([]Reaction, int, error)
}

func ValidReaction(symbol string) bool {
	switch symbol {
	case "thumb_up", "thumb_down", "cry", "confuse", "hello", "shock", "speechless", "salute", "ridicule", "clap", "laugh", "angry", "party", "pray", "heart":
		return true
	}
	return false
}
func (s *Service) serveReactions(w http.ResponseWriter, r *http.Request, p []string) {
	if s == nil || s.Repository == nil {
		failure(w, ErrUnavailable)
		return
	}
	repo, ok := s.Repository.(ReactionRepository)
	if !ok {
		failure(w, ErrUnsupported)
		return
	}
	if (len(p) != 4 && len(p) != 5) || !ValidID(p[0]) || !ValidID(p[2]) {
		failure(w, ErrInvalid)
		return
	}
	if r.Method == "GET" && len(p) == 4 {
		actor, e := (readIdentity{s}).Account(r)
		if e != nil {
			if e == messager.ErrSyncUnauthorized {
				send(w, 401, map[string]string{"code": "CHAT_UNAUTHORIZED"})
			} else if e == messager.ErrSyncForbidden {
				failure(w, ErrForbidden)
			} else {
				failure(w, ErrUnavailable)
			}
			return
		}
		q := ReactionQuery{Take: 20}
		for k, v := range r.URL.Query() {
			if len(v) != 1 {
				failure(w, ErrInvalid)
				return
			}
			switch k {
			case "symbol":
				q.Symbol = v[0]
			case "order":
				q.Order = v[0]
			case "take", "offset":
				n, x := strconv.Atoi(v[0])
				if x != nil {
					failure(w, ErrInvalid)
					return
				}
				if k == "take" {
					q.Take = n
				} else {
					q.Offset = n
				}
			default:
				failure(w, ErrInvalid)
				return
			}
		}
		items, total, e := repo.ListReactions(r.Context(), actor, p[0], p[2], q)
		if e != nil {
			failure(w, e)
			return
		}
		w.Header().Set("X-Total", strconv.Itoa(total))
		send(w, 200, items)
		return
	}
	if r.URL.RawQuery != "" {
		failure(w, ErrInvalid)
		return
	}
	principal, e := s.principal(r, "chat.messages.react")
	if e != nil {
		failure(w, e)
		return
	}
	ctx := r.Context()
	if s.Bind != nil {
		ctx = s.Bind(ctx, principal)
	}
	if r.Method == "POST" && len(p) == 4 {
		var req ReactionRequest
		e = decode(w, r, &req, "symbol", "attitude")
		var result *Reaction
		if e == nil {
			result, e = repo.React(ctx, principal.Account.ID, p[0], p[2], req)
		}
		if e != nil {
			failure(w, e)
			return
		}
		if result == nil {
			send(w, 204, nil)
		} else {
			send(w, 200, result)
		}
		return
	}
	if r.Method == "DELETE" && len(p) == 5 {
		e = empty(w, r)
		if e == nil {
			e = repo.RemoveReaction(ctx, principal.Account.ID, p[0], p[2], p[4])
		}
		if e != nil {
			failure(w, e)
			return
		}
		send(w, 204, nil)
		return
	}
	method(w, "GET, POST, DELETE")
}
