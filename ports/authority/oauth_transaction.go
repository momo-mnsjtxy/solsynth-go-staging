// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"
)

const OAuthSessionCategory = "oauth-v1"

// Implemented by canonical storage in the same read snapshot/write transaction.
// Missing binding support never accepts an OAuth principal.
type SessionBindingTransaction interface {
	CheckSessionBinding(Session, time.Time) error
}

// AuthenticateOAuth is a distinct, explicit token-use lane. Ordinary bearer
// authentication rejects these tokens, including otherwise valid signatures.
func (v *Verifier) AuthenticateOAuth(ctx context.Context, token string) (Principal, error) {
	if v == nil || v.service == nil {
		return Principal{}, ErrUnavailable
	}
	c, e := v.service.verifyUse(token, OAuthSessionCategory)
	if e != nil {
		return Principal{}, e
	}
	return v.service.withClaims(ctx, c, nil)
}

// OAuthTokenMetadataTransaction accepts only this issuer's original signed
// proof and validates its access-token lifetime and complete canonical binding.
// Session lifetime is deliberately not used as a substitute for token expiry.
func (s *Service) OAuthTokenMetadataTransaction(ctx context.Context, p Principal, tx Transaction) (map[string]any, error) {
	if s == nil || p.Session.Category != OAuthSessionCategory {
		return nil, ErrInvalid
	}
	fresh, e := (&Verifier{service: s}).AuthenticateTransaction(ctx, p, tx)
	if e != nil {
		return nil, e
	}
	c := p.proof.claims
	return map[string]any{"active": true, "scope": strings.Join(fresh.Session.Scopes, " "), "sub": fresh.Account.ID, "client_id": fresh.Session.ClientID, "token_type": "Bearer", "exp": c.Expires, "iat": c.Issued, "nbf": c.NotBefore, "iss": c.Issuer, "aud": c.Audience}, nil
}
func (s *Service) AuthenticateOAuth(ctx context.Context, token string) (Principal, error) {
	return (&Verifier{service: s}).AuthenticateOAuth(ctx, token)
}

func OAuthScope(scope string) bool {
	switch scope {
	case "openid", "profile", "email", "offline_access", "posts.view", "posts.create", "posts.update", "posts.delete", "chat.view", "chat.messages.create", "chat.messages.update", "chat.messages.delete", "profile.read", "wallet.read", "notifications.read", "realms.read":
		return true
	}
	return false
}

func oauthParent(tx Transaction, parent string, now time.Time) (Session, error) {
	p, e := tx.Session(parent)
	if e != nil || p.Type != 0 || p.AccountID != tx.Account().ID || p.RevokedAt != nil || p.AccountVersion != tx.Account().Version || !now.Before(p.ExpiresAt) {
		return Session{}, ErrInvalid
	}
	return p, nil
}

// IssueOAuthTransaction is a trusted adapter seam, not a request authenticator.
// Caller must validate the one-use code, PKCE, client, consent and complete
// canonical binding in this transaction before committing its returned session.
func (s *Service) IssueOAuthTransaction(ctx context.Context, tx Transaction, parent, client string, scopes []string, expires time.Time) (Session, Pair, error) {
	var se Session
	var pair Pair
	if s == nil || tx == nil || ctx.Err() != nil || !ValidID(parent) || !ValidID(client) || len(scopes) < 1 || len(scopes) > 32 {
		return se, pair, ErrInvalid
	}
	now := s.cfg.Now().UTC()
	p, e := oauthParent(tx, parent, now)
	if e != nil {
		return se, pair, e
	}
	if e = s.accountAllowed(tx, now, true); e != nil {
		return se, pair, e
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !OAuthScope(scope) || seen[scope] {
			return se, pair, ErrInvalid
		}
		seen[scope] = true
	}
	if !seen["openid"] || !now.Before(expires) {
		return se, pair, ErrInvalid
	}
	if expires.After(p.ExpiresAt) {
		expires = p.ExpiresAt
	}
	if max := now.Add(s.cfg.SessionTTL); expires.After(max) {
		expires = max
	}
	id, e := randomID()
	if e != nil {
		return se, pair, e
	}
	se = Session{ID: id, AccountID: tx.Account().ID, ParentID: parent, Label: "OAuth app", Type: 1, ClientID: client, CreatedAt: now, LastGrantedAt: now, Category: OAuthSessionCategory, Epoch: 1, AuthorizationEpoch: 1, AccountVersion: tx.Account().Version, ExpiresAt: expires, Scopes: append([]string{}, scopes...)}
	pair, e = s.mint(tx.Account(), &se, now)
	if e != nil {
		return se, pair, e
	}
	// Only the OAuth adapter's hashed rotation family can redeem this secret.
	se.RefreshHash = nil
	if !seen["offline_access"] {
		pair.RefreshToken = ""
		pair.RefreshExpiresIn = 0
	}
	if e = tx.SaveSession(se); e != nil {
		return Session{}, Pair{}, known(e)
	}
	return se, pair, nil
}

// RotateOAuthTransaction cannot extend the original grant or parent lifetime.
// The caller proves and consumes the current refresh hash in the same commit.
func (s *Service) RotateOAuthTransaction(ctx context.Context, tx Transaction, id string) (Session, Pair, error) {
	var pair Pair
	if s == nil || tx == nil || ctx.Err() != nil {
		return Session{}, pair, ErrInvalid
	}
	se, e := tx.Session(id)
	if e != nil {
		return se, pair, known(e)
	}
	now := s.cfg.Now().UTC()
	if se.Type != 1 || se.Category != OAuthSessionCategory || se.RevokedAt != nil || se.AccountVersion != tx.Account().Version || !now.Before(se.ExpiresAt) {
		return se, pair, ErrInvalid
	}
	if _, e = oauthParent(tx, se.ParentID, now); e != nil {
		return se, pair, e
	}
	if e = s.accountAllowed(tx, now, true); e != nil {
		return se, pair, e
	}
	binding, ok := tx.(SessionBindingTransaction)
	if !ok {
		return se, pair, ErrInvalid
	}
	if e = binding.CheckSessionBinding(se, now); e != nil {
		return se, pair, e
	}
	se.Epoch++
	se.LastGrantedAt = now
	pair, e = s.mint(tx.Account(), &se, now)
	if e != nil {
		return se, pair, e
	}
	se.RefreshHash = nil
	se.PreviousHash = nil
	se.SealedPair = nil
	if e = tx.SaveSession(se); e != nil {
		return Session{}, Pair{}, known(e)
	}
	return se, pair, nil
}

// OAuthIssuer and OAuthJWKS expose only explicit configured trust metadata.
func (s *Service) OAuthIssuer() string {
	if s == nil {
		return ""
	}
	return s.cfg.Issuer
}
func (s *Service) OAuthJWKS() map[string]any {
	keys := []map[string]string{}
	ids := []string{}
	if s != nil {
		for id := range s.cfg.TrustedKeys {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		k := s.cfg.TrustedKeys[id]
		keys = append(keys, map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": id, "n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
	}
	return map[string]any{"keys": keys}
}

// OAuthIDToken emits only the approved OIDC authentication claims. Userinfo
// separately projects profile/email from current canonical records under scope.
func (s *Service) OAuthIDToken(ctx context.Context, tx Transaction, id, client, nonce string) (string, error) {
	if s == nil || tx == nil || ctx.Err() != nil || len(client) < 1 || len(client) > 128 || len(nonce) > 256 || strings.ContainsAny(nonce, "\x00\r\n") {
		return "", ErrInvalid
	}
	se, e := tx.Session(id)
	if e != nil || se.Type != 1 || se.Category != OAuthSessionCategory {
		return "", ErrInvalid
	}
	now := s.cfg.Now().UTC()
	parent, e := oauthParent(tx, se.ParentID, now)
	if e != nil {
		return "", e
	}
	exp := now.Add(s.cfg.AccessTTL)
	if se.ExpiresAt.Before(exp) {
		exp = se.ExpiresAt
	}
	if !now.Before(exp) {
		return "", ErrExpired
	}
	claims := map[string]any{"iss": s.cfg.Issuer, "aud": client, "sub": tx.Account().ID, "sid": se.ID, "iat": now.Unix(), "exp": exp.Unix(), "auth_time": parent.CreatedAt.Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": s.cfg.SigningKeyID})
	p, _ := json.Marshal(claims)
	raw := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(raw))
	sig, e := rsa.SignPKCS1v15(rand.Reader, s.cfg.SigningKey, crypto.SHA256, sum[:])
	if e != nil {
		return "", e
	}
	return raw + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
