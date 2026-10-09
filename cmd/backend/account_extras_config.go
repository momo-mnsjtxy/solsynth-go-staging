// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"flag"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/namecardpg"
	"solsynth.local/go-migration/internal/rewindpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/rewind"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
)

type accountExtraOptions struct {
	enabled                *bool
	price, currency, payee *string
	period, limit, year    *int
}

func accountExtraFlags(f *flag.FlagSet) accountExtraOptions {
	return accountExtraOptions{f.Bool("enable-name-cards", false, "enable paid name cards with explicitly configured canonical points settlement"), f.String("name-card-price", "100", "positive points price"), f.String("name-card-currency", "", "explicit points currency"), f.String("name-card-payee-wallet", "", "existing canonical payee wallet UUID"), f.Int("name-card-period-days", 30, "rolling purchase window"), f.Int("name-card-purchase-limit", 1, "maximum orders per rolling window"), f.Int("rewind-reporting-year", 0, "explicit annual report year; zero uses current UTC year")}
}
func (o accountExtraOptions) apply(c *application.Config) error {
	if *o.year != 0 {
		if _, _, e := rewind.Window(*o.year); e != nil {
			return e
		}
		c.Rewind = &rewindpg.Options{ReportingYear: *o.year}
	}
	if !*o.enabled {
		if *o.currency != "" || *o.payee != "" || *o.price != "100" || *o.period != 30 || *o.limit != 1 {
			return errors.New("name-card policy requires explicit enablement")
		}
		return nil
	}
	price, e := wallet.ParseAmount(*o.price)
	if e != nil || price.Sign() <= 0 || !authority.ValidID(*o.payee) || len(*o.currency) == 0 || len(*o.currency) > 128 || strings.TrimSpace(*o.currency) != *o.currency || strings.ContainsAny(*o.currency, "\x00\r\n") || *o.period < 1 || *o.period > 366 || *o.limit < 1 || *o.limit > 100 {
		return errors.New("invalid explicit name-card settlement policy")
	}
	c.NameCards = &namecardpg.Options{Enabled: true, PricePoints: price, PointsCurrency: *o.currency, PayeeWalletID: *o.payee, PurchasePeriodDays: *o.period, MaxPurchases: *o.limit}
	return nil
}
