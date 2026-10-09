// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashAndValidation(t *testing.T) {
	p := "synthetic password with sufficient length"
	a, e := hashPassword(p)
	if e != nil {
		t.Fatal(e)
	}
	b, e := hashPassword(p)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(a, b) || bytes.Contains(a, []byte(p)) || !checkPassword(a, p) || checkPassword(a, "wrong") || checkPassword(a, strings.Repeat("x", 1025)) {
		t.Fatal("password hash safety")
	}
	if _, e = hashPassword("short"); e != ErrInput {
		t.Fatal(e)
	}
	for _, bad := range []string{"", "pbkdf2-sha256$999999999$AAAA$AAAA", "pbkdf2-sha256$600000$bad$bad"} {
		if checkPassword([]byte(bad), p) {
			t.Fatal("accepted malformed hash")
		}
	}
}
func TestTOTPRFCVectorWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	n := time.Unix(59, 0)
	if got := totp(secret, 1); got != "287082" {
		t.Fatal(got)
	}
	if c, ok := checkTOTP(secret, "287082", n, -1); !ok || c != 1 {
		t.Fatal(c, ok)
	}
	if _, ok := checkTOTP(secret, "287082", n, 1); ok {
		t.Fatal("accepted replay")
	}
	if _, ok := checkTOTP(secret, "287082", time.Unix(150, 0), -1); ok {
		t.Fatal("accepted expired TOTP")
	}
	for _, bad := range []string{"28708x", "2870820", " 287082"} {
		if _, ok := checkTOTP(secret, bad, n, -1); ok {
			t.Fatal("accepted malformed TOTP")
		}
	}
}
func TestTOTPEncryptionBindsAccountAndFactor(t *testing.T) {
	s := &Service{cfg: Config{SealKey: bytes.Repeat([]byte{7}, 32)}}
	secret := []byte("synthetic TOTP seed!")
	c, e := s.seal(secret, "account-a", "factor-a")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(c, secret) {
		t.Fatal("plaintext seed")
	}
	got, e := s.open(c, "account-a", "factor-a")
	if e != nil || !bytes.Equal(got, secret) {
		t.Fatal(e)
	}
	for _, v := range [][2]string{{"account-b", "factor-a"}, {"account-a", "factor-b"}} {
		if _, e = s.open(c, v[0], v[1]); e == nil {
			t.Fatal("cross-factor secret accepted")
		}
	}
	c[len(c)-1] ^= 1
	if _, e = s.open(c, "account-a", "factor-a"); e == nil {
		t.Fatal("tampered ciphertext")
	}
}
func TestStrictHTTPDecodeAndPeer(t *testing.T) {
	for _, body := range []string{`{"account":"a","account":"b"}`, `{"account":"a","unknown":1}`, `{"account":"a"} {}`, strings.Repeat("x", 16385)} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		var v struct {
			Account string `json:"account"`
		}
		if decode(httptest.NewRecorder(), r, &v) == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`"287082"`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", "10.2.3.4")
	var code string
	if e := decode(httptest.NewRecorder(), r, &code); e != nil || code != "287082" {
		t.Fatal(e)
	}
	if peer(r) == "10.2.3.4" {
		t.Fatal("trusted forwarding header")
	}
}
func TestDTOSecretsNeverSerialize(t *testing.T) {
	f := Factor{ID: "synthetic", Secret: []byte("secret"), LastCounter: 4}
	b, e := json.Marshal(f)
	if e != nil {
		t.Fatal(e)
	}
	for _, k := range []string{"secret", "last_counter", "pending_until"} {
		if strings.Contains(string(b), k) {
			t.Fatal("factor leaked private field")
		}
	}
	c := Challenge{ID: "public challenge", Hash: []byte("private"), AccountID: "public-account", Version: 3, Purpose: "private-purpose"}
	b, e = json.Marshal(c)
	if e != nil || strings.Contains(string(b), "private") {
		t.Fatal("challenge leaked internals", e)
	}
}
