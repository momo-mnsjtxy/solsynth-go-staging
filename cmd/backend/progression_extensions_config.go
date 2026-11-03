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
	captcha                             *bool
	nightOwls                           *bool
	streakGold                          *bool
	streakAccount, streakWallet         *string
	streakMaxDay                        *int64
}

func progressionExtensionFlags(f *flag.FlagSet) progressionExtensionSettings {
	return progressionExtensionSettings{
		points:        f.Bool("enable-progression-source-points", false, "enable earned grants paid from explicit funded treasury"),
		account:       f.String("progression-treasury-account", "", "existing canonical treasury account UUID"),
		wallet:        f.String("progression-treasury-wallet", "", "existing treasury-owned wallet UUID"),
		currency:      f.String("progression-source-currency", "", "explicit canonical reward currency"),
		products:      f.String("check-in-perk-products", "", "comma-separated canonical subscription identifiers eligible for backdates"),
		maxGrant:      f.Int64("progression-max-points-per-grant", 1000000, "maximum funded points per earned grant"),
		maxDay:        f.Int64("progression-max-points-per-day", 10000000, "maximum funded points per UTC day"),
		birthday:      f.Int64("birthday-experience", 0, "explicit once-per-year birthday XP policy; zero disables"),
		backdate:      f.Int("check-in-backdate-days", 0, "explicit maximum backdate window; zero disables"),
		captcha:       f.Bool("require-check-in-captcha", false, "require the explicitly configured verification CAPTCHA for every check-in"),
		nightOwls:     f.Bool("enable-night-owl-reminders", false, "enable durable local-time wellbeing reminder producer"),
		streakGold:    f.Bool("enable-check-in-streak-gold", false, "pay earned 7-day and 30-day check-in gold tiers from an explicit funded treasury"),
		streakAccount: f.String("check-in-gold-treasury-account", "", "existing canonical treasury account UUID for check-in gold"),
		streakWallet:  f.String("check-in-gold-treasury-wallet", "", "existing treasury-owned wallet UUID with funded golds"),
		streakMaxDay:  f.Int64("check-in-gold-max-per-day", 10000, "maximum check-in gold paid from the treasury per UTC day"),
	}
}
func (s progressionExtensionSettings) apply(c *application.Config) error {
	c.NightOwlReminders = *s.nightOwls
	if *s.captcha {
		if c.Signin == nil || c.Signin.Captcha == nil {
			return errors.New("check-in CAPTCHA requires an explicit configured verification provider")
		}
		c.ProgressionCaptcha = c.Signin.Captcha
	}
	if *s.points {
		cfg := progressionpg.SourcePointConfig{TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, Currency: *s.currency, MaxPerGrant: *s.maxGrant, MaxPerDay: *s.maxDay}
		if _, e := progressionpg.NewFundedSourcePoints(cfg); e != nil {
			return e
		}
		c.ProgressionSourcePoints = &cfg
	} else if *s.account != "" || *s.wallet != "" || *s.currency != "" || *s.maxGrant != 1000000 || *s.maxDay != 10000000 {
		return errors.New("progression treasury policy requires explicit enablement")
	}
	if *s.streakGold {
		cfg := progressionpg.SourcePointConfig{TreasuryAccountID: *s.streakAccount, TreasuryWalletID: *s.streakWallet, Currency: "golds", MaxPerGrant: 6, MaxPerDay: *s.streakMaxDay}
		if _, e := progressionpg.NewFundedCheckInStreakGold(cfg); e != nil {
			return e
		}
		c.CheckInStreakGold = &cfg
	} else if *s.streakAccount != "" || *s.streakWallet != "" || *s.streakMaxDay != 10000 {
		return errors.New("check-in gold treasury policy requires explicit enablement")
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
