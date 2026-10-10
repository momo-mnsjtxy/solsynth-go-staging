// SPDX-License-Identifier: AGPL-3.0-only
package oauthpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/oauth"
)

// Introspection is restricted to the confidential client which owns the grant.
// It never leaks another client's subject, scopes or token validity.
func (r *Repository) Introspect(ctx context.Context, client, secret, token string) (map[string]any, error) {
	inactive := map[string]any{"active": false}
	if r == nil {
		return nil, oauth.ErrUnavailable
	}
	if secret == "" {
		return nil, oauth.ErrClient
	}
	if e := r.preflightClient(ctx, client, secret); e != nil {
		return nil, e
	}
	p, e := r.issuer.AuthenticateOAuth(ctx, token)
	if e != nil {
		if errors.Is(e, authority.ErrUnavailable) {
			return nil, e
		}
		return inactive, nil
	}
	var owner string
	if e = r.db.QueryRowContext(ctx, `SELECT client_id FROM owned_oauth_grants WHERE session_id=$1`, p.Session.ID).Scan(&owner); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return inactive, nil
		}
		return nil, e
	}
	if owner != client {
		return inactive, nil
	}
	var out map[string]any
	e = r.within(ctx, p.Account.ID, client, false, func(b *boundary) error {
		if b.app.OAuthConfig == nil || b.app.OAuthConfig.IsPublicClient {
			return oauth.ErrClient
		}
		if _, e := r.client(ctx, b, secret); e != nil {
			return e
		}
		if p.Session.ClientID != b.app.ID {
			return oauth.ErrGrant
		}
		b.final = func() error {
			if _, e := r.client(ctx, b, secret); e != nil {
				return e
			}
			var e error
			out, e = r.issuer.OAuthTokenMetadataTransaction(ctx, p, b.authority)
			if e == nil {
				out["client_id"] = client
			}
			return e
		}
		return nil
	})
	if e != nil {
		var f *authority.Failure
		if errors.As(e, &f) && f.Status != 503 || errors.Is(e, oauth.ErrGrant) {
			return inactive, nil
		}
		return nil, e
	}
	return out, nil
}
