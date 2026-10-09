// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/ports/passport/passkit"
)

type passkitOptions struct {
	enabled *bool
	file    *string
}

func passkitFlags(fs *flag.FlagSet) passkitOptions {
	return passkitOptions{fs.Bool("enable-apple-wallet", false, "enable Apple Wallet member pass signing with explicit existing certificate and asset configuration"), fs.String("apple-wallet-config", "", "explicit JSON Apple Wallet configuration; no default key or certificate discovery")}
}

type passkitDocument struct {
	PassType     string            `json:"pass_type_identifier"`
	Team         string            `json:"team_identifier"`
	Organization string            `json:"organization_name"`
	Description  string            `json:"description"`
	LogoText     string            `json:"logo_text"`
	Foreground   string            `json:"foreground_color"`
	Background   string            `json:"background_color"`
	Label        string            `json:"label_color"`
	Terms        string            `json:"terms_text"`
	Barcode      string            `json:"barcode_alt_text"`
	WebService   string            `json:"web_service_url"`
	Site         string            `json:"site_url"`
	StoreID      int64             `json:"associated_store_identifier"`
	Certificate  string            `json:"signing_certificate_file"`
	Key          string            `json:"signing_key_file"`
	Intermediate string            `json:"wwdr_certificate_file"`
	Assets       map[string]string `json:"assets"`
	ProgramGroup string            `json:"program_group"`
}

func readPassFile(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, passkit.ErrUnavailable
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, passkit.ErrUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, passkit.ErrUnavailable
	}
	return b, nil
}
func passCertificate(path string) (*x509.Certificate, error) {
	b, e := readPassFile(path, 65536)
	if e != nil {
		return nil, e
	}
	if block, rest := pem.Decode(b); block != nil {
		if block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
			return nil, passkit.ErrUnavailable
		}
		b = block.Bytes
	}
	c, e := x509.ParseCertificate(b)
	if e != nil {
		return nil, passkit.ErrUnavailable
	}
	return c, nil
}
func (o passkitOptions) apply(c *application.Config) error {
	if !*o.enabled {
		if *o.file != "" {
			return errors.New("Apple Wallet configuration requires explicit enablement")
		}
		return nil
	}
	if *o.file == "" {
		return passkit.ErrUnavailable
	}
	b, e := readPassFile(*o.file, 65536)
	if e != nil {
		return e
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d passkitDocument
	if e = dec.Decode(&d); e != nil {
		return passkit.ErrUnavailable
	}
	var extra any
	if e = dec.Decode(&extra); e != io.EOF {
		return passkit.ErrUnavailable
	}
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(filepath.Dir(*o.file), p)
	}
	if d.Key == "" || d.Certificate == "" || d.Intermediate == "" || len(d.ProgramGroup) > 256 {
		return passkit.ErrUnavailable
	}
	info, e := os.Lstat(resolve(d.Key))
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return passkit.ErrUnavailable
	}
	key, e := issuerKey(resolve(d.Key))
	if e != nil {
		return passkit.ErrUnavailable
	}
	cert, e := passCertificate(resolve(d.Certificate))
	if e != nil {
		return e
	}
	intermediate, e := passCertificate(resolve(d.Intermediate))
	if e != nil {
		return e
	}
	signer, e := passkit.NewCMS(key, cert, intermediate, d.PassType, d.Team)
	if e != nil {
		return e
	}
	config := passkit.Config{OrganizationName: d.Organization, Description: d.Description, LogoText: d.LogoText, ForegroundColor: d.Foreground, BackgroundColor: d.Background, LabelColor: d.Label, TermsText: d.Terms, BarcodeAltText: d.Barcode, WebServiceURL: d.WebService, SiteURL: d.Site, AssociatedStoreIdentifier: d.StoreID, Signer: signer, Assets: map[string][]byte{}}
	if len(d.Assets) > 9 {
		return passkit.ErrUnavailable
	}
	for name, path := range d.Assets {
		b, e := readPassFile(resolve(path), 1<<20)
		if e != nil {
			return e
		}
		config.Assets[name] = b
	}
	if _, e = passkit.NewGenerator(config); e != nil {
		return e
	}
	c.AppleWallet = &config
	c.AppleWalletProgramGroup = d.ProgramGroup
	return nil
}
