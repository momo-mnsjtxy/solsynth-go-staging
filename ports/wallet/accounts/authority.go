// SPDX-License-Identifier: AGPL-3.0-only
package accounts

import (
	"context"
	"net/http"
)

// VerifiedIdentity's P must be a proof-bearing principal minted by trusted
// verification, not a decoded JWT map. The owned authority.Verifier implements
// this with an unexported service-bound proof and fresh database rechecks.
type VerifiedIdentity[P any] interface {
	AuthenticateRequest(*http.Request) (P, error)
	AuthenticatePrincipal(context.Context, P) (P, error)
	AuthorizePrincipal(context.Context, P, ...string) (P, error)
}
type RealmAuthority interface {
	Realm(context.Context, Principal, string, RealmRole) error
	RealmIDs(context.Context, Principal) ([]string, error)
}
type verifiedContextKey struct{}
type verifiedEnvelope[P any] struct {
	bridge *identityBridge[P]
	proof  P
}
type identityBridge[P any] struct {
	identity VerifiedIdentity[P]
	project  func(P) Principal
	realms   RealmAuthority
	mapError func(error) error
}

// NewAuthorityHandler composes actual verified identity, fresh grants, and
// mandatory payment policy. The caller configures authority public trust keys;
// this adapter keeps only the proof-bearing principal in private request state.
// It never retains bearer tokens, signing keys, or request-supplied account IDs.
func NewAuthorityHandler[P any](store Store, identity VerifiedIdentity[P], project func(P) Principal, realms RealmAuthority, payments PaymentVerifier, mapError func(error) error) http.Handler {
	bridge := &identityBridge[P]{identity: identity, project: project, realms: realms, mapError: mapError}
	return NewHandler(&Service{Store: store, Access: bridge, Payments: payments}, bridge)
}
func (b *identityBridge[P]) known(e error) error {
	if e == nil {
		return nil
	}
	if b.mapError != nil {
		return b.mapError(e)
	}
	return ErrUnauthorized
}
func (b *identityBridge[P]) Authenticate(r *http.Request) (Principal, error) {
	if b.identity == nil || b.project == nil {
		return Principal{}, ErrUnavailable
	}
	proof, e := b.identity.AuthenticateRequest(r)
	if e != nil {
		return Principal{}, b.known(e)
	}
	p := b.project(proof)
	if !ValidID(p.AccountID) || p.SessionID == "" {
		return Principal{}, ErrUnauthorized
	}
	*r = *r.WithContext(context.WithValue(r.Context(), verifiedContextKey{}, verifiedEnvelope[P]{b, proof}))
	return p, nil
}
func (b *identityBridge[P]) verify(ctx context.Context, p Principal, permissions ...string) error {
	envelope, ok := ctx.Value(verifiedContextKey{}).(verifiedEnvelope[P])
	if !ok || envelope.bridge != b || b.project(envelope.proof) != p {
		return ErrUnauthorized
	}
	var proof P
	var e error
	if len(permissions) == 0 {
		proof, e = b.identity.AuthenticatePrincipal(ctx, envelope.proof)
	} else {
		proof, e = b.identity.AuthorizePrincipal(ctx, envelope.proof, permissions...)
	}
	if e != nil {
		return b.known(e)
	}
	if b.project(proof) != p {
		return ErrUnauthorized
	}
	return nil
}
func (b *identityBridge[P]) Permission(ctx context.Context, p Principal, key string) error {
	return b.verify(ctx, p, key)
}
func (b *identityBridge[P]) Realm(ctx context.Context, p Principal, id string, role RealmRole) error {
	if e := b.verify(ctx, p); e != nil {
		return e
	}
	if b.realms == nil {
		return ErrUnavailable
	}
	return b.realms.Realm(ctx, p, id, role)
}
func (b *identityBridge[P]) RealmIDs(ctx context.Context, p Principal) ([]string, error) {
	if e := b.verify(ctx, p); e != nil {
		return nil, e
	}
	if b.realms == nil {
		return nil, ErrUnavailable
	}
	return b.realms.RealmIDs(ctx, p)
}
