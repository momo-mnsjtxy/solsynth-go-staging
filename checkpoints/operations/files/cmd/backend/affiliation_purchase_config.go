// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"time"

	"solsynth.local/go-migration/internal/affiliationpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/ports/authority"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type affiliationPurchaseSettings struct {
	enabled                          *bool
	price, currency, account, wallet *string
	maxUses                          *int
	maxLifetime                      *time.Duration
}

func affiliationPurchaseFlags(f *flag.FlagSet) affiliationPurchaseSettings {
	return affiliationPurchaseSettings{
		f.Bool("enable-paid-affiliation-invites", false, "enable funded registration invitation purchases"),
		f.String("affiliation-invite-price", "100", "positive exact internal balance price"),
		f.String("affiliation-invite-currency", "", "explicit internal balance currency"),
		f.String("affiliation-invite-treasury-account", "", "existing canonical treasury account UUID"),
		f.String("affiliation-invite-treasury-wallet", "", "existing canonical treasury wallet UUID"),
		f.Int("affiliation-invite-max-usages", 10, "maximum purchased invitation uses"),
		f.Duration("affiliation-invite-max-lifetime", 30*24*time.Hour, "maximum purchased invitation lifetime"),
	}
}
func (s affiliationPurchaseSettings) apply(c *application.Config) error {
	defaults := *s.price == "100" && *s.currency == "" && *s.account == "" && *s.wallet == "" && *s.maxUses == 10 && *s.maxLifetime == 30*24*time.Hour
	if !*s.enabled {
		if !defaults {
			return errors.New("paid affiliation invite policy requires explicit enablement")
		}
		return nil
	}
	price, e := wallet.ParseAmount(*s.price)
	if e != nil || price.Sign() <= 0 || !wallet.ValidCurrency(*s.currency) || !authority.ValidID(*s.account) || !authority.ValidID(*s.wallet) || *s.maxUses < 1 || *s.maxUses > 1000 || *s.maxLifetime < time.Hour || *s.maxLifetime > 365*24*time.Hour {
		return errors.New("invalid paid affiliation invite policy")
	}
	c.AffiliationPurchase = &affiliationpg.PurchaseConfig{Currency: *s.currency, TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, Price: price, MaxUsages: *s.maxUses, MaxLifetime: *s.maxLifetime}
	return nil
}
