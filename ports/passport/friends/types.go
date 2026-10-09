// SPDX-License-Identifier: AGPL-3.0-only
package friends

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/presence"
)

type Item struct {
	Account    accounts.Account      `json:"account"`
	Status     presence.Status       `json:"status"`
	Activities []activities.Activity `json:"activities"`
}
type Filter struct {
	IncludeOffline, Paged bool
	Offset, Take          int
}
type Repository interface {
	Overview(context.Context, string, Filter) ([]Item, int, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
}
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid     = &Failure{"VALIDATION_ERROR", "Invalid friends overview request.", 400}
	ErrUnavailable = &Failure{"FRIENDS_UNAVAILABLE", "Friends overview is unavailable.", 503}
)

func Validate(f Filter) error {
	if f.Offset < 0 || f.Offset > 100000 || f.Take < 1 || f.Paged && f.Take > 200 || !f.Paged && (f.Offset != 0 || f.Take != 1000) {
		return ErrInvalid
	}
	return nil
}
