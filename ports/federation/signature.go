// SPDX-License-Identifier: AGPL-3.0-only
package federation

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Verified can only be populated by Verify. It binds the body and pinned key
// owner to the exact signed inbox route; a zero value grants no authority.
type Verified struct {
	activity Activity
	remote   RemoteKey
	digest   [32]byte
	target   string
	valid    bool
	signedAt time.Time
}

func (v Verified) Activity() Activity {
	a := v.activity
	a.Object = append(json.RawMessage{}, a.Object...)
	a.Target = append(json.RawMessage{}, a.Target...)
	a.Instrument = append(json.RawMessage{}, a.Instrument...)
	a.To = append([]string{}, a.To...)
	a.CC = append([]string{}, a.CC...)
	return a
}
func (v Verified) Fresh(now time.Time) bool {
	return v.valid && now.Sub(v.signedAt) <= 5*time.Minute && v.signedAt.Sub(now) <= 5*time.Minute
}
func snapshotRemote(k RemoteKey) RemoteKey {
	if k.PublicKey != nil {
		public := *k.PublicKey
		if public.N != nil {
			public.N = new(big.Int).Set(public.N)
		}
		k.PublicKey = &public
	}
	return k
}

// Snapshot prevents a provider-owned RSA modulus from changing a retained binding.
func (k RemoteKey) Snapshot() RemoteKey          { return snapshotRemote(k) }
func (v Verified) Remote() RemoteKey             { return snapshotRemote(v.remote) }
func (v Verified) Digest() []byte                { return append([]byte{}, v.digest[:]...) }
func (v Verified) ValidFor(actorURL string) bool { return v.valid && v.target == actorURL+"/inbox" }

func PEM(signer crypto.Signer) (string, error) {
	if signer == nil {
		return "", ErrUnavailable
	}
	pub, ok := signer.Public().(*rsa.PublicKey)
	if !ok || pub == nil || pub.N == nil || pub.N.BitLen() < 2048 || pub.N.BitLen() > 8192 {
		return "", ErrUnavailable
	}
	b, e := x509.MarshalPKIXPublicKey(pub)
	if e != nil {
		return "", ErrUnavailable
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b})), nil
}
func signatureParams(raw string) (map[string]string, error) {
	if len(raw) > 16384 {
		return nil, ErrSignature
	}
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || out[k] != "" || len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' || strings.ContainsAny(v[1:len(v)-1], "\"\\\r\n") {
			return nil, ErrSignature
		}
		switch k {
		case "keyId", "algorithm", "headers", "signature":
		default:
			return nil, ErrSignature
		}
		out[k] = v[1 : len(v)-1]
	}
	if len(out) != 4 || out["algorithm"] != "rsa-sha256" || out["keyId"] == "" || out["signature"] == "" {
		return nil, ErrSignature
	}
	return out, nil
}
func signingString(r *http.Request, headers string) (string, error) {
	if headers != "(request-target) host date digest" && headers != "(request-target) host date digest content-type" {
		return "", ErrSignature
	}
	parts := []string{"(request-target): " + strings.ToLower(r.Method) + " " + r.URL.RequestURI(), "host: " + r.Host}
	for _, name := range strings.Fields(headers)[2:] {
		values := r.Header.Values(name)
		if len(values) != 1 || strings.ContainsAny(values[0], "\r\n") {
			return "", ErrSignature
		}
		parts = append(parts, name+": "+values[0])
	}
	return strings.Join(parts, "\n"), nil
}
func Verify(ctx context.Context, c Config, keys KeyProvider, r *http.Request, body []byte) (Verified, error) {
	var out Verified
	if keys == nil {
		return out, ErrUnavailable
	}
	if e := c.Validate(); e != nil {
		return out, ErrUnavailable
	}
	if r.Method != "POST" || len(r.Header.Values("Signature")) != 1 || r.Header.Get("Authorization") != "" {
		return out, ErrSignature
	}
	origin, e := Origin(c.Origin)
	if e != nil || origin != "https://"+r.Host || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		return out, ErrSignature
	}
	if e = CheckJSON(body); e != nil {
		return out, e
	}
	var a Activity
	if json.Unmarshal(body, &a) != nil || a.ID == "" || a.Actor == "" || len(a.To) > 100 || len(a.CC) > 100 {
		return out, ErrInvalid
	}
	if !c.Allows(a.Actor) || !c.Allows(a.ID) {
		return out, ErrSignature
	}
	actorOrigin, _ := Origin(a.Actor)
	idOrigin, _ := Origin(a.ID)
	if actorOrigin != idOrigin {
		return out, ErrSignature
	}
	params, e := signatureParams(r.Header.Get("Signature"))
	if e != nil {
		return out, e
	}
	canonical, e := signingString(r, params["headers"])
	if e != nil {
		return out, e
	}
	stamp, e := http.ParseTime(r.Header.Get("Date"))
	if e != nil {
		return out, ErrSignature
	}
	skew := c.Clock().Sub(stamp)
	if skew > 5*time.Minute || skew < -5*time.Minute {
		return out, ErrSignature
	}
	sum := sha256.Sum256(body)
	want := "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(r.Header.Get("Digest"))) != 1 {
		return out, ErrSignature
	}
	k, e := keys.RemoteKey(ctx, params["keyId"])
	if e != nil {
		return out, ErrSignature
	}
	k = snapshotRemote(k)
	if !c.ValidRemote(k) || k.KeyID != params["keyId"] || k.Actor != a.Actor {
		return out, ErrSignature
	}
	signed, e := base64.StdEncoding.DecodeString(params["signature"])
	if e != nil {
		return out, ErrSignature
	}
	hash := sha256.Sum256([]byte(canonical))
	if rsa.VerifyPKCS1v15(k.PublicKey, crypto.SHA256, hash[:], signed) != nil {
		return out, ErrSignature
	}
	return Verified{activity: a, remote: k, digest: sum, target: c.Origin + r.URL.Path, valid: true, signedAt: stamp}, nil
}
func Sign(r *http.Request, body []byte, keyID string, signer crypto.Signer, now time.Time) error {
	if _, e := PEM(signer); e != nil {
		return e
	}
	key, e := ParseURL(keyID)
	if e != nil || key.Fragment == "" {
		return ErrInvalid
	}
	if r.Method != "POST" || r.URL == nil {
		return ErrInvalid
	}
	r.Host = r.URL.Host
	r.Header.Set("Date", now.UTC().Format(http.TimeFormat))
	r.Header.Set("Content-Type", MediaType)
	digest := sha256.Sum256(body)
	r.Header.Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(digest[:]))
	headers := "(request-target) host date digest content-type"
	canonical, e := signingString(r, headers)
	if e != nil {
		return e
	}
	sum := sha256.Sum256([]byte(canonical))
	signed, e := signer.Sign(rand.Reader, sum[:], crypto.SHA256)
	if e != nil {
		return ErrUnavailable
	}
	r.Header.Set("Signature", fmt.Sprintf(`keyId="%s",algorithm="rsa-sha256",headers="%s",signature="%s"`, keyID, headers, base64.StdEncoding.EncodeToString(signed)))
	return nil
}
