// SPDX-License-Identifier: AGPL-3.0-only
package passkit

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"sort"
	"time"
)

// CMS signs the Apple manifest as detached RFC 5652 SignedData. It does not
// request certificates, read credentials, generate keys or contact providers.
// The embedding host supplies its already-authorized signing key and chain.
type CMS struct {
	key                       *rsa.PrivateKey
	certificate, intermediate *x509.Certificate
	passType, team            string
}

var oidData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
var oidSigned = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
var oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
var oidRSA = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
var oidUID = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}

func NewCMS(key *rsa.PrivateKey, certificate, intermediate *x509.Certificate, passType, team string) (*CMS, error) {
	if key == nil || certificate == nil || intermediate == nil || key.N.BitLen() < 2048 || key.Validate() != nil || passType == "" || team == "" {
		return nil, ErrUnavailable
	}
	pub, ok := certificate.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.Cmp(key.N) != 0 || pub.E != key.E || certificate.IsCA || certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 || certificate.CheckSignatureFrom(intermediate) != nil {
		return nil, ErrUnavailable
	}
	uid, ou := "", false
	for _, n := range certificate.Subject.Names {
		if n.Type.Equal(oidUID) {
			uid, _ = n.Value.(string)
		}
	}
	for _, v := range certificate.Subject.OrganizationalUnit {
		ou = ou || v == team
	}
	if uid != passType || !ou {
		return nil, ErrUnavailable
	}
	return &CMS{key, certificate, intermediate, passType, team}, nil
}
func (s *CMS) Fingerprint() string {
	if s == nil {
		return ""
	}
	h := sha256.New()
	h.Write(s.certificate.Raw)
	h.Write(s.intermediate.Raw)
	return hex.EncodeToString(h.Sum(nil))
}
func (s *CMS) Identity() (string, string) {
	if s == nil {
		return "", ""
	}
	return s.passType, s.team
}

type cmsAttribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}
type cmsIssuer struct {
	Issuer asn1.RawValue
	Serial *big.Int
}
type cmsSigner struct {
	Version    int
	Issuer     cmsIssuer
	Digest     pkix.AlgorithmIdentifier
	Attributes asn1.RawValue
	Algorithm  pkix.AlgorithmIdentifier
	Signature  []byte
}
type cmsContent struct{ Type asn1.ObjectIdentifier }
type cmsSignedData struct {
	Version      int
	Digests      []pkix.AlgorithmIdentifier `asn1:"set"`
	Content      cmsContent
	Certificates asn1.RawValue
	Signers      []cmsSigner `asn1:"set"`
}
type cmsEnvelope struct {
	Type    asn1.ObjectIdentifier
	Content asn1.RawValue
}

func (s *CMS) SignManifest(ctx context.Context, manifest []byte, now time.Time) ([]byte, error) {
	if s == nil || len(manifest) == 0 || len(manifest) > 1<<20 {
		return nil, ErrUnavailable
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	for _, c := range []*x509.Certificate{s.certificate, s.intermediate} {
		if now.Before(c.NotBefore) || !now.Before(c.NotAfter) {
			return nil, ErrUnavailable
		}
	}
	digest := sha256.Sum256(manifest)
	var attrs [][]byte
	for _, a := range []struct {
		oid   asn1.ObjectIdentifier
		value any
	}{
		{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}, oidData},
		{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}, digest[:]},
		{asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}, now.UTC().Truncate(time.Second)},
	} {
		v, e := asn1.Marshal(a.value)
		if e != nil {
			return nil, e
		}
		b, e := asn1.Marshal(cmsAttribute{a.oid, []asn1.RawValue{{FullBytes: v}}})
		if e != nil {
			return nil, e
		}
		attrs = append(attrs, b)
	}
	sort.Slice(attrs, func(i, j int) bool { return bytes.Compare(attrs[i], attrs[j]) < 0 })
	attrBytes := bytes.Join(attrs, nil)
	encoded, e := asn1.Marshal(asn1.RawValue{Class: 0, Tag: 17, IsCompound: true, Bytes: attrBytes})
	if e != nil {
		return nil, e
	}
	hash := sha256.Sum256(encoded)
	signature, e := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, hash[:])
	if e != nil {
		return nil, e
	}
	algorithm := pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}
	certs := [][]byte{s.certificate.Raw, s.intermediate.Raw}
	sort.Slice(certs, func(i, j int) bool { return bytes.Compare(certs[i], certs[j]) < 0 })
	signed := cmsSignedData{1, []pkix.AlgorithmIdentifier{algorithm}, cmsContent{oidData}, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: bytes.Join(certs, nil)}, []cmsSigner{{1, cmsIssuer{asn1.RawValue{FullBytes: s.certificate.RawIssuer}, s.certificate.SerialNumber}, algorithm, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: attrBytes}, pkix.AlgorithmIdentifier{Algorithm: oidRSA, Parameters: asn1.NullRawValue}, signature}}}
	content, e := asn1.Marshal(signed)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return asn1.Marshal(cmsEnvelope{oidSigned, asn1.RawValue{Class: 2, Tag: 0, IsCompound: true, Bytes: content}})
}
