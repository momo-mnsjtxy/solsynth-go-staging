// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"solsynth.local/go-migration/ports/wallet/accounts"
)

// proofIdentity bridges the already verified private host context into the
// wallet's private proof envelope. The source request header has been removed.
type proofIdentity struct{ identity }

// RequestIdentityPresent distinguishes anonymous requests from a present proof
// that fails revalidation after the host has removed the raw bearer header.
func (a proofIdentity) RequestIdentityPresent(r *http.Request) bool {
	return r.Context().Value(principalKey{}) != nil
}
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
func walletHandler(db *sql.DB, id identity, payments *walletpg.PINVerifier, guard *authoritypg.TransactionGuard, realms *socialpg.Repository) (http.Handler, *walletpg.Repository) {
	repo := walletpg.NewWithAuthority(db, guard)
	if guard == nil {
		repo = walletpg.New(db) // Explicit separate-pool non-atomic opt-in.
	}
	repo.WithRealmWritePolicy(canonicalRealmWrites{realms})
	repo.WithRealmReadPolicy(canonicalRealmWrites{realms})
	repo.WithPaymentAuthorization(payments)
	handler := accounts.NewAuthorityHandler(repo, proofIdentity{id}, func(p authority.Principal) accounts.Principal {
		return accounts.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
	}, realms, payments.Atomic(), walletError)
	return bindIdentity(id.verifier, bindTransactionAuthority(guard, handler)), repo
}

type canonicalRealmWrites struct{ repository *socialpg.Repository }

func (c canonicalRealmWrites) AuthorizeRealmWrite(ctx context.Context, tx *sql.Tx, actor, realm string) error {
	if c.repository == nil {
		return accounts.ErrUnavailable
	}
	role, err := c.repository.RealmRoleForWriteInTransaction(ctx, tx, actor, realm)
	if errors.Is(err, social.ErrForbidden) || errors.Is(err, social.ErrNotFound) {
		return accounts.ErrForbidden
	}
	if err != nil {
		return accounts.ErrUnavailable
	}
	if role != social.Owner {
		return accounts.ErrForbidden
	}
	return nil
}

func (a proofIdentity) Require(ctx context.Context, keys ...string) (authority.Principal, error) {
	return a.require(ctx, keys...)
}

func (c canonicalRealmWrites) AuthorizeRealmRead(ctx context.Context, tx *sql.Tx, actor, realm string) error {
	if c.repository == nil {
		return accounts.ErrUnavailable
	}
	_, err := c.repository.RealmRoleForWriteInTransaction(ctx, tx, actor, realm)
	if errors.Is(err, social.ErrForbidden) || errors.Is(err, social.ErrNotFound) {
		return accounts.ErrNotFound
	}
	if err != nil {
		return accounts.ErrUnavailable
	}
	return nil
}
