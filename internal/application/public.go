// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"encoding/json"
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/passport/badgediscovery"
	walletaccounts "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
	"solsynth.local/go-migration/ports/wallet/productcatalog"
)

func publicCatalogRoute(p string) bool {
	return badgediscovery.RouteMatches(p) || exchangecatalog.RouteMatches(p) || productcatalog.RouteMatches(p)
}

// Catalogs are anonymous configuration reads. The enclosing host verifies any
// supplied identity before dispatch; they cannot exchange funds or buy products.
func publicCatalogs(c Config) (http.Handler, error) {
	exchange, err := exchangecatalog.New(c.CatalogProviders...)
	if err != nil {
		return nil, err
	}
	if _, err = exchange.Options(); err != nil {
		return nil, err
	}
	badges := badgediscovery.NewHandler(badgediscovery.Config{IconsPath: c.BadgeIconsPath})
	var products http.Handler
	product, err := productcatalog.New(c.CatalogProviders...)
	if err == nil {
		products = productcatalog.NewOwnedPublicHandler(product)
	} else if !errors.Is(err, productcatalog.ErrDefaultCurrencyUnverified) {
		return nil, err
	}
	exchanges := exchangecatalog.NewHandler(exchange)
	if c.WalletExchange != nil {
		policy, e := walletaccounts.NewExchangePolicy(*c.WalletExchange)
		if e != nil {
			return nil, e
		}
		type option struct {
			SourceCurrency string      `json:"source_currency"`
			SourceAmount   json.Number `json:"source_amount"`
			TargetCurrency string      `json:"target_currency"`
			TargetAmount   json.Number `json:"target_amount"`
		}
		options := []option{}
		for _, r := range policy.Config().Rates {
			options = append(options, option{r.SourceCurrency, json.Number(r.SourceAmount), r.TargetCurrency, json.Number(r.TargetAmount)})
		}
		exchanges = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				w.Header().Set("Allow", "GET")
				w.WriteHeader(405)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(options)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case badgediscovery.RouteMatches(r.URL.Path):
			badges.ServeHTTP(w, r)
		case exchangecatalog.RouteMatches(r.URL.Path):
			exchanges.ServeHTTP(w, r)
		case productcatalog.RouteMatches(r.URL.Path):
			if products == nil {
				problem(w, 503, "PRODUCT_CURRENCY_CONFIGURATION_REQUIRED")
				return
			}
			products.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}), nil
}
