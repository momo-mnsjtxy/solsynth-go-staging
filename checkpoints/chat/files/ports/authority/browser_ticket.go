// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"time"
)

// BrowserTicketRedeemer atomically consumes a random ticket hash. Implementers
// return recorded coordinates only to the verifier, which rechecks canonical
// account, session, OAuth binding and authorization generation state.
type BrowserTicketRedeemer interface {
	RedeemBrowserTicket(context.Context, []byte) (actor, session string, version, sessionEpoch, authorizationEpoch int64, use string, issuedAt, expiresAt time.Time, err error)
}

// RedeemBrowserTicket accepts only the random one-use capability. It does not
// expose a public coordinate-to-principal constructor.
func (v *Verifier) RedeemBrowserTicket(ctx context.Context, hash []byte) (Principal, error) {
	if v == nil || v.service == nil || len(hash) != 32 {
		return Principal{}, ErrInvalid
	}
	var backend any = v.service.store
	if v.service.reader != nil {
		backend = v.service.reader
	}
	store, ok := backend.(BrowserTicketRedeemer)
	if !ok {
		return Principal{}, ErrUnavailable
	}
	actor, session, version, sessionEpoch, authorizationEpoch, use, issuedAt, expiresAt, err := store.RedeemBrowserTicket(ctx, hash)
	if err != nil {
		return Principal{}, err
	}
	return v.resumeBrowserTicket(ctx, actor, session, version, sessionEpoch, authorizationEpoch, use, issuedAt, expiresAt)
}

func (v *Verifier) resumeBrowserTicket(ctx context.Context, actor, session string, version, sessionEpoch, authorizationEpoch int64, use string, issuedAt, expiresAt time.Time) (Principal, error) {
	if v == nil || v.service == nil || !ValidID(actor) || !ValidID(session) || version < 1 || sessionEpoch < 1 || authorizationEpoch < 1 || authorizationEpoch > sessionEpoch {
		return Principal{}, ErrInvalid
	}
	now := v.service.cfg.Now().UTC()
	issuedAt, expiresAt = issuedAt.UTC(), expiresAt.UTC()
	if issuedAt.After(now) || !expiresAt.After(now) || !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > 30*time.Second {
		return Principal{}, ErrExpired
	}
	if use != "user" && use != OAuthSessionCategory {
		return Principal{}, ErrInvalid
	}
	c := claims{Issuer: v.service.cfg.Issuer, Audience: v.service.cfg.Audience, Subject: actor, SessionID: session, ID: session, Use: use, Version: version, Epoch: sessionEpoch, Expires: expiresAt.Unix(), Issued: issuedAt.Unix(), NotBefore: issuedAt.Unix()}
	return v.service.withClaims(ctx, c, func(_ Transaction, p Principal, _ time.Time) error {
		if p.Session.AuthorizationEpoch != authorizationEpoch {
			return ErrStale
		}
		return nil
	})
}
