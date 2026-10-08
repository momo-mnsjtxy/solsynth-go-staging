// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"net/http"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type commerceIdentity struct{ identity }

func (i commerceIdentity) Authenticate(r *http.Request) (wallet.Principal, error) {
	p, e := i.require(r.Context())
	if e != nil {
		return wallet.Principal{}, e
	}
	return wallet.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}, nil
}
func (i commerceIdentity) Require(ctx context.Context, p wallet.Principal, key string) error {
	fresh, e := i.require(ctx, key)
	if e != nil {
		return e
	}
	if p.AccountID != fresh.Account.ID || p.SessionID != fresh.Session.ID {
		return authority.ErrInvalid
	}
	return nil
}
