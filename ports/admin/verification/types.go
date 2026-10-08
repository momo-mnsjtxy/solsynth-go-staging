// SPDX-License-Identifier: AGPL-3.0-only
package verification

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/passport/accounts"
)

type Kind string

const (
	Account   Kind = "account"
	Publisher Kind = "publisher"
	Realm     Kind = "realm"
)

func (k Kind) Permission() string {
	switch k {
	case Account:
		return "accounts.manage"
	case Publisher:
		return "publishers.moderate"
	case Realm:
		return "realms.moderate"
	}
	return ""
}

type Repository interface {
	SetVerification(context.Context, string, Kind, string, *accounts.VerificationMark) (any, error)
}
type Auth interface {
	Authenticate(*http.Request) (string, error)
}
