// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testKeys struct {
	key    *rsa.PrivateKey
	remote RemoteKey
}

func (k testKeys) LocalKey(context.Context, string) (crypto.Signer, error) { return k.key, nil }
func (k testKeys) RemoteKey(_ context.Context, id string) (RemoteKey, error) {
	if id != k.remote.KeyID {
		return RemoteKey{}, ErrNotFound
	}
	return k.remote, nil
}
func (k testKeys) RemoteActor(_ context.Context, id string) (RemoteKey, error) {
	if id != k.remote.Actor {
		return RemoteKey{}, ErrNotFound
	}
	return k.remote, nil
}
func keyFixture(t *testing.T) (Config, testKeys) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 8, 1, 2, 3, 0, time.UTC)
	return Config{Origin: "https://local.example.test", AllowedOrigins: []string{"https://remote.example.test"}, Now: func() time.Time { return now }, MaxAttempts: 4, DeliveryTimeout: time.Second}, testKeys{key, RemoteKey{Actor: "https://remote.example.test/actor", Inbox: "https://remote.example.test/inbox", KeyID: "https://remote.example.test/actor#main-key", PublicKey: &key.PublicKey}}
}
func signedFixture(t *testing.T, c Config, k testKeys) (*http.Request, []byte) {
	t.Helper()
	a := Activity{ID: k.remote.Actor + "/follow/1", Type: "Follow", Actor: k.remote.Actor, Object: json.RawMessage(`"https://local.example.test/activitypub/actors/alice"`)}
	b, _ := json.Marshal(a)
	r := httptest.NewRequest("POST", c.Origin+"/activitypub/actors/alice/inbox", bytes.NewReader(b))
	if e := Sign(r, b, k.remote.KeyID, k.key, c.Clock()); e != nil {
		t.Fatal(e)
	}
	return r, b
}
func TestSignatureBindsBodyActorHostTargetAndDate(t *testing.T) {
	c, k := keyFixture(t)
	r, b := signedFixture(t, c, k)
	v, e := Verify(context.Background(), c, k, r, b)
	if e != nil || !v.ValidFor(c.Origin+"/activitypub/actors/alice") {
		t.Fatal(v, e)
	}
	tests := []struct {
		name   string
		change func(*http.Request, []byte) []byte
	}{
		{"body", func(r *http.Request, b []byte) []byte {
			return bytes.Replace(b, []byte("/follow/1"), []byte("/follow/2"), 1)
		}},
		{"host", func(r *http.Request, b []byte) []byte { r.Host = "other.example.test"; return b }},
		{"target", func(r *http.Request, b []byte) []byte { r.URL.Path = "/activitypub/actors/bob/inbox"; return b }},
		{"date", func(r *http.Request, b []byte) []byte {
			r.Header.Set("Date", c.Clock().Add(-6*time.Minute).Format(http.TimeFormat))
			return b
		}},
		{"duplicate_digest", func(r *http.Request, b []byte) []byte { r.Header.Add("Digest", r.Header.Get("Digest")); return b }},
		{"missing_digest_coverage", func(r *http.Request, b []byte) []byte {
			r.Header.Set("Signature", strings.Replace(r.Header.Get("Signature"), "host date digest content-type", "host date", 1))
			return b
		}},
		{"bearer_injection", func(r *http.Request, b []byte) []byte { r.Header.Set("Authorization", "Bearer opaque"); return b }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := signedFixture(t, c, k)
			b = tt.change(r, b)
			if _, e := Verify(context.Background(), c, k, r, b); e == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	// Signed content from a key owned by a different actor remains unauthorized.
	r, b = signedFixture(t, c, k)
	b = bytes.ReplaceAll(b, []byte(k.remote.Actor), []byte("https://remote.example.test/other"))
	if e = Sign(r, b, k.remote.KeyID, k.key, c.Clock()); e != nil {
		t.Fatal(e)
	}
	if _, e = Verify(context.Background(), c, k, r, b); !errors.Is(e, ErrSignature) {
		t.Fatal(e)
	}
	if _, e = Verify(context.Background(), c, nil, r, b); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestActivityJSONAndDestinationPolicy(t *testing.T) {
	c, _ := keyFixture(t)
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"http://remote.example.test/inbox", "https://127.0.0.1/inbox", "https://remote.example.test@127.0.0.1/inbox", "https://remote.example.test:8443/inbox", "https://remote.example.test/inbox?token=x", "https://remote.example.test./inbox", "https://remote.example.test/a/../inbox", "https://remote.example.test/inbox#fragment", "https://other.example.test/inbox", "https://remote.example.test/%2fadmin"} {
		if c.Allows(s) {
			t.Fatal("allowed destination", s)
		}
	}
	for _, s := range []string{`{"id":"a","id":"b"}`, `{"object":{"bcc":[]}}`, `{"Actor":"https://remote.example.test/a"}`, `{"publicKey":{"PublicKeyPem":"hidden"}}`, `{"x":1} {"x":2}`, strings.Repeat("[", 15) + "0" + strings.Repeat("]", 15)} {
		if CheckJSON([]byte(s)) == nil {
			t.Fatal("accepted JSON", s)
		}
	}
	if e := CheckJSON([]byte(`{"object":{"id":"a"},"to":["b"]}`)); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"127.0.0.1", "10.2.3.4", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::a00:1"} {
		if publicAddress(net.ParseIP(s)) {
			t.Fatal("accepted nonpublic address", s)
		}
	}
	if !publicAddress(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
}
func TestVerifiedEnvelopeIsImmutable(t *testing.T) {
	c, k := keyFixture(t)
	r, b := signedFixture(t, c, k)
	v, e := Verify(context.Background(), c, k, r, b)
	if e != nil {
		t.Fatal(e)
	}
	a := v.Activity()
	a.Object[0] = '!'
	if v.Activity().Object[0] == '!' {
		t.Fatal("verified body can be mutated")
	}
	if (Verified{}).ValidFor(c.Origin + "/activitypub/actors/alice") {
		t.Fatal("zero proof accepted")
	}
}
