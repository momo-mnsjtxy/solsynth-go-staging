// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/connectionpg"
	"solsynth.local/go-migration/internal/connectionprovider"
	p "solsynth.local/go-migration/ports/passport/connections"
)

type connectionSettings struct {
	steamKeyEnv *string
	file        *string
	keyEnv      *string
}

func accountConnectionFlags(f *flag.FlagSet) connectionSettings {
	return connectionSettings{f.String("steam-presence-key-env", "", "optional Steam Web API key environment variable for verified provider presence"), f.String("account-provider-config", "", "absolute account-link provider JSON; never contains provider secrets"), f.String("account-provider-key-env", "SOLSYNTH_ACCOUNT_CONNECTION_KEY", "environment variable with base64 32-byte connection verifier encryption key")}
}
func (s connectionSettings) apply(c *application.Config) error {
	if *s.file == "" {
		if *s.steamKeyEnv != "" {
			return errors.New("Steam presence requires explicit account provider configuration")
		}
		return nil
	}
	invalid := errors.New("invalid explicit account provider configuration")
	if !filepath.IsAbs(*s.file) || filepath.Clean(*s.file) != *s.file {
		return invalid
	}
	info, e := os.Lstat(*s.file)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return invalid
	}
	f, e := os.Open(*s.file)
	if e != nil {
		return invalid
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return invalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return invalid
	}
	fields, e := realmBoostObject(raw)
	if e != nil {
		return invalid
	}
	for k := range fields {
		if k != "version" && k != "providers" {
			return invalid
		}
	}
	var version int
	var specs []json.RawMessage
	if json.Unmarshal(fields["version"], &version) != nil || version != 1 || json.Unmarshal(fields["providers"], &specs) != nil || len(specs) < 1 || len(specs) > 32 {
		return invalid
	}
	key, e := base64.StdEncoding.DecodeString(os.Getenv(*s.keyEnv))
	if e != nil || len(key) != 32 {
		return invalid
	}
	cfg := connectionpg.Config{EncryptionKey: key, Providers: []p.Provider{}}
	seen := map[string]bool{}
	for _, raw := range specs {
		m, x := realmBoostObject(raw)
		if x != nil {
			return invalid
		}
		for k := range m {
			switch k {
			case "kind", "provider", "authorization_url", "token_url", "userinfo_url", "client_id", "client_secret_env", "callback_url", "scopes", "api_key_env", "api_secret_env":
			default:
				return invalid
			}
		}
		var spec struct {
			Kind             string   `json:"kind"`
			APIKeyEnv        string   `json:"api_key_env"`
			APISecretEnv     string   `json:"api_secret_env"`
			Provider         string   `json:"provider"`
			AuthorizationURL string   `json:"authorization_url"`
			TokenURL         string   `json:"token_url"`
			UserinfoURL      string   `json:"userinfo_url"`
			ClientID         string   `json:"client_id"`
			ClientSecretEnv  string   `json:"client_secret_env"`
			CallbackURL      string   `json:"callback_url"`
			Scopes           []string `json:"scopes"`
		}
		if json.Unmarshal(raw, &spec) != nil {
			return invalid
		}
		var provider p.Provider
		if spec.Kind != "lastfm" && (spec.APIKeyEnv != "" || spec.APISecretEnv != "") {
			return invalid
		}
		switch spec.Kind {
		case "steam":
			if spec.Provider != "steam" || spec.TokenURL != "" || spec.ClientID != "" || spec.ClientSecretEnv != "" || len(spec.Scopes) != 0 || spec.AuthorizationURL != "" || spec.UserinfoURL != "" {
				return invalid
			}
			provider, e = connectionprovider.NewSteam(spec.CallbackURL, nil)
		case "lastfm":
			if spec.Provider != "lastfm" || spec.TokenURL != "" || spec.UserinfoURL != "" || spec.AuthorizationURL != "" || spec.ClientID != "" || spec.ClientSecretEnv != "" || len(spec.Scopes) != 0 {
				return invalid
			}
			key, secret := os.Getenv(spec.APIKeyEnv), os.Getenv(spec.APISecretEnv)
			provider, e = connectionprovider.NewLastFM(key, secret, spec.CallbackURL, nil)
			if e == nil {
				c.LastFMPresence, e = connectionprovider.NewLastFMPresence(key, nil)
			}
		case "oauth-userinfo":
			provider, e = connectionprovider.NewOAuth(connectionprovider.OAuthConfig{Provider: spec.Provider, AuthorizationURL: spec.AuthorizationURL, TokenURL: spec.TokenURL, UserinfoURL: spec.UserinfoURL, ClientID: spec.ClientID, ClientSecret: os.Getenv(spec.ClientSecretEnv), CallbackURL: spec.CallbackURL, Scopes: spec.Scopes}, nil)
		default:
			return invalid
		}
		if e != nil || provider == nil || seen[provider.Name()] {
			return invalid
		}
		seen[provider.Name()] = true
		cfg.Providers = append(cfg.Providers, provider)
	}
	if *s.steamKeyEnv != "" {
		observer, e := connectionprovider.NewSteamPresence(os.Getenv(*s.steamKeyEnv), nil)
		if e != nil || !seen["steam"] {
			return invalid
		}
		c.SteamPresence = observer
	}
	c.AccountConnections = &cfg
	return nil
}
