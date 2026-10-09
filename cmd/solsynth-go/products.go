// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
	"solsynth.local/go-migration/ports/wallet/productcatalog"
)

type walletProductFlags struct {
	enabled            bool
	configs            configFiles
	defaultCurrency    string
	defaultCurrencySet bool
}

func makeWalletProductHandler(cfg walletProductFlags) (http.Handler, error) {
	if !cfg.enabled {
		if len(cfg.configs) > 0 || cfg.defaultCurrencySet {
			return nil, errors.New("wallet product configuration requires explicit synthetic catalog opt-in")
		}
		return productcatalog.NewHandler(nil), nil
	}
	var providers []exchangecatalog.Provider
	for i, path := range cfg.configs {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("wallet product configuration provider %d cannot be opened", i+1)
		}
		provider, loadErr := exchangecatalog.LoadJSON(file)
		closeErr := file.Close()
		if loadErr != nil {
			return nil, fmt.Errorf("wallet product configuration provider %d is invalid", i+1)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("wallet product configuration provider %d could not be closed", i+1)
		}
		providers = append(providers, provider)
	}
	var catalog *productcatalog.Catalog
	var err error
	if cfg.defaultCurrencySet {
		catalog, err = productcatalog.NewWithSyntheticDefaultCurrency(cfg.defaultCurrency, providers...)
	} else {
		catalog, err = productcatalog.New(providers...)
	}
	if err != nil {
		return nil, errors.New("wallet product configuration unresolved: supply explicit Currency or a labelled synthetic default")
	}
	return productcatalog.NewSyntheticCompatibilityHandler(catalog), nil
}
