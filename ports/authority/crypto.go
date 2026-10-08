// SPDX-License-Identifier: AGPL-3.0-only
package authority

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
	"time"
)

type Config struct {
	Issuer, Audience, SigningKeyID      string
	SigningKey                          *rsa.PrivateKey
	TrustedKeys                         map[string]*rsa.PublicKey
	RefreshHashKey, ReplaySealKey       []byte
	AccessTTL, SessionTTL, RefreshGrace time.Duration
	Now                                 func() time.Time
}
type Service struct {
	store  Store
	reader ReadStore
	cfg    Config
	seal   cipher.AEAD
}
type claims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Subject   string `json:"sub"`
	ID        string `json:"jti"`
	SessionID string `json:"sid"`
	Use       string `json:"token_use"`
	Version   int64  `json:"ver"`
	Epoch     int64  `json:"epoch"`
	Expires   int64  `json:"exp"`
	Issued    int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
}

func New(store Store, c Config) (*Service, error) {
	if store == nil || c.Issuer == "" || c.Audience == "" || c.SigningKeyID == "" || c.SigningKey == nil || c.SigningKey.N == nil || c.SigningKey.N.BitLen() < 2048 || len(c.RefreshHashKey) < 32 || len(c.ReplaySealKey) != 32 {
		return nil, errors.New("authority: store, issuer, audience, RSA-2048+ signing key, key ID and independent hash/seal keys required")
	}
	if err := c.SigningKey.Validate(); err != nil {
		return nil, err
	}
	if hmac.Equal(c.RefreshHashKey, c.ReplaySealKey) {
		return nil, errors.New("authority: hash and seal keys must differ")
	}
	keys := map[string]*rsa.PublicKey{}
	for id, k := range c.TrustedKeys {
		if id == "" || k == nil || k.N == nil || k.N.BitLen() < 2048 || k.E < 65537 {
			return nil, errors.New("authority: invalid trusted RSA key")
		}
		keys[id] = &rsa.PublicKey{N: new(big.Int).Set(k.N), E: k.E}
	}
	k, ok := keys[c.SigningKeyID]
	if !ok || k.N.Cmp(c.SigningKey.N) != 0 || k.E != c.SigningKey.E {
		return nil, errors.New("authority: signing key must be explicitly trusted")
	}
	c.TrustedKeys = keys
	c.RefreshHashKey = append([]byte(nil), c.RefreshHashKey...)
	c.ReplaySealKey = append([]byte(nil), c.ReplaySealKey...)
	if c.AccessTTL == 0 {
		c.AccessTTL = 10 * time.Minute
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 30 * 24 * time.Hour
	}
	if c.AccessTTL%time.Second != 0 || c.SessionTTL%time.Second != 0 || c.AccessTTL < time.Second || c.AccessTTL > time.Hour || c.SessionTTL < c.AccessTTL || c.RefreshGrace < 0 || c.RefreshGrace > 60*time.Second {
		return nil, errors.New("authority: invalid token lifetime or grace")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	b, err := aes.NewCipher(c.ReplaySealKey)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, cfg: c, seal: seal}, nil
}
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func secret() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b), err
}
func (s *Service) digest(raw string) []byte {
	h := hmac.New(sha256.New, s.cfg.RefreshHashKey)
	h.Write([]byte(raw))
	return h.Sum(nil)
}
func validSecret(raw string) bool {
	b, e := base64.RawURLEncoding.DecodeString(raw)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == raw
}

// Reject duplicate JSON keys, including nested claims, before struct decoding.
func uniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return ErrInvalid
				}
				seen[k] = true
				if e = walk(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(); e != nil {
					return e
				}
			}
		default:
			return ErrInvalid
		}
		_, e = d.Token()
		return e
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrInvalid
	}
	return nil
}
func decodeStrict(data []byte, v any) error {
	if uniqueJSON(data) != nil {
		return ErrInvalid
	}
	// encoding/json otherwise accepts case-insensitive aliases of tagged fields.
	// The owned JWT/request contract accepts exact spellings only.
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Pointer || typ.Elem().Kind() != reflect.Struct {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	typ = typ.Elem()
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			allowed[tag] = true
		}
	}
	for name := range fields {
		if !allowed[name] {
			return ErrInvalid
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func (s *Service) sign(a Account, se Session, now time.Time) (string, error) {
	h, _ := json.Marshal(struct {
		Alg  string `json:"alg"`
		Type string `json:"typ"`
		Kid  string `json:"kid"`
	}{"RS256", "JWT", s.cfg.SigningKeyID})
	exp := now.Add(s.cfg.AccessTTL)
	if se.ExpiresAt.Before(exp) {
		exp = se.ExpiresAt
	}
	c := claims{s.cfg.Issuer, s.cfg.Audience, a.ID, se.ID, se.ID, "user", a.Version, se.Epoch, exp.Unix(), now.Unix(), now.Unix()}
	p, _ := json.Marshal(c)
	b := base64.RawURLEncoding
	raw := b.EncodeToString(h) + "." + b.EncodeToString(p)
	sum := sha256.Sum256([]byte(raw))
	sig, e := rsa.SignPKCS1v15(rand.Reader, s.cfg.SigningKey, crypto.SHA256, sum[:])
	if e != nil {
		return "", e
	}
	return raw + "." + b.EncodeToString(sig), nil
}
func (s *Service) verify(token string) (claims, error) {
	var c claims
	if len(token) > 8192 {
		return c, ErrInvalid
	}
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return c, ErrInvalid
	}
	dec := func(raw string) ([]byte, error) {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || base64.RawURLEncoding.EncodeToString(b) != raw {
			return nil, ErrInvalid
		}
		return b, nil
	}
	h, e := dec(p[0])
	if e != nil {
		return c, ErrInvalid
	}
	var hdr struct {
		Alg  string `json:"alg"`
		Type string `json:"typ"`
		Kid  string `json:"kid"`
	}
	if decodeStrict(h, &hdr) != nil || hdr.Alg != "RS256" || hdr.Type != "JWT" {
		return c, ErrInvalid
	}
	k := s.cfg.TrustedKeys[hdr.Kid]
	if k == nil {
		return c, ErrInvalid
	}
	sig, e := dec(p[2])
	if e != nil {
		return c, ErrInvalid
	}
	hash := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if rsa.VerifyPKCS1v15(k, crypto.SHA256, hash[:], sig) != nil {
		return c, ErrInvalid
	}
	payload, e := dec(p[1])
	if e != nil || decodeStrict(payload, &c) != nil {
		return c, ErrInvalid
	}
	if c.Issuer != s.cfg.Issuer || c.Audience != s.cfg.Audience || !ValidID(c.Subject) || !ValidID(c.SessionID) || c.ID != c.SessionID || c.Use != "user" || c.Version < 1 || c.Epoch < 1 || c.Issued <= 0 || c.NotBefore < c.Issued || c.Expires <= c.NotBefore || c.Expires-c.Issued > int64(s.cfg.AccessTTL/time.Second) {
		return c, ErrInvalid
	}
	return c, nil
}
func (s *Service) sealPair(pair Pair, session string) ([]byte, error) {
	raw, e := json.Marshal(pair)
	if e != nil {
		return nil, e
	}
	n := make([]byte, s.seal.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	return s.seal.Seal(n, n, raw, []byte(session)), nil
}
func (s *Service) openPair(raw []byte, session string) (Pair, error) {
	var p Pair
	n := s.seal.NonceSize()
	if len(raw) < n {
		return p, ErrUnavailable
	}
	plain, e := s.seal.Open(nil, raw[:n], raw[n:], []byte(session))
	if e != nil {
		return p, ErrUnavailable
	}
	e = json.Unmarshal(plain, &p)
	return p, e
}
