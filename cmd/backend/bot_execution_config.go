// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/botdispatch"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/sphere/reader"
	"time"
)

var errBotExecutionConfig = errors.New("invalid or unavailable explicit bot execution configuration")

type botExecutionSettings struct {
	enabled *bool
	file    *string
}

func botExecutionFlags(fs *flag.FlagSet) botExecutionSettings {
	return botExecutionSettings{fs.Bool("enable-bot-execution", false, "enable explicitly enrolled ordinary-group bot delivery"), fs.String("bot-execution-config", "", "absolute strict JSON file referencing existing bot IDs, exact HTTPS origins and signing-key files")}
}
func botConfigFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errBotExecutionConfig
	}
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errBotExecutionConfig
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errBotExecutionConfig
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return nil, errBotExecutionConfig
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || int64(len(b)) > limit {
		return nil, errBotExecutionConfig
	}
	return b, nil
}
func botFields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	m, e := realmBoostObject(raw)
	if e != nil || len(m) != len(names) {
		return nil, errBotExecutionConfig
	}
	for _, name := range names {
		if len(m[name]) == 0 || string(m[name]) == "null" {
			return nil, errBotExecutionConfig
		}
	}
	return m, nil
}
func (s botExecutionSettings) apply(c *application.Config) error {
	if !*s.enabled {
		if *s.file != "" {
			return errBotExecutionConfig
		}
		return nil
	}
	raw, e := botConfigFile(*s.file, 65536)
	if e != nil {
		return errBotExecutionConfig
	}
	f, e := botFields(raw, "version", "allowed_origins", "send_timeout_ms", "event_lifetime_seconds", "max_attempts", "max_pending_per_room", "adapters")
	if e != nil {
		return e
	}
	var version, timeout, lifetime int
	var origins []string
	var entries []json.RawMessage
	cfg := &application.BotExecutionConfig{}
	if c.MessagerPolicy == nil {
		return errBotExecutionConfig
	}
	cfg.Runtime.NoneEncryptionMode = int(c.MessagerPolicy.NoneEncryptionMode)
	if json.Unmarshal(f["version"], &version) != nil || version != 1 || json.Unmarshal(f["allowed_origins"], &origins) != nil || json.Unmarshal(f["send_timeout_ms"], &timeout) != nil || json.Unmarshal(f["event_lifetime_seconds"], &lifetime) != nil || json.Unmarshal(f["max_attempts"], &cfg.Runtime.MaxAttempts) != nil || json.Unmarshal(f["max_pending_per_room"], &cfg.Runtime.MaxPendingPerRoom) != nil || json.Unmarshal(f["adapters"], &entries) != nil || len(entries) < 1 || len(entries) > 128 {
		return errBotExecutionConfig
	}
	if timeout < 1 || timeout > 30000 || lifetime < 1 || lifetime > 86400 {
		return errBotExecutionConfig
	}
	cfg.Runtime.SendTimeout = time.Duration(timeout) * time.Millisecond
	cfg.Runtime.EventLifetime = time.Duration(lifetime) * time.Second
	if cfg.Runtime.Validate() != nil {
		return errBotExecutionConfig
	}
	policy, e := reader.NewPolicy(origins)
	if e != nil {
		return errBotExecutionConfig
	}
	seenOrigins := map[string]bool{}
	for _, origin := range origins {
		canonical, e := policy.Normalize(origin)
		if e != nil || canonical != origin || seenOrigins[origin] {
			return errBotExecutionConfig
		}
		seenOrigins[origin] = true
	}
	seenNames, seenBots := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		fields, e := botFields(entry, "name", "bot_id", "endpoint", "signing_key_file")
		if e != nil {
			return e
		}
		var name, bot, endpoint, keyFile string
		if json.Unmarshal(fields["name"], &name) != nil || json.Unmarshal(fields["bot_id"], &bot) != nil || json.Unmarshal(fields["endpoint"], &endpoint) != nil || json.Unmarshal(fields["signing_key_file"], &keyFile) != nil || !bots.ValidAdapterName(name) || !authority.ValidID(bot) || seenNames[name] || seenBots[bot] {
			return errBotExecutionConfig
		}
		canonical, e := policy.Normalize(endpoint)
		if e != nil || canonical != endpoint {
			return errBotExecutionConfig
		}
		key, e := botConfigFile(keyFile, 128)
		if e != nil || len(key) < 32 {
			return errBotExecutionConfig
		}
		client, e := botdispatch.PublicClient(policy, cfg.Runtime.SendTimeout)
		if e != nil {
			return errBotExecutionConfig
		}
		adapter, e := botdispatch.New(botdispatch.Config{Name: name, BotID: bot, Endpoint: endpoint, SigningKey: key, Client: client})
		if e != nil {
			return errBotExecutionConfig
		}
		cfg.Adapters = append(cfg.Adapters, adapter)
		seenNames[name] = true
		seenBots[bot] = true
	}
	c.BotExecution = cfg
	return nil
}
