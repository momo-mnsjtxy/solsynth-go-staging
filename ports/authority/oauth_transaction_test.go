// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type oauthBoundMemory struct {
	*memStore
	allow *atomic.Bool
}
type oauthBoundTx struct {
	Transaction
	allow *atomic.Bool
}

func (t oauthBoundTx) CheckSessionBinding(s Session, now time.Time) error {
	if !t.allow.Load() || s.Category != OAuthSessionCategory {
		return ErrInvalid
	}
	_, e := oauthParent(t.Transaction, s.ParentID, now)
	return e
}
func (m oauthBoundMemory) WithinAccount(ctx context.Context, id string, fn func(Transaction) error) error {
	return m.memStore.WithinAccount(ctx, id, func(tx Transaction) error { return fn(oauthBoundTx{tx, m.allow}) })
}
func (m oauthBoundMemory) ReadAccount(ctx context.Context, id string, fn func(Transaction) error) error {
	return m.WithinAccount(ctx, id, fn)
}
func TestAuthorityBoundOAuthTransactionLaneAndCascade(t *testing.T) {
	base, m, clock := setup(t)
	allowed := &atomic.Bool{}
	allowed.Store(true)
	store := oauthBoundMemory{m, allowed}
	issuer, e := New(store, base.cfg)
	if e != nil {
		t.Fatal(e)
	}
	v, e := NewVerifier(store, base.cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	parent := issue(t, issuer, accountA, "*")
	p, e := v.Authenticate(ctx, parent.Token)
	if e != nil {
		t.Fatal(e)
	}
	var se Session
	var pair Pair
	e = store.WithinAccount(ctx, accountA, func(tx Transaction) error {
		var x error
		se, pair, x = issuer.IssueOAuthTransaction(ctx, tx, p.Session.ID, accountB, []string{"openid", "profile", "offline_access"}, time.Unix(clock.Load(), 0).Add(2*time.Hour))
		return x
	})
	if e != nil {
		t.Fatal(e)
	}
	if se.ExpiresAt.After(p.Session.ExpiresAt) || se.ParentID != p.Session.ID || len(se.RefreshHash) != 0 || pair.RefreshToken == "" {
		t.Fatal("canonical child/refresh bounds", se)
	}
	delegated, e := v.AuthenticateOAuth(ctx, pair.Token)
	if e != nil || delegated.Session.ID != se.ID {
		t.Fatal(delegated, e)
	}
	_, e = v.Authenticate(ctx, pair.Token)
	want(t, e, ErrInvalid)
	_, e = v.AuthenticateOAuth(ctx, parent.Token)
	want(t, e, ErrInvalid)
	if _, e = issuer.Refresh(ctx, pair.RefreshToken, 1, accountB); e == nil {
		t.Fatal("generic refresh accepted OAuth family")
	}
	other, _ := NewVerifier(store, base.cfg)
	_, e = other.AuthenticatePrincipal(ctx, delegated)
	want(t, e, ErrInvalid)
	var rotated Pair
	var idToken string
	e = store.WithinAccount(ctx, accountA, func(tx Transaction) error {
		var x error
		_, rotated, x = issuer.RotateOAuthTransaction(ctx, tx, se.ID)
		if x != nil {
			return x
		}
		idToken, x = issuer.OAuthIDToken(ctx, tx, se.ID, "synthetic-client", "synthetic-bound-nonce")
		return x
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = v.AuthenticateOAuth(ctx, pair.Token)
	want(t, e, ErrStale)
	if _, e = v.AuthenticateOAuth(ctx, rotated.Token); e != nil {
		t.Fatal(e)
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		t.Fatal("ID token shape")
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if e = rsa.VerifyPKCS1v15(&base.cfg.SigningKey.PublicKey, crypto.SHA256, sum[:], sig); e != nil {
		t.Fatal(e)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil || claims["aud"] != "synthetic-client" || claims["iss"] != base.cfg.Issuer || claims["sub"] != accountA || claims["nonce"] != "synthetic-bound-nonce" {
		t.Fatal(claims)
	}
	_, e = v.AuthenticateOAuth(ctx, idToken)
	want(t, e, ErrInvalid)
	allowed.Store(false)
	_, e = v.AuthenticateOAuth(ctx, rotated.Token)
	want(t, e, ErrInvalid)
	allowed.Store(true)
	if e = issuer.Revoke(ctx, parent.Token, "current"); e != nil {
		t.Fatal(e)
	}
	_, e = v.AuthenticateOAuth(ctx, rotated.Token)
	if !errors.Is(e, ErrRevoked) {
		t.Fatal("parent cascade", e)
	}
}
