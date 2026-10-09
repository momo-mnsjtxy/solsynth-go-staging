// SPDX-License-Identifier: AGPL-3.0-only
package passkit

import (
	"context"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *Failure) Error() string { return e.Code }

var (
	ErrInvalid      = &Failure{"PASSKIT_INVALID", "Invalid pass request.", 400}
	ErrUnauthorized = &Failure{"PASSKIT_UNAUTHORIZED", "Pass authorization is required.", 401}
	ErrUnavailable  = &Failure{"PASSKIT_UNCONFIGURED", "Apple Wallet signing is not configured or available.", 503}
)

func Normalize(e error) error {
	var p *Failure
	var a *authority.Failure
	if errors.As(e, &p) || errors.As(e, &a) {
		return e
	}
	return ErrUnavailable
}

type Bundle struct {
	Data      []byte
	Tag       string
	UpdatedAt time.Time
}
type Serials struct {
	SerialNumbers []string `json:"serialNumbers"`
	LastUpdated   string   `json:"lastUpdated"`
}
type Repository interface {
	Member(context.Context, string) (Bundle, error)
	Latest(context.Context, string, string, string) (Bundle, error)
	Register(context.Context, string, string, string, string, string) (bool, error)
	Unregister(context.Context, string, string, string, string) error
	Updates(context.Context, string, string, string, string) (Serials, error)
}
type Authority interface {
	AuthenticateRequest(*http.Request) (authority.Principal, error)
	AuthorizePrincipal(context.Context, authority.Principal, ...string) (authority.Principal, error)
}

// Provisioning exposes an enduring pass credential, including when retrieving
// an existing pass. Call only with a currently revalidated canonical principal.
// Delegated or app sessions cannot extend their lifetime through a pass token.
const ProvisionPermission = "accounts.manage"

func RequireProvisioningPrincipal(p authority.Principal) error {
	if !authority.ValidID(p.Account.ID) || !authority.ValidID(p.Session.ID) || p.Session.AccountID != p.Account.ID || p.Session.Type != 0 || p.Session.ParentID != "" || p.Session.ClientID != "" {
		return authority.ErrForbidden
	}
	return nil
}

type Member struct {
	ID, Name, Nick, FirstName, MiddleName, LastName, Program string
	CreatedAt                                                time.Time
	Picture                                                  []byte
}
type ManifestSigner interface {
	Identity() (passType, team string)
	Fingerprint() string
	SignManifest(context.Context, []byte, time.Time) ([]byte, error)
}
