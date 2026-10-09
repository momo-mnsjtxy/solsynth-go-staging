// SPDX-License-Identifier: AGPL-3.0-only
package pushprovider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fcmTransport func(*http.Request) (*http.Response, error)

func (f fcmTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fcmResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestFCMFixedEndpointSignedCredentialsAndPrivateChallenge(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	calls := 0
	now := time.Now().UTC()
	client := &http.Client{Transport: fcmTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Host {
		case "oauth2.googleapis.com":
			if r.URL.Path != "/token" || r.Method != "POST" {
				t.Fatal("unexpected authentication endpoint")
			}
			r.ParseForm()
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("bad JWT")
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
				t.Fatal("unsigned server assertion")
			}
			claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var c map[string]any
			if json.Unmarshal(claims, &c) != nil || c["scope"] != "https://www.googleapis.com/auth/firebase.messaging" || c["aud"] != "https://oauth2.googleapis.com/token" {
				t.Fatal("incorrect service scope")
			}
			return fcmResponse(`{"access_token":"server-only-token","token_type":"Bearer","expires_in":3600}`), nil
		case "fcm.googleapis.com":
			if r.URL.Path != "/v1/projects/synthetic-project/messages:send" || r.Header.Get("Authorization") != "Bearer server-only-token" {
				t.Fatal("wrong FCM project/credential")
			}
			var body struct {
				Message struct {
					Token string
					Data  map[string]string
				}
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Message.Token != "synthetic-device-token" || body.Message.Data["challenge"] != "private-device-code" {
				t.Fatal("not delivered to the device")
			}
			return fcmResponse(`{"name":"projects/synthetic-project/messages/server-receipt"}`), nil
		default:
			t.Fatal("arbitrary network endpoint", r.URL.Host)
			return nil, nil
		}
	})}
	f, e := NewFCM(FCMConfig{ProjectID: "synthetic-project", ServiceAccountEmail: "backend@synthetic-project.iam.gserviceaccount.com", PrivateKeyPEM: pemKey}, client)
	if e != nil {
		t.Fatal(e)
	}
	f.now = func() time.Time { return now }
	for range 2 {
		if e = f.SendDevice(context.Background(), "synthetic-device-token", map[string]string{"challenge": "private-device-code"}); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 3 {
		t.Fatal("unbounded token exchange", calls)
	}
	if _, e = NewFCM(FCMConfig{ProjectID: "synthetic-project/../../attacker", ServiceAccountEmail: f.cfg.ServiceAccountEmail, PrivateKeyPEM: pemKey}, client); e == nil {
		t.Fatal("noncanonical project escaped fixed endpoint")
	}
}
