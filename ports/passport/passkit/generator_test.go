// SPDX-License-Identifier: AGPL-3.0-only
package passkit_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/testpasskit"
	"solsynth.local/go-migration/ports/passport/passkit"
)

func member() passkit.Member {
	return passkit.Member{ID: "71000000-0000-4000-8000-000000000001", Name: "synthetic_member", Nick: "Fallback", FirstName: "Synthetic", LastName: "Member", CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func TestSignedPassManifestAndIndependentOpenSSL(t *testing.T) {
	f := testpasskit.New(t)
	g, e := passkit.NewGenerator(f.Config)
	if e != nil {
		t.Fatal(e)
	}
	files, fingerprint, e := g.Files(member(), strings.Repeat("a", 48))
	if e != nil {
		t.Fatal(e)
	}
	data, e := g.Generate(context.Background(), files)
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		t.Fatal(e)
	}
	got := map[string][]byte{}
	for _, f := range z.File {
		if _, ok := got[f.Name]; ok {
			t.Fatal("duplicate entry")
		}
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		got[f.Name] = b
	}
	if len(got) != 5 {
		t.Fatal("unexpected files", len(got))
	}
	var manifest map[string]string
	if e = json.Unmarshal(got["manifest.json"], &manifest); e != nil {
		t.Fatal(e)
	}
	for name, digest := range manifest {
		h := sha1.Sum(got[name])
		if hex.EncodeToString(h[:]) != digest {
			t.Fatal("wrong manifest", name)
		}
	}
	var p map[string]any
	if e = json.Unmarshal(got["pass.json"], &p); e != nil {
		t.Fatal(e)
	}
	if p["authenticationToken"] != strings.Repeat("a", 48) || p["serialNumber"] != member().ID || p["webServiceURL"] != f.Config.WebServiceURL {
		t.Fatal("pass fields")
	}
	if strings.Contains(string(got["pass.json"]), "private_key") {
		t.Fatal("key leaked")
	}
	_, same, e := g.Files(member(), strings.Repeat("a", 48))
	if e != nil || same != fingerprint {
		t.Fatal("unstable fingerprint")
	}
	renewed := testpasskit.New(t)
	renewedGenerator, e := passkit.NewGenerator(renewed.Config)
	if e != nil {
		t.Fatal(e)
	}
	_, renewedFingerprint, e := renewedGenerator.Files(member(), strings.Repeat("a", 48))
	if e != nil || renewedFingerprint == fingerprint {
		t.Fatal("signer rotation did not change pass version")
	}
	corrupt := map[string][]byte{}
	for name, data := range files {
		corrupt[name] = bytes.Clone(data)
	}
	corrupt["pass.json"] = append(corrupt["pass.json"], byte(' '))
	if _, e = g.Generate(context.Background(), corrupt); e == nil {
		t.Fatal("inconsistent manifest signed")
	}
	changed := member()
	changed.FirstName = "Changed"
	_, different, e := g.Files(changed, strings.Repeat("a", 48))
	if e != nil || different == fingerprint {
		t.Fatal("profile update not reflected")
	}
	// An independent, installed OpenSSL process verifies the detached CMS,
	// signed attributes, digest and certificate chain, then rejects tampering.
	openssl, e := exec.LookPath("openssl")
	if e != nil {
		t.Fatal("OpenSSL is required for independent CMS verification", e)
	}
	dir := t.TempDir()
	write := func(name string, b []byte) {
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("signature.der", got["signature"])
	write("manifest.json", got["manifest.json"])
	write("root.pem", testpasskit.PEM(f.Root))
	verify := func() ([]byte, error) {
		cmd := exec.Command(openssl, "cms", "-verify", "-binary", "-inform", "DER", "-in", filepath.Join(dir, "signature.der"), "-content", filepath.Join(dir, "manifest.json"), "-CAfile", filepath.Join(dir, "root.pem"), "-purpose", "any", "-out", filepath.Join(dir, "verified.json"))
		return cmd.CombinedOutput()
	}
	if output, e := verify(); e != nil {
		t.Fatalf("independent CMS verify: %v %s", e, output)
	}
	write("manifest.json", append(got["manifest.json"], byte(' ')))
	if _, e = verify(); e == nil {
		t.Fatal("tampered manifest accepted")
	}
	print := exec.Command(openssl, "cms", "-cmsout", "-print", "-inform", "DER", "-in", filepath.Join(dir, "signature.der"))
	output, e := print.CombinedOutput()
	if e != nil || !bytes.Contains(output, []byte("signingTime")) {
		t.Fatal("signing-time attribute missing", e)
	}
}
func TestPassSigningConfigurationAndExpiredCertificate(t *testing.T) {
	f := testpasskit.New(t)
	if _, e := passkit.NewCMS(f.Key, f.Certificate, f.Intermediate, "pass.example.other", "SYNTHETIC1"); e == nil {
		t.Fatal("certificate identity mismatch")
	}
	if _, e := passkit.NewCMS(f.Key, f.Certificate, f.Root, "pass.example.synthetic.member", "SYNTHETIC1"); e == nil {
		t.Fatal("wrong chain")
	}
	for _, mutate := range []func(*passkit.Config){func(c *passkit.Config) { c.Signer = nil }, func(c *passkit.Config) { c.WebServiceURL = "http://example.test" }, func(c *passkit.Config) { c.WebServiceURL = "https://example.test/api/passkit/v1" }, func(c *passkit.Config) { c.Assets = map[string][]byte{"../key": []byte("bad")} }, func(c *passkit.Config) {
		c.Assets = map[string][]byte{"icon.png": []byte("bad"), "icon@2x.png": []byte("bad")}
	}} {
		c := f.Config
		mutate(&c)
		if _, e := passkit.NewGenerator(c); e == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	c := f.Config
	c.Now = func() time.Time { return f.Certificate.NotAfter.Add(time.Second) }
	g, e := passkit.NewGenerator(c)
	if e != nil {
		t.Fatal(e)
	}
	files, _, e := g.Files(member(), strings.Repeat("a", 48))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = g.Generate(context.Background(), files); e == nil {
		t.Fatal("expired signer accepted")
	}
	m := member()
	m.Picture = f.Config.Assets["icon.png"]
	files, _, e = g.Files(m, strings.Repeat("a", 48))
	if e != nil || len(files["thumbnail@3x.png"]) == 0 {
		t.Fatal("canonical profile image was not projected", e)
	}
}
