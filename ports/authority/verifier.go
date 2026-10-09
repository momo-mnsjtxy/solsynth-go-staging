// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"crypto/rsa"
	"errors"
	"math/big"
	"net/http"
	"time"
)

// Verifier intentionally exposes no issuance, refresh, code or session-write
// methods. Domain services need only public trust keys and fresh database reads.
type Verifier struct{ service *Service }

func NewVerifier(store ReadStore, c Config) (*Verifier, error) {
	if store == nil || c.Issuer == "" || c.Audience == "" || len(c.TrustedKeys) == 0 {
		return nil, errors.New("authority: store, issuer, audience and explicit trusted public keys required")
	}
	keys := map[string]*rsa.PublicKey{}
	for id, k := range c.TrustedKeys {
		if id == "" || k == nil || k.N == nil || k.N.BitLen() < 2048 || k.E < 65537 {
			return nil, errors.New("authority: invalid trusted RSA key")
		}
		keys[id] = &rsa.PublicKey{N: new(big.Int).Set(k.N), E: k.E}
	}
	if c.AccessTTL == 0 {
		c.AccessTTL = 10 * time.Minute
	}
	if c.AccessTTL < time.Second || c.AccessTTL > time.Hour || c.AccessTTL%time.Second != 0 {
		return nil, errors.New("authority: invalid maximum access-token lifetime")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	// Only verification settings cross this boundary, even if a caller supplied
	// unrelated issuer configuration. No private key/refresh secret is retained.
	safe := Config{Issuer: c.Issuer, Audience: c.Audience, TrustedKeys: keys, AccessTTL: c.AccessTTL, Now: c.Now}
	return &Verifier{service: &Service{reader: store, cfg: safe}}, nil
}
func (v *Verifier) Authenticate(ctx context.Context, token string) (Principal, error) {
	return v.service.Authenticate(ctx, token)
}
func (v *Verifier) Authorize(ctx context.Context, token string, permissions ...string) (Principal, error) {
	return v.service.Authorize(ctx, token, permissions...)
}
func (v *Verifier) AuthenticateRequest(r *http.Request) (Principal, error) {
	return v.service.AuthenticateRequest(r)
}
func (v *Verifier) AuthorizeRequest(r *http.Request, permissions ...string) (Principal, error) {
	return v.service.AuthorizeRequest(r, permissions...)
}
func (v *Verifier) AuthorizePrincipal(ctx context.Context, p Principal, permissions ...string) (Principal, error) {
	return v.service.AuthorizePrincipal(ctx, p, permissions...)
}
