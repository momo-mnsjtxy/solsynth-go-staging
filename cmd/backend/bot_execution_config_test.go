// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/messagerpg"
	"strings"
	"testing"
)

func TestBotExecutionConfigExplicitStrictOffline(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "synthetic-signing-bytes")
	if e := os.WriteFile(key, make([]byte, 32), 0600); e != nil {
		t.Fatal(e)
	}
	valid := map[string]any{"version": 1, "allowed_origins": []string{"https://bot.example.com/"}, "send_timeout_ms": 1000, "event_lifetime_seconds": 600, "max_attempts": 3, "max_pending_per_room": 20, "adapters": []any{map[string]any{"name": "synthetic", "bot_id": "11111111-1111-4111-8111-111111111111", "endpoint": "https://bot.example.com/inbox", "signing_key_file": key}}}
	raw, _ := json.Marshal(valid)
	apply := func(raw []byte, enabled bool) (application.Config, error) {
		t.Helper()
		file := filepath.Join(dir, "config.json")
		if e := os.WriteFile(file, raw, 0600); e != nil {
			t.Fatal(e)
		}
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		s := botExecutionFlags(fs)
		*s.enabled = enabled
		*s.file = file
		c := application.Config{MessagerPolicy: &messagerpg.IndependentPolicy{NoneEncryptionMode: 77}}
		e := s.apply(&c)
		return c, e
	}
	c, e := apply(raw, true)
	if e != nil || c.BotExecution == nil || len(c.BotExecution.Adapters) != 1 || c.BotExecution.Runtime.NoneEncryptionMode != 77 {
		t.Fatal("explicit config", e)
	}
	if _, e = apply(raw, false); e == nil {
		t.Fatal("config silently activated")
	}
	for _, bad := range []string{strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"version":1`, `"Version":1`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"unknown":0`, 1), strings.Replace(string(raw), `"max_attempts":3`, `"max_attempts":0`, 1), strings.Replace(string(raw), `"max_pending_per_room":20`, `"max_pending_per_room":10001`, 1), strings.Replace(string(raw), `"send_timeout_ms":1000`, `"send_timeout_ms":30001`, 1), strings.Replace(string(raw), `https://bot.example.com/inbox`, `https://127.0.0.1/inbox`, 1), strings.Replace(string(raw), `https://bot.example.com/inbox`, `https://bot.example.com:444/inbox`, 1), strings.Replace(string(raw), `"adapters":[`, `"adapters":null,"ignored":[`, 1)} {
		if _, e = apply([]byte(bad), true); e == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	fs := flag.NewFlagSet("disabled", flag.ContinueOnError)
	s := botExecutionFlags(fs)
	var disabled application.Config
	if e = s.apply(&disabled); e != nil || disabled.BotExecution != nil {
		t.Fatal("default activation")
	}
	if _, e = botConfigFile("relative", 128); e == nil {
		t.Fatal("relative key accepted")
	}
	link := filepath.Join(dir, "link")
	if e = os.Symlink(key, link); e != nil {
		t.Fatal(e)
	}
	if _, e = botConfigFile(link, 128); e == nil {
		t.Fatal("symlink key accepted")
	}
}

func TestBotExecutionConfigRejectsInvalidFieldsOffline(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "inert-key")
	if err := os.WriteFile(key, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"name": "synthetic", "bot_id": "11111111-1111-4111-8111-111111111111", "endpoint": "https://bot.example.com/inbox", "signing_key_file": key}
	valid := map[string]any{"version": 1, "allowed_origins": []string{"https://bot.example.com/"}, "send_timeout_ms": 1000, "event_lifetime_seconds": 600, "max_attempts": 3, "max_pending_per_room": 20, "adapters": []any{entry}}
	file := filepath.Join(dir, "config.json")
	check := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		enabled := true
		config := application.Config{MessagerPolicy: &messagerpg.IndependentPolicy{NoneEncryptionMode: 77}}
		if err := (botExecutionSettings{&enabled, &file}).apply(&config); err != errBotExecutionConfig || config.BotExecution != nil {
			t.Fatal("invalid config was accepted, exposed details, or partially activated")
		}
	}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, value := range valid {
		valid[name] = nil
		t.Run("null "+name, func(t *testing.T) { check(encode(valid)) })
		delete(valid, name)
		t.Run("missing "+name, func(t *testing.T) { check(encode(valid)) })
		valid[name] = value
	}
	for name, value := range entry {
		entry[name] = nil
		t.Run("adapter null "+name, func(t *testing.T) { check(encode(valid)) })
		delete(entry, name)
		t.Run("adapter missing "+name, func(t *testing.T) { check(encode(valid)) })
		entry[name] = value
	}
	for field, badValues := range map[string][]any{
		"version":                {0, 2, 1.5, "1"},
		"send_timeout_ms":        {0, 30001, 1.5, "1000"},
		"event_lifetime_seconds": {0, 86401, 1.5},
		"max_attempts":           {0, 21, 1.5},
		"max_pending_per_room":   {0, 10001, 1.5},
		"allowed_origins":        {[]string{}, []any{nil}, []string{"https://bot.example.com/", "https://bot.example.com/"}, []string{"https://BOT.example.com/"}, []string{"https://bot.example.com:443/"}, []string{"https://bot.example.com"}, []string{"https://bot.example.com/inbox"}},
		"adapters":               {[]any{}, []any{nil}, []any{entry, entry}},
	} {
		original := valid[field]
		for _, bad := range badValues {
			valid[field] = bad
			t.Run("invalid "+field, func(t *testing.T) { check(encode(valid)) })
		}
		valid[field] = original
	}
	for field, badValues := range map[string][]any{
		"name":             {"", "UPPER", strings.Repeat("a", 65)},
		"bot_id":           {"not-an-id", "11111111-1111-4111-8111-11111111111A"},
		"endpoint":         {"https://other.example.com/inbox", "https://bot.example.com/inbox?private=value", "https://bot.example.com/inbox#fragment", "https://user:private@bot.example.com/inbox", "https://BOT.example.com/inbox", "https://bot.example.com:443/inbox", "https://bot.example.com/inbox?"},
		"signing_key_file": {"relative", filepath.Join(dir, "absent-private-path")},
	} {
		original := entry[field]
		for _, bad := range badValues {
			entry[field] = bad
			t.Run("invalid adapter "+field, func(t *testing.T) { check(encode(valid)) })
		}
		entry[field] = original
	}
	raw := string(encode(valid))
	for _, malformed := range []string{
		raw + " {}", "[]", "null", raw[:len(raw)-1],
		strings.Replace(raw, `"name":"synthetic"`, `"name":"synthetic","name":"synthetic"`, 1),
		strings.Replace(raw, `"name":"synthetic"`, `"name":"synthetic","signing_key":"inert-inline-value"`, 1),
		strings.Replace(raw, `"name":"synthetic"`, `"Name":"synthetic"`, 1),
		strings.Replace(raw, `"version":1`, `"version":1,"\u0076ersion":1`, 1),
	} {
		check([]byte(malformed))
	}
	for _, size := range []int{31, 129} {
		if err := os.WriteFile(key, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
		check([]byte(raw))
	}
	enabled, empty := true, ""
	if err := (botExecutionSettings{&enabled, &empty}).apply(&application.Config{}); err != errBotExecutionConfig {
		t.Fatal("enable flag alone was accepted")
	}
}
