// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"strings"
)

func issuerKey(path string) (*rsa.PrivateKey, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("explicit issuer key unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(b) > 65536 {
		return nil, errors.New("invalid issuer key")
	}
	block, rest := pem.Decode(b)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("one issuer private key PEM required")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, e = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var value any
		value, e = x509.ParsePKCS8PrivateKey(block.Bytes)
		if e == nil {
			var ok bool
			key, ok = value.(*rsa.PrivateKey)
			if !ok {
				e = errors.New("RSA issuer key required")
			}
		}
	default:
		e = errors.New("RSA private key PEM required; certificates are not accepted")
	}
	if e != nil || key == nil || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.Validate() != nil {
		return nil, errors.New("invalid RSA issuer key")
	}
	return key, nil
}
func issuerSecret(name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("explicit issuer secret environment name required")
	}
	b, e := base64.StdEncoding.DecodeString(os.Getenv(name))
	if e != nil || len(b) != 32 {
		return nil, errors.New("issuer secret must encode exactly32 bytes")
	}
	return b, nil
}
