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
	enabled                                                       *bool
	price, currency, payee                                        *string
	period, limit, year                                           *int
	actions                                                       *bool
	dictionary, stopWords, dictionaryHash, stopWordsHash          *string
	inputBytes, texts, tokens, dictionaryBytes, dictionaryEntries *int
}

func accountExtraFlags(f *flag.FlagSet) accountExtraOptions {
	return accountExtraOptions{
		enabled:           f.Bool("enable-name-cards", false, "enable paid name cards with explicitly configured canonical points settlement"),
		price:             f.String("name-card-price", "100", "positive points price"),
		currency:          f.String("name-card-currency", "", "explicit points currency"),
		payee:             f.String("name-card-payee-wallet", "", "existing canonical payee wallet UUID"),
		period:            f.Int("name-card-period-days", 30, "rolling purchase window"),
		limit:             f.Int("name-card-purchase-limit", 1, "maximum orders per rolling window"),
		year:              f.Int("rewind-reporting-year", 0, "explicit annual report year; zero uses current UTC year"),
		actions:           f.Bool("enable-rewind-action-activity", false, "measure current Go canonical account-action history, not legacy telemetry"),
		dictionary:        f.String("rewind-dictionary", "", "absolute regular UTF-8 dictionary file: word positive_frequency [part_of_speech]"),
		stopWords:         f.String("rewind-stop-words", "", "optional absolute regular UTF-8 stop-word file, one word per line"),
		dictionaryHash:    f.String("rewind-dictionary-sha256", "", "optional lowercase SHA-256 pin for configured dictionary"),
		stopWordsHash:     f.String("rewind-stop-words-sha256", "", "optional lowercase SHA-256 pin for configured stop words"),
		inputBytes:        f.Int("rewind-max-input-bytes", 1048576, "tokenizer input byte limit, 1..1048576"),
		texts:             f.Int("rewind-max-texts", 1000, "tokenizer post limit, 1..1000"),
		tokens:            f.Int("rewind-max-tokens", 200000, "tokenizer token limit, 1..200000"),
		dictionaryBytes:   f.Int("rewind-max-dictionary-bytes", 16*1048576, "dictionary/stop-word file byte limit, 1..16777216"),
		dictionaryEntries: f.Int("rewind-max-dictionary-entries", 200000, "dictionary/stop-word entry limit, 1..200000"),
	}
}
func (o accountExtraOptions) apply(c *application.Config) error {
	if *o.year != 0 {
		if _, _, e := rewind.Window(*o.year); e != nil {
			return e
		}
	}
	if *o.dictionary == "" && (*o.stopWords != "" || *o.dictionaryHash != "" || *o.stopWordsHash != "" || *o.inputBytes != 1048576 || *o.texts != 1000 || *o.tokens != 200000 || *o.dictionaryBytes != 16*1048576 || *o.dictionaryEntries != 200000) {
		return errors.New("rewind tokenizer settings require an explicit dictionary")
	}
	if *o.year != 0 || *o.actions || *o.dictionary != "" {
		c.Rewind = &rewindpg.Options{ReportingYear: *o.year, CanonicalActionActivity: *o.actions}
		if *o.dictionary != "" {
			if *o.inputBytes < 1 || *o.texts < 1 || *o.tokens < 1 || *o.dictionaryBytes < 1 || *o.dictionaryEntries < 1 {
				return errors.New("rewind dictionary limits must be positive")
			}
			config := rewindpg.DictionaryConfig{DictionaryPath: *o.dictionary, StopWordsPath: *o.stopWords, DictionarySHA256: *o.dictionaryHash, StopWordsSHA256: *o.stopWordsHash, MaxInputBytes: *o.inputBytes, MaxTexts: *o.texts, MaxTokens: *o.tokens, MaxDictionaryBytes: *o.dictionaryBytes, MaxDictionaryEntries: *o.dictionaryEntries}
			// Validate and load once before composing the HTTP host. Application callers
			// may instead supply Options.Dictionary and get identical constructor checks.
			tokenizer, e := rewindpg.NewDictionaryTokenizer(config)
			if e != nil {
				return e
			}
			c.Rewind.Tokenizer = tokenizer
		}
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
