// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
)

type awardSettings struct {
	enabled, maintenance, realtime     *bool
	currency, account, wallet, maximum *string
	accountLimit, streamLimit          *int
	ttl                                *time.Duration
}

func livestreamAwardFlags(fs *flag.FlagSet) awardSettings {
	return awardSettings{
		fs.Bool("enable-livestream-awards", false, "enable explicitly bound paid livestream awards"),
		fs.Bool("livestream-awards-maintenance-only", false, "release existing award reserves without streaming APIs"),
		fs.Bool("livestream-award-realtime", false, "enqueue durable current-audience award packets"),
		fs.String("livestream-award-currency", "", "explicit award currency"),
		fs.String("livestream-award-treasury-account", "", "existing treasury account ID"),
		fs.String("livestream-award-treasury-wallet", "", "existing treasury wallet ID"),
		fs.String("livestream-award-max-amount", "1000", "maximum exact award amount"),
		fs.Int("livestream-award-account-limit", 100, "maximum pending orders per account and stream"),
		fs.Int("livestream-award-stream-limit", 10000, "maximum awards per stream"),
		fs.Duration("livestream-award-order-ttl", time.Hour, "pending award order lifetime"),
	}
}
func (s awardSettings) apply(c *application.Config) error {
	defaults := *s.maximum == "1000" && *s.accountLimit == 100 && *s.streamLimit == 10000 && *s.ttl == time.Hour && !*s.realtime
	if !*s.enabled && !*s.maintenance {
		if *s.currency != "" || *s.account != "" || *s.wallet != "" || !defaults {
			return errors.New("award settings require explicit enablement")
		}
		return nil
	}
	if *s.enabled && *s.maintenance {
		return errors.New("award creation and maintenance-only are mutually exclusive")
	}
	if !authority.ValidID(*s.account) || !authority.ValidID(*s.wallet) || !live.Text(*s.currency, 128) || strings.TrimSpace(*s.currency) == "" || strings.TrimSpace(*s.currency) != *s.currency {
		return errors.New("explicit valid award treasury binding required")
	}
	if *s.maintenance {
		if c.Livestreams != nil || !defaults {
			return errors.New("maintenance-only excludes streaming and creation settings")
		}
		c.LivestreamAwardMaintenance = &livestreampg.AwardMaintenanceConfig{PointsCurrency: *s.currency, TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet}
		return nil
	}
	if c.Livestreams == nil {
		return errors.New("awards require streaming configuration")
	}
	if *s.realtime && c.Livestreams.Chat == nil {
		return errors.New("realtime awards require durable livestream data delivery")
	}
	if _, e := live.ParseAwardPoints(*s.maximum); e != nil {
		return e
	}
	amount, e := wallet.ParseAmount(*s.maximum)
	if e != nil {
		return e
	}
	cap, _ := wallet.ParseAmount("43200")
	if amount.Cmp(cap) > 0 || *s.accountLimit < 1 || *s.accountLimit > 1000 || *s.streamLimit < 1 || *s.streamLimit > 100000 || *s.ttl < time.Minute || *s.ttl > 24*time.Hour {
		return errors.New("invalid award creation bounds")
	}
	c.Livestreams.Awards = &livestreampg.AwardConfig{PointsCurrency: *s.currency, TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, MaxAmount: amount, MaxOrdersPerAccountStream: *s.accountLimit, MaxAwardsPerStream: *s.streamLimit, OrderTTL: *s.ttl, RealtimePackets: *s.realtime}
	return nil
}
