// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/publisherratingpg"
	"solsynth.local/go-migration/internal/sponsorshippg"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

type publisherCommerceSettings struct {
	sponsors, ratings                  *bool
	account, wallet, maximum, earliest *string
	orders                             *int
	ttl, interval                      *time.Duration
}

func publisherCommerceFlags(fs *flag.FlagSet) publisherCommerceSettings {
	return publisherCommerceSettings{
		fs.Bool("enable-post-sponsorship", false, "enable golds-backed public post sponsorship"),
		fs.Bool("enable-publisher-ratings", false, "enable the explicit reaction-only publisher rating subset"),
		fs.String("sponsor-treasury-account", "", "existing canonical sponsorship treasury account UUID"),
		fs.String("sponsor-treasury-wallet", "", "existing canonical sponsorship treasury wallet UUID"),
		fs.String("sponsor-max-amount", "1000000", "maximum exact golds per sponsorship order"),
		fs.String("publisher-ratings-earliest-day", "", "earliest enabled UTC rating day, YYYY-MM-DD"),
		fs.Int("sponsor-post-order-limit", 10000, "maximum cumulative sponsorship orders per post"),
		fs.Duration("sponsor-order-ttl", time.Hour, "unpaid sponsorship order lifetime"),
		fs.Duration("publisher-commerce-interval", time.Minute, "bounded publisher rating and hourly-auction recovery poll"),
	}
}
func (s publisherCommerceSettings) apply(c *application.Config) error {
	if !*s.sponsors && (*s.account != "" || *s.wallet != "" || *s.maximum != "1000000" || *s.orders != 10000 || *s.ttl != time.Hour) {
		return errors.New("sponsorship configuration requires explicit enablement")
	}
	if !*s.ratings && *s.earliest != "" {
		return errors.New("rating start day requires explicit reaction-only enablement")
	}
	if !*s.sponsors && !*s.ratings {
		if *s.interval != time.Minute {
			return errors.New("publisher worker interval requires an enabled module")
		}
		return nil
	}
	if *s.interval < time.Second || *s.interval > time.Hour {
		return errors.New("publisher maintenance interval must be one second through one hour")
	}
	if *s.sponsors {
		amount, e := wallet.ParseAmount(*s.maximum)
		if e != nil || amount.Cmp(sponsorshipMinimum()) < 0 || !authority.ValidID(*s.account) || !authority.ValidID(*s.wallet) || *s.orders < 1 || *s.orders > 100000 || *s.ttl < time.Minute || *s.ttl > 24*time.Hour {
			return errors.New("invalid explicit sponsorship treasury or creation bounds")
		}
		c.Sponsorship = &sponsorshippg.Config{TreasuryAccountID: *s.account, TreasuryWalletID: *s.wallet, MaxAmount: amount, MaxOrdersPerPost: *s.orders, OrderTTL: *s.ttl}
	}
	if *s.ratings {
		day, e := time.Parse("2006-01-02", *s.earliest)
		if e != nil || day.IsZero() {
			return errors.New("explicit valid UTC rating start day required")
		}
		c.PublisherRatings = &publisherratingpg.Config{ReactionOnly: true, EarliestDay: day}
	}
	return nil
}
func sponsorshipMinimum() wallet.Amount { return s.Minimum() }
func (s publisherCommerceSettings) start(ctx context.Context, b *application.Backend, out io.Writer) {
	if !*s.sponsors && !*s.ratings {
		return
	}
	go func() {
		run := func() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			job, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, e := b.ProcessPublisherCommerceDue(job, 100)
			cancel()
			if e != nil {
				fmt.Fprintln(out, "Publisher commerce maintenance failed; committed receipts remain durable.")
			}
		}
		run()
		ticker := time.NewTicker(*s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
