// SPDX-License-Identifier: AGPL-3.0-only
package timeline

import (
	"net/http"
	"solsynth.local/go-migration/ports/sphere/posts"
	"strconv"
	"strings"
	"time"
)

// The authored bool request defaults an omitted notify field to false. An
// explicit JSON null remains invalid, as for a non-nullable source bool.
type subscriptionNotify bool

func (v *subscriptionNotify) UnmarshalJSON(raw []byte) error {
	switch strings.TrimSpace(string(raw)) {
	case "true":
		*v = true
	case "false":
		*v = false
	default:
		return ErrInvalid
	}
	return nil
}

func registerSubscriptionState(m *http.ServeMux, base Repository, identity func(http.ResponseWriter, *http.Request, bool, string) (string, bool)) {
	type operation struct {
		pattern    string
		permission bool
		paging     bool
		run        func(SubscriptionStateRepository, http.ResponseWriter, *http.Request, string, int, int)
	}
	operations := []operation{
		{"GET /api/publishers/{name}/subscription/read-status", false, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			v, e := repo.SubscriptionReadStatus(r.Context(), actor, r.PathValue("name"))
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
		}},
		{"PUT /api/publishers/{name}/subscription/read-status", true, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			var body struct {
				LastReadAt *time.Time `json:"last_read_at"`
			}
			if e := decode(w, r, &body, false); e != nil {
				failure(w, e)
				return
			}
			v, e := repo.UpdateSubscriptionReadStatus(r.Context(), actor, r.PathValue("name"), body.LastReadAt)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
		}},
		{"PUT /api/publishers/subscriptions/read-status", true, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			var body struct{}
			if e := decode(w, r, &body, true); e != nil {
				failure(w, e)
				return
			}
			n, e := repo.MarkAllSubscriptionsRead(r.Context(), actor)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, map[string]int{"updated_count": n})
		}},
		{"PATCH /api/publishers/{name}/subscription/me/notify", true, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			var body struct {
				Notify subscriptionNotify `json:"notify"`
			}
			if e := decode(w, r, &body, false); e != nil {
				failure(w, e)
				return
			}
			v, e := repo.UpdateSubscriptionNotify(r.Context(), actor, r.PathValue("name"), bool(body.Notify))
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
		}},
		{"GET /api/publishers/{name}/subscribers", false, true, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, offset, take int) {
			v, total, e := repo.Subscribers(r.Context(), actor, r.PathValue("name"), offset, take)
			if e != nil {
				failure(w, e)
				return
			}
			w.Header().Set("X-Total", strconv.FormatInt(total, 10))
			respond(w, 200, v)
		}},
		{"POST /api/publishers/{name}/subscribers/{accountId}", true, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			var body struct{}
			if e := decode(w, r, &body, true); e != nil {
				failure(w, e)
				return
			}
			v, e := repo.AddSubscriber(r.Context(), actor, r.PathValue("name"), r.PathValue("accountId"))
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
		}},
		{"DELETE /api/publishers/{name}/subscribers/{accountId}", true, false, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, _, _ int) {
			var body struct{}
			if e := decode(w, r, &body, true); e != nil {
				failure(w, e)
				return
			}
			if e := repo.RemoveSubscriber(r.Context(), actor, r.PathValue("name"), r.PathValue("accountId")); e != nil {
				failure(w, e)
				return
			}
			respond(w, 204, nil)
		}},
		{"GET /api/publishers/subscriptions/live", false, true, func(repo SubscriptionStateRepository, w http.ResponseWriter, r *http.Request, actor string, offset, take int) {
			v, e := repo.SubscribedLiveStreams(r.Context(), actor, offset, take)
			if e != nil {
				failure(w, e)
				return
			}
			respond(w, 200, v)
		}},
	}
	for _, op := range operations {
		m.HandleFunc(op.pattern, func(w http.ResponseWriter, r *http.Request) {
			permission := ""
			if op.permission {
				permission = "publishers.subscriptions.manage"
			}
			actor, ok := identity(w, r, true, permission)
			if !ok {
				return
			}
			repo, ok := base.(SubscriptionStateRepository)
			if !ok {
				failure(w, posts.ErrUnavailable)
				return
			}
			allowed := []string{}
			if op.paging {
				allowed = []string{"offset", "take"}
			}
			q, e := query(r, allowed...)
			if e != nil {
				failure(w, e)
				return
			}
			offset, take := 0, 20
			if op.paging {
				offset, e = integer(q, "offset", 0)
				if e == nil {
					take, e = integer(q, "take", 20)
				}
				if e != nil || offset < 0 || offset > 100000 || take < 1 || take > 100 {
					failure(w, ErrInvalid)
					return
				}
			}
			op.run(repo, w, r, actor, offset, take)
		})
	}
}
