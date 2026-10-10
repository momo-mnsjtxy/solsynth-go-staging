// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/federation"
	"solsynth.local/go-migration/ports/media"
	"strings"
)

type mediaLocationSettings struct{ pools, remoteHosts, nfcKeyEnv, ffmpeg, limiter, geocoder, nfcConfig, nfcHelper, nfcLimiter *string }

func mediaLocationFlags(fs *flag.FlagSet) mediaLocationSettings {
	return mediaLocationSettings{
		fs.String("media-pools-config", "", "absolute strict JSON mapping of existing pool IDs to preprovisioned private filesystem roots"),
		fs.String("media-remote-hosts", "", "explicit comma-separated exact HTTPS remote upload hosts; empty disables fetching"),
		fs.String("nfc-key-encryption-env", "", "explicit environment name containing a base64 32-byte encrypted-tag wrapping key"),
		fs.String("media-renderer-ffmpeg", "", "explicit absolute ffmpeg executable enabling bounded AVIF thumbnails and live-photo validation"),
		fs.String("media-renderer-limiter", "", "explicit absolute Linux prlimit executable required with ffmpeg"),
		fs.String("location-geocoder-endpoint", "", "explicit HTTPS endpoint implementing the owned bounded geocoding provider contract"),
		fs.String("nfc-pcsc-config", "", "absolute private strict JSON binding tag record IDs, UIDs, readers and application key environment names"),
		fs.String("nfc-pcsc-helper", "", "absolute compiled scripts/nfc-pcsc-bridge.c executable"),
		fs.String("nfc-pcsc-limiter", "", "absolute Linux prlimit executable bounding physical NFC exchanges"),
	}
}

// Storage roots are opened and pinned without creating directories or seeding
// pool policy rows. The operator installs the owned schema and enrolls pools
// through the guarded pool administration API separately.
func (s mediaLocationSettings) apply(c *application.Config) (func(), error) {
	var roots []*mediastore.Filesystem
	var locker *locationpg.PCSCLocker
	close := func() {
		for _, root := range roots {
			root.Close()
		}
		clear(c.NFCKeyEncryptionKey)
		if locker != nil {
			locker.Close()
		}
	}
	invalid := errors.New("invalid or unavailable explicit media/location configuration")
	fail := func() (func(), error) { close(); return nil, invalid }
	if *s.geocoder != "" {
		provider, e := locationpg.NewHTTPSGeocoder(*s.geocoder)
		if e != nil {
			return fail()
		}
		c.LocationGeocoder = provider
	}
	if (*s.ffmpeg == "") != (*s.limiter == "") {
		return fail()
	}
	if *s.ffmpeg != "" {
		if c.MediaStore == nil {
			return fail()
		}
		renderer, e := mediapg.NewFFmpegRenderer(*s.ffmpeg, *s.limiter)
		if e != nil {
			return fail()
		}
		c.MediaRenderer = renderer
	}
	if *s.nfcKeyEnv != "" {
		key, e := base64.StdEncoding.Strict().DecodeString(os.Getenv(*s.nfcKeyEnv))
		if e != nil || len(key) != 32 {
			clear(key)
			return fail()
		}
		c.NFCKeyEncryptionKey = key
	}
	if *s.nfcConfig != "" || *s.nfcHelper != "" || *s.nfcLimiter != "" {
		if *s.nfcConfig == "" || *s.nfcHelper == "" || *s.nfcLimiter == "" || len(c.NFCKeyEncryptionKey) != 32 {
			return fail()
		}
		bindings, e := loadPCSCBindings(*s.nfcConfig)
		if e != nil {
			return fail()
		}
		locker, e = locationpg.NewPCSCLocker(*s.nfcHelper, *s.nfcLimiter, bindings)
		for _, b := range bindings {
			clear(b.ApplicationKey)
		}
		if e != nil {
			return fail()
		}
		c.NFCLocker = locker
	}
	if *s.remoteHosts != "" || *s.pools != "" {
		if c.MediaStore == nil {
			return fail()
		}
	}
	if *s.remoteHosts != "" {
		hosts := strings.Split(*s.remoteHosts, ",")
		for i := range hosts {
			hosts[i] = strings.TrimSpace(hosts[i])
		}
		fetcher, e := mediapg.NewHTTPSRemoteFetcher(hosts)
		if e != nil {
			return fail()
		}
		c.MediaRemoteFetcher = fetcher
	}
	if *s.pools == "" {
		return close, nil
	}
	path := *s.pools
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fail()
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return fail()
	}
	f, e := os.Open(path)
	if e != nil {
		return fail()
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return fail()
	}
	raw, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil || len(raw) > 64<<10 || federation.CheckJSON(raw) != nil {
		return fail()
	}
	var document struct {
		Version int `json:"version"`
		Pools   []struct {
			ID   string `json:"id"`
			Root string `json:"root"`
		} `json:"pools"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&document) != nil || document.Version != 1 || len(document.Pools) < 1 || len(document.Pools) > 100 {
		return fail()
	}
	c.MediaPools = map[string]media.ObjectStore{}
	for _, pool := range document.Pools {
		if !authority.ValidID(pool.ID) || pool.ID != strings.ToLower(pool.ID) || c.MediaPools[pool.ID] != nil {
			return fail()
		}
		root, e := mediastore.NewFilesystem(pool.Root, c.MaxMediaBytes)
		if e != nil {
			return fail()
		}
		roots = append(roots, root)
		c.MediaPools[pool.ID] = root
	}
	return close, nil
}

func loadPCSCBindings(path string) ([]locationpg.PCSCBinding, error) {
	invalid := errors.New("invalid NFC PCSC binding configuration")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, invalid
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return nil, invalid
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, invalid
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return nil, invalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil || len(raw) > 64<<10 || federation.CheckJSON(raw) != nil {
		return nil, invalid
	}
	var doc struct {
		Version int `json:"version"`
		Tags    []struct {
			RecordID        string `json:"record_id"`
			UID             string `json:"uid"`
			Reader          string `json:"reader"`
			KeyEnv          string `json:"application_key_env"`
			KeyNumber       int    `json:"key_number"`
			NDEFURITemplate string `json:"ndef_uri_template"`
		} `json:"tags"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || doc.Version != 1 || len(doc.Tags) < 1 || len(doc.Tags) > 100 {
		return nil, invalid
	}
	bindings := []locationpg.PCSCBinding{}
	success := false
	defer func() {
		if !success {
			for _, b := range bindings {
				clear(b.ApplicationKey)
			}
		}
	}()
	for _, v := range doc.Tags {
		if len(v.KeyEnv) < 1 || len(v.KeyEnv) > 128 || v.KeyNumber < 0 || v.KeyNumber > 4 {
			return nil, invalid
		}
		for i, c := range v.KeyEnv {
			if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
				return nil, invalid
			}
		}
		key, e := base64.StdEncoding.Strict().DecodeString(os.Getenv(v.KeyEnv))
		if e != nil || len(key) != 16 {
			clear(key)
			return nil, invalid
		}
		bindings = append(bindings, locationpg.PCSCBinding{RecordID: v.RecordID, UID: v.UID, Reader: v.Reader, NDEFURITemplate: v.NDEFURITemplate, ApplicationKey: key, KeyNumber: byte(v.KeyNumber)})
	}
	success = true
	return bindings, nil
}
