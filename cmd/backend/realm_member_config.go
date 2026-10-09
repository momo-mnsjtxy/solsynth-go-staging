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
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/realmmemberpg"
)

type realmMemberSettings struct {
	enabled *bool
	file    *string
}

func realmMemberFlags(fs *flag.FlagSet) realmMemberSettings {
	return realmMemberSettings{fs.Bool("enable-realm-members", false, "enable canonical realm labels and explicitly configured scoped progression"), fs.String("realm-member-config", "", "absolute JSON file with explicit level thresholds and tenure policy")}
}

var errRealmMemberConfig = errors.New("invalid or unavailable explicit realm member configuration")

func (s realmMemberSettings) apply(c *application.Config) error {
	if !*s.enabled {
		if *s.file != "" {
			return errRealmMemberConfig
		}
		return nil
	}
	if c.RealmBoosts == nil {
		return errRealmMemberConfig
	}
	name := *s.file
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return errRealmMemberConfig
	}
	info, e := os.Lstat(name)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errRealmMemberConfig
	}
	f, e := os.Open(name)
	if e != nil {
		return errRealmMemberConfig
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return errRealmMemberConfig
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return errRealmMemberConfig
	}
	fields, e := realmBoostObject(raw)
	if e != nil {
		return errRealmMemberConfig
	}
	for k, v := range fields {
		if k != "version" && k != "level_thresholds" && k != "enable_daily_tenure" || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errRealmMemberConfig
		}
	}
	var version int
	var levels []*int64
	cfg := realmmemberpg.Config{}
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["level_thresholds"], &levels) != nil || levels == nil || json.Unmarshal(fields["enable_daily_tenure"], &cfg.EnableDailyTenure) != nil {
		return errRealmMemberConfig
	}
	cfg.LevelThresholds = []int64{}
	for _, n := range levels {
		if n == nil {
			return errRealmMemberConfig
		}
		cfg.LevelThresholds = append(cfg.LevelThresholds, *n)
	}
	if realmmemberpg.ValidateConfig(cfg) != nil {
		return errRealmMemberConfig
	}
	c.RealmMembers = &cfg
	return nil
}
