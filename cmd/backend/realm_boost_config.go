// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/realmboostpg"
)

type realmBoostSettings struct {
	enabled *bool
	file    *string
}

func realmBoostFlags(fs *flag.FlagSet) realmBoostSettings {
	return realmBoostSettings{
		fs.Bool("enable-realm-boosts", false, "enable explicitly configured canonical realm boost checkout"),
		fs.String("realm-boost-config", "", "absolute JSON policy file with existing treasury IDs, four label caps and explicit perk mappings"),
	}
}

var errRealmBoostConfig = errors.New("invalid or unavailable explicit realm boost configuration")

// A token walk retains exact field spelling and rejects duplicate decoded keys.
// Both the outer document and the identifier map use this same object parser.
func realmBoostObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errRealmBoostConfig
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, e := d.Token()
	if e != nil || first != json.Delim('{') {
		return nil, errRealmBoostConfig
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		token, e := d.Token()
		if e != nil {
			return nil, errRealmBoostConfig
		}
		key, ok := token.(string)
		if !ok || out[key] != nil {
			return nil, errRealmBoostConfig
		}
		var value json.RawMessage
		if e = d.Decode(&value); e != nil {
			return nil, errRealmBoostConfig
		}
		out[key] = value
	}
	last, e := d.Token()
	if e != nil || last != json.Delim('}') {
		return nil, errRealmBoostConfig
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errRealmBoostConfig
	}
	return out, nil
}

func (s realmBoostSettings) apply(c *application.Config) error {
	if !*s.enabled {
		if *s.file != "" {
			return errRealmBoostConfig
		}
		return nil
	}
	name := *s.file
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return errRealmBoostConfig
	}
	info, e := os.Lstat(name)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errRealmBoostConfig
	}
	f, e := os.Open(name)
	if e != nil {
		return errRealmBoostConfig
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return errRealmBoostConfig
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return errRealmBoostConfig
	}
	fields, e := realmBoostObject(raw)
	if e != nil {
		return errRealmBoostConfig
	}
	for key := range fields {
		switch key {
		case "version", "treasury_account_id", "treasury_wallet_id", "label_caps", "perk_levels", "order_ttl_seconds":
		default:
			return errRealmBoostConfig
		}
	}
	var version int
	var caps []*int
	cfg := realmboostpg.Config{Policy: &realmboostpg.OwnedPolicy{PerkLevels: map[string]int{}}, OrderTTL: time.Hour}
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 ||
		json.Unmarshal(fields["treasury_account_id"], &cfg.TreasuryAccountID) != nil ||
		json.Unmarshal(fields["treasury_wallet_id"], &cfg.TreasuryWalletID) != nil ||
		json.Unmarshal(fields["label_caps"], &caps) != nil || len(caps) != 4 {
		return errRealmBoostConfig
	}
	for i, n := range caps {
		if n == nil {
			return errRealmBoostConfig
		}
		cfg.Policy.LabelCaps[i] = *n
	}
	perks, e := realmBoostObject(fields["perk_levels"])
	if e != nil || len(perks) > 1000 {
		return errRealmBoostConfig
	}
	for id, raw := range perks {
		var level int
		if json.Unmarshal(raw, &level) != nil {
			return errRealmBoostConfig
		}
		cfg.Policy.PerkLevels[id] = level
	}
	if raw, ok := fields["order_ttl_seconds"]; ok {
		var seconds int64
		if json.Unmarshal(raw, &seconds) != nil || seconds < 60 || seconds > 86400 {
			return errRealmBoostConfig
		}
		cfg.OrderTTL = time.Duration(seconds) * time.Second
	}
	if realmboostpg.ValidateConfig(cfg) != nil {
		return errRealmBoostConfig
	}
	c.RealmBoosts = &cfg
	return nil
}
