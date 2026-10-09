// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/hex"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
)

// AES-CMAC follows NIST SP 800-38B. SUN session derivation and odd-byte
// truncation follow NXP AN12196 rev 2.0 sections 3.3 and 3.4.4.2.
func aesCMAC(key, msg []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	l := make([]byte, 16)
	b.Encrypt(l, l)
	double := func(v []byte) []byte {
		out := make([]byte, 16)
		carry := byte(0)
		for i := 15; i >= 0; i-- {
			out[i] = v[i]<<1 | carry
			carry = v[i] >> 7
		}
		if carry != 0 {
			out[15] ^= 0x87
		}
		return out
	}
	k1 := double(l)
	k2 := double(k1)
	n := (len(msg) + 15) / 16
	if n == 0 {
		n = 1
	}
	last := make([]byte, 16)
	off := (n - 1) * 16
	copy(last, msg[off:])
	sub := k1
	if len(msg) == 0 || len(msg)%16 != 0 {
		last[len(msg)-off] = 0x80
		sub = k2
	}
	for i := range last {
		last[i] ^= sub[i]
	}
	state := make([]byte, 16)
	for i := 0; i < n-1; i++ {
		for j := range state {
			state[j] ^= msg[i*16+j]
		}
		b.Encrypt(state, state)
	}
	for i := range state {
		state[i] ^= last[i]
	}
	b.Encrypt(state, state)
	return state, nil
}
func verifySUN(metaKey, fileKey []byte, uid, picc, encrypted, cmac, profile string) (int, error) {
	if len(metaKey) != 16 || len(fileKey) != 16 {
		return 0, locations.ErrUnavailable
	}
	p, e := hex.DecodeString(picc)
	if e != nil || len(p) != 16 {
		return 0, locations.ErrInvalid
	}
	mac, e := hex.DecodeString(cmac)
	if e != nil || len(mac) != 8 {
		return 0, locations.ErrInvalid
	}
	b, e := aes.NewCipher(metaKey)
	if e != nil {
		return 0, e
	}
	plain := make([]byte, 16)
	cipher.NewCBCDecrypter(b, make([]byte, 16)).CryptBlocks(plain, p)
	if plain[0] != 0xc7 || !strings.EqualFold(hex.EncodeToString(plain[1:8]), uid) {
		return 0, locations.ErrNotFound
	}
	sv := append([]byte{0x3c, 0xc3, 0, 1, 0, 0x80}, plain[1:11]...)
	session, e := aesCMAC(fileKey, sv)
	if e != nil {
		return 0, e
	}
	var input []byte
	switch profile {
	case "empty":
		if encrypted != "" {
			return 0, locations.ErrInvalid
		}
	case "encrypted-file":
		enc, e := hex.DecodeString(encrypted)
		if e != nil || len(enc) == 0 || len(enc) > 1024 || len(enc)%16 != 0 || encrypted != strings.ToUpper(encrypted) {
			return 0, locations.ErrInvalid
		}
		input = []byte(encrypted + "&cmac=")
	default:
		return 0, locations.ErrUnavailable
	}
	full, e := aesCMAC(session, input)
	if e != nil {
		return 0, e
	}
	truncated := make([]byte, 8)
	for i := range truncated {
		truncated[i] = full[i*2+1]
	}
	if subtle.ConstantTimeCompare(truncated, mac) != 1 {
		return 0, locations.ErrNotFound
	}
	return int(plain[8]) | int(plain[9])<<8 | int(plain[10])<<16, nil
}
