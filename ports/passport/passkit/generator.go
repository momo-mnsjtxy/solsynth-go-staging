// SPDX-License-Identifier: AGPL-3.0-only
package passkit

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1" // Apple Wallet manifest format requires SHA-1; CMS uses SHA-256.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

type Config struct {
	OrganizationName, Description, LogoText, ForegroundColor, BackgroundColor, LabelColor, TermsText, BarcodeAltText string
	WebServiceURL, SiteURL                                                                                           string
	AssociatedStoreIdentifier                                                                                        int64
	Assets                                                                                                           map[string][]byte
	Signer                                                                                                           ManifestSigner
	Now                                                                                                              func() time.Time
}
type Generator struct {
	cfg            Config
	passType, team string
}

var assetNames = map[string]bool{"icon.png": true, "icon@2x.png": true, "icon@3x.png": true, "logo.png": true, "logo@2x.png": true, "logo@3x.png": true, "thumbnail.png": true, "thumbnail@2x.png": true, "thumbnail@3x.png": true}

func NewGenerator(c Config) (*Generator, error) {
	if c.Signer == nil || c.OrganizationName == "" || c.Description == "" || len(c.OrganizationName) > 255 || len(c.Description) > 1024 || len(c.TermsText) > 16384 || c.AssociatedStoreIdentifier < 0 {
		return nil, ErrUnavailable
	}
	passType, team := c.Signer.Identity()
	if !strings.HasPrefix(passType, "pass.") || len(passType) > 255 || team == "" {
		return nil, ErrUnavailable
	}
	for _, v := range []string{c.WebServiceURL, c.SiteURL} {
		u, e := url.Parse(v)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, ErrUnavailable
		}
	}
	if strings.HasSuffix(strings.TrimRight(c.WebServiceURL, "/"), "/v1") {
		return nil, ErrUnavailable
	}
	c.WebServiceURL = strings.TrimRight(c.WebServiceURL, "/")
	if c.Now == nil {
		c.Now = time.Now
	}
	assets := map[string][]byte{}
	total := 0
	for n, b := range c.Assets {
		if !assetNames[n] || len(b) > 1<<20 {
			return nil, ErrUnavailable
		}
		cfg, e := png.DecodeConfig(bytes.NewReader(b))
		if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
			return nil, ErrUnavailable
		}
		if _, e = png.Decode(bytes.NewReader(b)); e != nil {
			return nil, ErrUnavailable
		}
		total += len(b)
		assets[n] = bytes.Clone(b)
	}
	if total > 8<<20 || len(assets["icon.png"]) == 0 || len(assets["icon@2x.png"]) == 0 {
		return nil, ErrUnavailable
	}
	c.Assets = assets
	if c.ForegroundColor == "" {
		c.ForegroundColor = "rgb(255, 255, 255)"
	}
	if c.BackgroundColor == "" {
		c.BackgroundColor = "rgb(91, 93, 148)"
	}
	if c.LabelColor == "" {
		c.LabelColor = "rgb(224, 242, 254)"
	}
	if c.BarcodeAltText == "" {
		c.BarcodeAltText = "MEMBER CODE"
	}
	return &Generator{c, passType, team}, nil
}
func (g *Generator) PassType() string { return g.passType }

type field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// Files returns only controlled archive names. Profile bytes come from the
// canonical owned media store, never an account-supplied URL or external fetch.
func (g *Generator) Files(m Member, token string) (map[string][]byte, string, error) {
	if g == nil || !authority.ValidID(m.ID) || len(token) != 48 || m.Name == "" || m.CreatedAt.IsZero() {
		return nil, "", ErrInvalid
	}
	if _, e := hex.DecodeString(token); e != nil {
		return nil, "", ErrInvalid
	}
	full := strings.TrimSpace(strings.Join(strings.Fields(m.FirstName+" "+m.MiddleName+" "+m.LastName), " "))
	if full == "" {
		full = m.Nick
	}
	if full == "" {
		full = m.Name
	}
	program := m.Program
	if program == "" {
		program = "Standard"
	}
	profile := strings.TrimRight(g.cfg.SiteURL, "/") + "/accounts/" + url.PathEscape(m.Name)
	barcode := map[string]string{"format": "PKBarcodeFormatQR", "message": profile, "messageEncoding": "iso-8859-1", "altText": g.cfg.BarcodeAltText}
	pass := map[string]any{"formatVersion": 1, "passTypeIdentifier": g.passType, "teamIdentifier": g.team, "serialNumber": m.ID, "organizationName": g.cfg.OrganizationName, "description": g.cfg.Description, "logoText": g.cfg.LogoText, "foregroundColor": g.cfg.ForegroundColor, "backgroundColor": g.cfg.BackgroundColor, "labelColor": g.cfg.LabelColor, "authenticationToken": token, "webServiceURL": g.cfg.WebServiceURL, "barcode": barcode, "barcodes": []any{barcode}, "userInfo": map[string]string{"id": m.ID}, "generic": map[string]any{
		"headerFields": []field{{"h1", "USERNAME", "@" + m.Name}}, "primaryFields": []field{{"p1", "NAME", full}}, "secondaryFields": []field{{"s1", "MEMBER SINCE", strconv.Itoa(m.CreatedAt.UTC().Year())}}, "auxiliaryFields": []field{{"a1", "STELLAR PROGRAM", program}}, "backFields": []field{{"b1", "Terms", g.cfg.TermsText}, {"b2", "Profile", profile}, {"b3", "Account ID", m.ID}}}}
	if g.cfg.AssociatedStoreIdentifier > 0 {
		pass["associatedStoreIdentifiers"] = []int64{g.cfg.AssociatedStoreIdentifier}
	}
	payload, e := json.Marshal(pass)
	if e != nil {
		return nil, "", e
	}
	files := map[string][]byte{"pass.json": payload}
	for n, b := range g.cfg.Assets {
		files[n] = bytes.Clone(b)
	}
	if len(m.Picture) > 0 && len(m.Picture) <= 4<<20 {
		cfg, _, e := image.DecodeConfig(bytes.NewReader(m.Picture))
		if e == nil && cfg.Width > 0 && cfg.Height > 0 && int64(cfg.Width)*int64(cfg.Height) <= 12_000_000 {
			if img, _, e := image.Decode(bytes.NewReader(m.Picture)); e == nil {
				for n, size := range map[string]int{"icon.png": 29, "icon@2x.png": 58, "icon@3x.png": 87, "thumbnail.png": 90, "thumbnail@2x.png": 180, "thumbnail@3x.png": 270} {
					b := image.NewNRGBA(image.Rect(0, 0, size, size))
					bounds := img.Bounds()
					for y := 0; y < size; y++ {
						for x := 0; x < size; x++ {
							b.Set(x, y, img.At(bounds.Min.X+x*bounds.Dx()/size, bounds.Min.Y+y*bounds.Dy()/size))
						}
					}
					var encoded bytes.Buffer
					if e = png.Encode(&encoded, b); e != nil {
						return nil, "", e
					}
					files[n] = encoded.Bytes()
				}
			}
		}
	}
	manifest := map[string]string{}
	for n, b := range files {
		h := sha1.Sum(b)
		manifest[n] = hex.EncodeToString(h[:])
	}
	data, e := json.Marshal(manifest)
	if e != nil {
		return nil, "", e
	}
	files["manifest.json"] = data
	// Update versions hash the full source files with SHA-256 independently
	// of Apple's mandatory SHA-1 manifest, and notice signer-chain rotation.
	versions := map[string]string{"signing_certificate_chain": g.cfg.Signer.Fingerprint()}
	for name, b := range files {
		h := sha256.Sum256(b)
		versions[name] = hex.EncodeToString(h[:])
	}
	versionBytes, e := json.Marshal(versions)
	if e != nil {
		return nil, "", e
	}
	hash := sha256.Sum256(versionBytes)
	return files, hex.EncodeToString(hash[:]), nil
}
func (g *Generator) Generate(ctx context.Context, files map[string][]byte) ([]byte, error) {
	if g == nil {
		return nil, ErrUnavailable
	}
	if len(files) < 4 || len(files) > 11 || len(files["pass.json"]) == 0 || len(files["icon.png"]) == 0 || len(files["icon@2x.png"]) == 0 {
		return nil, ErrInvalid
	}
	var manifest map[string]string
	if e := json.Unmarshal(files["manifest.json"], &manifest); e != nil || len(manifest) != len(files)-1 {
		return nil, ErrInvalid
	}
	total := 0
	for name, data := range files {
		if name != "pass.json" && name != "manifest.json" && !assetNames[name] || len(data) > 4<<20 {
			return nil, ErrInvalid
		}
		total += len(data)
		if name != "manifest.json" {
			h := sha1.Sum(data)
			if manifest[name] != hex.EncodeToString(h[:]) {
				return nil, ErrInvalid
			}
		}
	}
	if total > 16<<20 {
		return nil, ErrInvalid
	}
	signature, e := g.cfg.Signer.SignManifest(ctx, files["manifest.json"], g.cfg.Now())
	if e != nil {
		return nil, e
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range append(names, "signature") {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		b := files[name]
		if name == "signature" {
			b = signature
		}
		f, e := archive.Create(name)
		if e != nil {
			return nil, e
		}
		if _, e = f.Write(b); e != nil {
			return nil, e
		}
	}
	if e = archive.Close(); e != nil {
		return nil, e
	}
	return output.Bytes(), nil
}
