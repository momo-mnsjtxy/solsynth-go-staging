// SPDX-License-Identifier: AGPL-3.0-only
// Development host for the first persistent migration slice.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/watchpg"
	"solsynth.local/go-migration/internal/watchstore"
	"solsynth.local/go-migration/ports/passport/badgediscovery"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
	"solsynth.local/go-migration/ports/wallet/productcatalog"
)

// fixtureAuth is explicitly development-only and binds one synthetic account.
// Empty configuration denies every request. It is not a token verifier.
type fixtureAuth struct{ account string }

func (a fixtureAuth) AccountID(r *http.Request) (string, error) {
	if a.account == "" || r.Header.Get("X-Test-Account") != a.account {
		return "", errors.New("production authentication adapter is not configured")
	}
	return a.account, nil
}
func (a fixtureAuth) CanManagePostSubscriptions(r *http.Request, id string) error {
	if a.account == "" || id != a.account {
		return errors.New("forbidden")
	}
	return nil
}
func main() {
	address := flag.String("listen", "127.0.0.1:8080", "loopback listen address")
	storage := flag.String("storage", "file", "watch persistence adapter: file, postgres, or none")
	badgeIcons := flag.String("badge-icons", "./Resources/BadgeIcons", "badge SVG directory; upstream default, missing directory omits icon URLs")
	var productFlags walletProductFlags
	flag.BoolVar(&productFlags.enabled, "synthetic-wallet-product-catalog", false, "opt in to documented wallet product configuration candidate; no purchases")
	flag.Var(&productFlags.configs, "wallet-product-config", "wallet product JSON snapshot provider; repeat in precedence order")
	flag.StringVar(&productFlags.defaultCurrency, "synthetic-wallet-product-default-currency", "", "explicit fixture default, not inferred upstream constant")
	var exchangeConfigs configFiles
	flag.Var(&exchangeConfigs, "exchange-config", "appsettings-style JSON file for exchange catalog; repeat in low-to-high precedence order")
	dsnEnv := flag.String("postgres-dsn-env", "SOLSYNTH_POSTGRES_DSN", "environment variable holding the PostgreSQL DSN; never logged")
	data := flag.String("data", "./data/watch-preferences.json", "single-process local persistence file")
	fixture := flag.String("fixture-account", "", "opt-in synthetic local-test identity; NOT production authentication")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "synthetic-wallet-product-default-currency" {
			productFlags.defaultCurrencySet = true
		}
	})
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		log.Fatal("development host must bind a loopback IP")
	}
	catalog, err := loadExchangeCatalog(exchangeConfigs)
	if err != nil {
		log.Fatal(err)
	}
	products, err := makeWalletProductHandler(productFlags)
	if err != nil {
		log.Fatal(err)
	}
	var repository sphere.WatchPreferenceRepository
	switch *storage {
	case "none":
		// Disabled persistence-backed routes remain fail-closed; catalog is stateless.
	case "file":
		store, openErr := watchstore.Open(*data)
		if openErr != nil {
			log.Fatal(openErr)
		}
		repository = store
	case "postgres":
		dsn := os.Getenv(*dsnEnv)
		if dsn == "" {
			log.Fatal("PostgreSQL DSN environment variable is empty")
		}
		db, openErr := sql.Open("pgx", dsn)
		if openErr != nil {
			log.Fatal("Invalid PostgreSQL configuration (details suppressed to protect connection data)")
		}
		defer db.Close()
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(15 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := db.PingContext(ctx)
		cancel()
		if pingErr != nil {
			log.Fatal("PostgreSQL connection failed (details suppressed to protect connection data)")
		}
		repository = watchpg.New(db)
	default:
		log.Fatal("storage must be file, postgres, or none")
	}
	service := &sphere.WatchService{Repository: repository}
	server := &http.Server{Addr: *address, Handler: newAPIHandler(sphere.NewWatchHandler(service, fixtureAuth{account: *fixture}), catalog, badgediscovery.NewHandler(badgediscovery.Config{IconsPath: *badgeIcons}), products), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		log.Fatal("unable to listen on development loopback address")
	}
	defer listener.Close()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Print(err)
		}
	}()
	if *fixture == "" {
		log.Print("authentication is unconfigured: protected watch requests will be denied")
	} else {
		log.Print("SYNTHETIC FIXTURE MODE: no production authentication; local development only")
	}
	log.Printf("Solsynth development service listening at %s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-shutdownDone
}

// configFiles preserves provider precedence; configuration is a startup snapshot.
type configFiles []string

func (f *configFiles) String() string { return fmt.Sprint([]string(*f)) }
func (f *configFiles) Set(value string) error {
	if value == "" {
		return errors.New("exchange configuration path cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func loadExchangeCatalog(paths []string) (*exchangecatalog.Catalog, error) {
	providers := make([]exchangecatalog.Provider, 0, len(paths))
	for i, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("exchange configuration provider %d cannot be opened", i+1)
		}
		provider, loadErr := exchangecatalog.LoadJSON(file)
		closeErr := file.Close()
		if loadErr != nil {
			return nil, fmt.Errorf("exchange configuration provider %d is invalid", i+1)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("exchange configuration provider %d could not be closed", i+1)
		}
		providers = append(providers, provider)
	}
	return exchangecatalog.New(providers...)
}

// Exact routing is delegated to the catalog's verified MVC-style matcher.
// Other requests retain the existing protected watch handler unchanged.
func newAPIHandler(watch http.Handler, catalog *exchangecatalog.Catalog, badges ...http.Handler) http.Handler {
	exchange := exchangecatalog.NewHandler(catalog)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(badges) > 1 && badges[1] != nil && productcatalog.RouteMatches(r.URL.Path) {
			badges[1].ServeHTTP(w, r)
			return
		}
		if len(badges) > 0 && badges[0] != nil && badgediscovery.RouteMatches(r.URL.Path) {
			badges[0].ServeHTTP(w, r)
			return
		}
		if exchangecatalog.RouteMatches(r.URL.Path) {
			exchange.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/posts/watch" {
			watch.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}
