// SPDX-License-Identifier: AGPL-3.0-only
package signin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const passwordRounds = 600000

func random(n int) ([]byte, error) { b := make([]byte, n); _, e := rand.Read(b); return b, e }
func newID() (string, error) {
	b, e := random(16)
	if e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func newSecret() (string, error) {
	b, e := random(32)
	return base64.RawURLEncoding.EncodeToString(b), e
}
func digest(v string) []byte { b := sha256.Sum256([]byte(v)); return b[:] }
func validToken(s string) bool {
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}
func validPassword(s string) bool { return len(s) >= 12 && len(s) <= 1024 }
func hashPassword(password string) ([]byte, error) {
	if !validPassword(password) {
		return nil, ErrInput
	}
	salt, e := random(16)
	if e != nil {
		return nil, e
	}
	h, e := pbkdf2.Key(sha256.New, password, salt, passwordRounds, 32)
	if e != nil {
		return nil, e
	}
	return []byte(fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordRounds, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h))), nil
}
func checkPassword(encoded []byte, password string) bool {
	if len(password) > 1024 {
		return false
	}
	p := strings.Split(string(encoded), "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" || p[1] != strconv.Itoa(passwordRounds) {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(p[2])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(p[3])
	if e != nil || len(want) != 32 {
		return false
	}
	got, e := pbkdf2.Key(sha256.New, password, salt, passwordRounds, 32)
	return e == nil && subtle.ConstantTimeCompare(got, want) == 1
}
func (s *Service) seal(secret []byte, account, factor string) ([]byte, error) {
	b, e := aes.NewCipher(s.cfg.SealKey)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	n, e := random(g.NonceSize())
	if e != nil {
		return nil, e
	}
	return g.Seal(n, n, secret, []byte("signin-totp:"+account+":"+factor)), nil
}
func (s *Service) open(secret []byte, account, factor string) ([]byte, error) {
	b, e := aes.NewCipher(s.cfg.SealKey)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	if len(secret) < g.NonceSize()+g.Overhead() {
		return nil, ErrInvalid
	}
	return g.Open(nil, secret[:g.NonceSize()], secret[g.NonceSize():], []byte("signin-totp:"+account+":"+factor))
}
func totp(secret []byte, counter int64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(counter))
	h := hmac.New(sha1.New, secret)
	_, _ = h.Write(b[:])
	v := h.Sum(nil)
	o := v[len(v)-1] & 15
	n := binary.BigEndian.Uint32(v[o:o+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1000000)
}
func checkTOTP(secret []byte, code string, now time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	base := now.Unix() / 30
	for _, d := range []int64{0, -1, 1} {
		c := base + d
		if c > last && subtle.ConstantTimeCompare([]byte(totp(secret, c)), []byte(code)) == 1 {
			return c, true
		}
	}
	return 0, false
}
func totpSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}
