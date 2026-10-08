// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/postawardpg"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/postawards"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

type postAwardSettings struct {
	enabled, ratings                 *bool
	account, wallet, policy, maximum *string
	interval                         *time.Duration
}

func postAwardFlags(fs *flag.FlagSet) postAwardSettings {
	return postAwardSettings{fs.Bool("enable-post-awards", false, "enable funded points post awards"), fs.Bool("publisher-ratings-paid-awards", false, "include paid signed award score in the explicitly enabled rating producer"), fs.String("post-award-treasury-account", "", "existing canonical post award treasury account UUID"), fs.String("post-award-treasury-wallet", "", "existing canonical post award treasury wallet UUID"), fs.String("post-award-payout-policy", a.MerchantFull, "merchant_full_v1 default or explicit documented_80_v1"), fs.String("post-award-max-amount", "1000000", "maximum exact points per award"), fs.Duration("post-award-release-interval", 24*time.Hour, "daily UTC reserve release, or explicitly configured bounded retry interval")}
}
func (s postAwardSettings) apply(c *application.Config) error {
	if !*s.enabled {
		if *s.account != "" || *s.wallet != "" || *s.policy != a.MerchantFull || *s.maximum != "1000000" || *s.interval != 24*time.Hour || *s.ratings {
			return errors.New("post award configuration requires explicit enablement")
		}
		return nil
	}
	maximum, e := wallet.ParseAmount(*s.maximum)
	bound, _ := wallet.ParseAmount("1000000000")
	if e != nil || maximum.Sign() <= 0 || maximum.Cmp(bound) > 0 || !authority.ValidID(*s.account) || !authority.ValidID(*s.wallet) || (*s.policy != a.MerchantFull && *s.policy != a.Documented80) || *s.interval < time.Second || *s.interval > 24*time.Hour {
		return errors.New("invalid post award treasury, payout policy or bounds")
	}
	c.PostAwards = &postawardpg.Config{TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, Policy: *s.policy, MaxAmount: maximum}
	if *s.ratings {
		if c.PublisherRatings == nil {
			return errors.New("paid award ratings require an explicitly enabled rating producer and start day")
		}
		c.PublisherRatings.ReactionOnly = false
		c.PublisherRatings.PaidAwards = true
	}
	return nil
}
func (s postAwardSettings) start(ctx context.Context, b *application.Backend, out io.Writer) {
	if !*s.enabled {
		return
	}
	go func() {
		run := func() {
			job, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			for i := 0; i < 10; i++ {
				n, e := b.ProcessPostAwardsDue(job, 100)
				if e != nil {
					fmt.Fprintln(out, "Post award settlement is pending; existing reserves remain durable.")
					return
				}
				if n < 100 {
					return
				}
			}
		}
		run()
		for {
			delay := *s.interval
			if delay == 24*time.Hour {
				now := time.Now().UTC()
				delay = now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now)
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				run()
			}
		}
	}()
}
