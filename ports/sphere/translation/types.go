// SPDX-License-Identifier: AGPL-3.0-only
package translation

import (
	"context"
	"database/sql"
	"golang.org/x/text/language"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid         = &Failure{"TRANSLATION_INVALID", "A bounded text and valid language codes are required.", 400}
	ErrUnauthenticated = &Failure{"UNAUTHORIZED", "Authentication is required.", 401}
	ErrEntitlement     = &Failure{"TRANSLATION_ENTITLEMENT_REQUIRED", "An eligible current subscription is required.", 403}
	ErrUnavailable     = &Failure{"TRANSLATION_UNAVAILABLE", "Translation is not configured or unavailable.", 503}
	ErrProvider        = &Failure{"TRANSLATION_PROVIDER_FAILED", "The translation provider could not complete the request.", 502}
	ErrBusy            = &Failure{"TRANSLATION_BUSY", "Translation capacity is currently exhausted.", 429}
)

type Request struct{ Text, Target, Source string }

func (r Request) Normalize() (Request, error) {
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || utf8.RuneCountInString(r.Text) > 2000 || len(r.Text) > 8000 {
		return Request{}, ErrInvalid
	}
	for _, p := range []*string{&r.Target, &r.Source} {
		if p == &r.Source && (*p == "" || *p == "auto") {
			*p = "auto"
			continue
		}
		if len(*p) > 32 || *p == "" || strings.TrimSpace(*p) != *p || strings.ContainsAny(*p, "_ \t\r\n") {
			return Request{}, ErrInvalid
		}
		tag, e := language.Parse(*p)
		if e != nil || tag == language.Und {
			return Request{}, ErrInvalid
		}
		*p = tag.String()
	}
	return r, nil
}

type Provider interface {
	Namespace() string
	Translate(context.Context, Request) (string, error)
}

// Entitlement is an owned policy seam. It does not claim that an upstream
// account's excluded PerkLevel model has been reconstructed.
type Entitlement interface {
	Require(context.Context, *sql.Tx, string, time.Time) error
}
type Repository interface {
	Translate(context.Context, string, Request) (string, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
