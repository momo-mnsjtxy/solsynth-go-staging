// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/accounts"
)

// proofIdentity bridges the already verified private host context into the
// wallet's private proof envelope. The source request header has been removed.
type proofIdentity struct{ identity }

func (a proofIdentity) AuthenticateRequest(r *http.Request) (authority.Principal, error) {
	return a.require(r.Context())
}
func (a proofIdentity) AuthenticatePrincipal(ctx context.Context, p authority.Principal) (authority.Principal, error) {
	return a.verifier.AuthenticatePrincipal(ctx, p)
}
func (a proofIdentity) AuthorizePrincipal(ctx context.Context, p authority.Principal, keys ...string) (authority.Principal, error) {
	return a.verifier.AuthorizePrincipal(ctx, p, keys...)
}
func walletError(err error) error {
	var f *authority.Failure
	if errors.As(err, &f) {
		switch f.Status {
		case 401:
			return accounts.ErrUnauthorized
		case 403, 423:
			return accounts.ErrForbidden
		}
	}
	return accounts.ErrUnavailable
}
func walletHandler(db *sql.DB, id identity, peppers map[string][]byte) (http.Handler, *walletpg.Repository) {
	repo := walletpg.New(db)
	handler := accounts.NewAuthorityHandler(repo, proofIdentity{id}, func(p authority.Principal) accounts.Principal {
		return accounts.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
	}, walletpg.NewRealmAuthority(db), walletpg.NewPINVerifier(db, peppers), walletError)
	return bindIdentity(id.verifier, handler), repo
}
