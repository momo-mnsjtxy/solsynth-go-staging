// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/progressionpg"
	"strings"
)

type progressionExtensionSettings struct {
	points                              *bool
	account, wallet, currency, products *string
	maxGrant, maxDay, birthday          *int64
	backdate                            *int
}

func progressionExtensionFlags(f *flag.FlagSet) progressionExtensionSettings {
	return progressionExtensionSettings{f.Bool("enable-progression-source-points", false, "enable earned grants paid from explicit funded treasury"), f.String("progression-treasury-account", "", "existing canonical treasury account UUID"), f.String("progression-treasury-wallet", "", "existing treasury-owned wallet UUID"), f.String("progression-source-currency", "", "explicit canonical reward currency"), f.String("check-in-perk-products", "", "comma-separated canonical subscription identifiers eligible for backdates"), f.Int64("progression-max-points-per-grant", 1000000, "maximum funded points per earned grant"), f.Int64("progression-max-points-per-day", 10000000, "maximum funded points per UTC day"), f.Int64("birthday-experience", 0, "explicit once-per-year birthday XP policy; zero disables"), f.Int("check-in-backdate-days", 0, "explicit maximum backdate window; zero disables")}
}
func (s progressionExtensionSettings) apply(c *application.Config) error {
	if *s.points {
		cfg := progressionpg.SourcePointConfig{TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, Currency: *s.currency, MaxPerGrant: *s.maxGrant, MaxPerDay: *s.maxDay}
		if _, e := progressionpg.NewFundedSourcePoints(cfg); e != nil {
			return e
		}
		c.ProgressionSourcePoints = &cfg
	} else if *s.account != "" || *s.wallet != "" || *s.currency != "" || *s.maxGrant != 1000000 || *s.maxDay != 10000000 {
		return errors.New("progression treasury policy requires explicit enablement")
	}
	if *s.backdate != 0 || *s.birthday != 0 || *s.products != "" {
		policy := progressionpg.CheckInPolicy{BackdateWindowDays: *s.backdate, BirthdayExperience: *s.birthday}
		if *s.products != "" {
			policy.PerkProducts = strings.Split(*s.products, ",")
		}
		if e := progressionpg.ValidateCheckInPolicy(policy); e != nil {
			return e
		}
		c.ProgressionCheckIn = &policy
	}
	return nil
}
