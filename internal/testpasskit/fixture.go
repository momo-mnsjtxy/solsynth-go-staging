// SPDX-License-Identifier: AGPL-3.0-only
// Package testpasskit generates disposable synthetic certificates for tests.
// No credential, account, Apple certificate or persistent key is bundled.
package testpasskit

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/passport/passkit"
)

type Fixture struct {
	Config                          passkit.Config
	Key                             *rsa.PrivateKey
	Certificate, Intermediate, Root *x509.Certificate
}

func New(t testing.TB) Fixture {
	t.Helper()
	now := time.Now().UTC()
	key := func() *rsa.PrivateKey {
		k, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			t.Fatal(e)
		}
		return k
	}
	issue := func(template, parent *x509.Certificate, pub any, signer *rsa.PrivateKey) *x509.Certificate {
		raw, e := x509.CreateCertificate(rand.Reader, template, parent, pub, signer)
		if e != nil {
			t.Fatal(e)
		}
		c, e := x509.ParseCertificate(raw)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	rootKey := key()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic Pass Test Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	root := issue(rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	intermediateKey := key()
	intermediate := issue(&x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Synthetic Pass Test Intermediate"}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}, root, &intermediateKey.PublicKey, rootKey)
	leafKey := key()
	leaf := issue(&x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Synthetic Member Pass", OrganizationalUnit: []string{"SYNTHETIC1"}, ExtraNames: []pkix.AttributeTypeAndValue{{Type: asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}, Value: "pass.example.synthetic.member"}}}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}, intermediate, &leafKey.PublicKey, intermediateKey)
	signer, e := passkit.NewCMS(leafKey, leaf, intermediate, "pass.example.synthetic.member", "SYNTHETIC1")
	if e != nil {
		t.Fatal(e)
	}
	return Fixture{Config(t, signer), leafKey, leaf, intermediate, root}
}
func Config(t testing.TB, signer passkit.ManifestSigner) passkit.Config {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 58, 58))
	for y := 0; y < 58; y++ {
		for x := 0; x < 58; x++ {
			img.Set(x, y, color.NRGBA{R: 40, G: 100, B: 220, A: 255})
		}
	}
	var asset bytes.Buffer
	if e := png.Encode(&asset, img); e != nil {
		t.Fatal(e)
	}
	c := passkit.Config{OrganizationName: "Synthetic Test Organization", Description: "Synthetic membership card", LogoText: "Synthetic", SiteURL: "https://example.test", WebServiceURL: "https://example.test/api/passkit", TermsText: "Synthetic test pass. No admission or payment rights.", Signer: signer, Assets: map[string][]byte{"icon.png": asset.Bytes(), "icon@2x.png": asset.Bytes()}}
	return c
}
func PEM(c *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
}
