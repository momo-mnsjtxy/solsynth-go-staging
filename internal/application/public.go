// SPDX-License-Identifier: AGPL-3.0-only
package application

import (
	"errors"
	"net/http"
	"solsynth.local/go-migration/ports/passport/badgediscovery"
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
