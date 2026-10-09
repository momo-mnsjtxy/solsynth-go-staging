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
	a "solsynth.local/go-migration/ports/wallet/accounts"
)

type walletExchangeSettings struct {
	enabled *bool
	file    *string
}

func walletExchangeFlags(fs *flag.FlagSet) walletExchangeSettings {
	return walletExchangeSettings{fs.Bool("enable-wallet-exchange", false, "opt in to explicitly configured funded internal treasury exchange"), fs.String("wallet-exchange-config", "", "absolute JSON policy file containing existing treasury IDs, version and exact string rate operands")}
}

var errWalletExchangeConfig = errors.New("invalid or unavailable explicit wallet exchange configuration")

func (s walletExchangeSettings) apply(c *application.Config) error {
	if !*s.enabled {
		if *s.file != "" {
			return errWalletExchangeConfig
		}
		return nil
	}
	name := *s.file
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return errWalletExchangeConfig
	}
	info, e := os.Lstat(name)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errWalletExchangeConfig
	}
	f, e := os.Open(name)
	if e != nil {
		return errWalletExchangeConfig
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		return errWalletExchangeConfig
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil || len(raw) > 64<<10 {
		return errWalletExchangeConfig
	}
	fields, e := realmBoostObject(raw)
	if e != nil {
		return errWalletExchangeConfig
	}
	for key := range fields {
		switch key {
		case "version", "treasury_account_id", "treasury_wallet_id", "rates":
		default:
			return errWalletExchangeConfig
		}
	}
	var config a.ExchangeConfig
	if json.Unmarshal(raw, &config) != nil {
		return errWalletExchangeConfig
	}
	var rates []json.RawMessage
	if json.Unmarshal(fields["rates"], &rates) != nil || len(rates) == 0 || len(rates) > 32 {
		return errWalletExchangeConfig
	}
	for _, rate := range rates {
		fields, e := realmBoostObject(rate)
		if e != nil {
			return errWalletExchangeConfig
		}
		for key := range fields {
			switch key {
			case "source_currency", "source_amount", "target_currency", "target_amount":
			default:
				return errWalletExchangeConfig
			}
		}
	}
	policy, e := a.NewExchangePolicy(config)
	if e != nil {
		return errWalletExchangeConfig
	}
	copy := policy.Config()
	c.WalletExchange = &copy
	return nil
}
