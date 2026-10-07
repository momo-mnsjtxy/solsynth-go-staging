// SPDX-License-Identifier: AGPL-3.0-only
// Development host for the first persistent migration slice.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
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
	"solsynth.local/go-migration/ports/sphere"
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
	storage := flag.String("storage", "file", "persistence adapter: file or postgres")
	dsnEnv := flag.String("postgres-dsn-env", "SOLSYNTH_POSTGRES_DSN", "environment variable holding the PostgreSQL DSN; never logged")
	data := flag.String("data", "./data/watch-preferences.json", "single-process local persistence file")
	fixture := flag.String("fixture-account", "", "opt-in synthetic local-test identity; NOT production authentication")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		log.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		log.Fatal("development host must bind a loopback IP")
	}
	var repository sphere.WatchPreferenceRepository
	switch *storage {
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
		log.Fatal("storage must be file or postgres")
	}
	service := &sphere.WatchService{Repository: repository}
	server := &http.Server{Addr: *address, Handler: sphere.NewWatchHandler(service, fixtureAuth{account: *fixture}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Print(err)
		}
	}()
	if *fixture == "" {
		log.Print("authentication is unconfigured: requests will be denied")
	} else {
		log.Print("SYNTHETIC FIXTURE MODE: no production authentication; local development only")
	}
	log.Printf("PostWatch development service listening at %s", *address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
