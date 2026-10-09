// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"testing"
	"time"
)

// Saved only in exclusive disposable restart markers, never uploaded as a CI
// artifact. It contains public verifier material and the synthetic test clock;
// it cannot sign or issue replacement credentials after restart.
type restartAuthoritySnapshot struct {
	Issuer, Audience, KeyID string
	PublicKeys              map[string][]byte
	AccessTTL               time.Duration
	Now                     time.Time
	CapturedWall            time.Time
}

func saveAuthoritySnapshot(t *testing.T, c authority.Config) restartAuthoritySnapshot {
	t.Helper()
	s := restartAuthoritySnapshot{Issuer: c.Issuer, Audience: c.Audience, KeyID: c.SigningKeyID, PublicKeys: map[string][]byte{}, AccessTTL: c.AccessTTL, Now: c.Now().UTC(), CapturedWall: time.Now().UTC()}
	for id, key := range c.TrustedKeys {
		b, e := x509.MarshalPKIXPublicKey(key)
		if e != nil {
			t.Fatal(e)
		}
		s.PublicKeys[id] = b
	}
	if len(s.PublicKeys) == 0 {
		t.Fatal("missing original verification keys")
	}
	return s
}
func restoreAuthoritySnapshot(t *testing.T, s restartAuthoritySnapshot) authority.Config {
	t.Helper()
	keys := map[string]*rsa.PublicKey{}
	for id, b := range s.PublicKeys {
		k, e := x509.ParsePKIXPublicKey(b)
		if e != nil {
			t.Fatal(e)
		}
		v, ok := k.(*rsa.PublicKey)
		if !ok {
			t.Fatal("unexpected saved public key type")
		}
		keys[id] = v
	}
	return authority.Config{Issuer: s.Issuer, Audience: s.Audience, SigningKeyID: s.KeyID, TrustedKeys: keys, AccessTTL: s.AccessTTL, Now: func() time.Time { return s.Now.Add(time.Since(s.CapturedWall)) }}
}

// Configure one-hour synthetic credentials BEFORE creating restart target
// state. The bounded CI job is fifty minutes; no lifetime changes or token
// issuance occur after restart. The verifier clock advances by real elapsed
// wall time across processes while preserving the fixture calendar date.
func calendarRestartAuthority(t *testing.T, f calendarFixture) calendarFixture {
	t.Helper()
	originalNow, wall := f.authConfig.Now().UTC(), time.Now()
	f.authConfig.AccessTTL = time.Hour
	f.authConfig.SessionTTL = time.Hour
	f.authConfig.Now = func() time.Time { return originalNow.Add(time.Since(wall)) }
	issuer, e := authority.New(authoritypg.New(f.db), f.authConfig)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range []struct {
		actor string
		token *string
	}{{f.actor, &f.token}, {f.other, &f.otherToken}} {
		pair, e := issuer.IssueSession(context.Background(), item.actor, authority.SessionOptions{Label: "synthetic bounded restart", Scopes: []string{"*"}})
		if e != nil {
			t.Fatal(e)
		}
		*item.token = pair.Token
	}
	return calendarPool(t, f, f.db)
}
