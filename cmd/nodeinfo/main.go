// SPDX-License-Identifier: AGPL-3.0-only
// Loopback development host for the Sphere NodeInfo compatibility candidate.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/nodeinfopg"
	"solsynth.local/go-migration/ports/sphere/nodeinfo"
)

type config struct {
	listen                    string
	dsnEnv                    string
	syntheticSoftDeletePolicy string
	domain                    *string
	syntheticFediverseType    *int32
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("nodeinfo", flag.ContinueOnError)
	// Flag parser errors can contain arbitrary input, including misplaced secrets.
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.listen, "listen", "127.0.0.1:8082", "loopback IP and numeric port")
	flags.StringVar(&cfg.dsnEnv, "postgres-dsn-env", "", "environment variable containing the synthetic PostgreSQL DSN")
	flags.StringVar(&cfg.syntheticSoftDeletePolicy, "synthetic-soft-delete-policy", "", "explicit candidate policy: deleted-at-is-null")
	domain := flags.String("activitypub-domain", "", "ActivityPub domain; absent uses source localhost fallback")
	status := flags.String("synthetic-fediverse-type", "", "explicit int32 candidate Fediverse type; NOT a verified Fediverse enum or authentication")
	if err := flags.Parse(args); err != nil {
		return config{}, errors.New("invalid command options; use --listen, --postgres-dsn-env --synthetic-fediverse-type, --synthetic-soft-delete-policy and --activitypub-domain")
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("positional arguments are not supported")
	}
	var supplied bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "activitypub-domain" {
			cfg.domain = domain
		}
		if f.Name == "synthetic-fediverse-type" {
			supplied = true
		}
	})
	if supplied {
		value, err := strconv.ParseInt(*status, 10, 32)
		if err != nil {
			return config{}, errors.New("synthetic Fediverse type must be an explicit signed int32")
		}
		v := int32(value)
		cfg.syntheticFediverseType = &v
	}
	if err := cfg.validate(); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func (cfg config) validate() error {
	host, port, err := net.SplitHostPort(cfg.listen)
	if err != nil {
		return errors.New("listen address must contain a loopback IP and numeric port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("development host must bind a loopback IP")
	}
	if port == "" {
		return errors.New("listen port must be between 0 and 65535")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("listen port must be between 0 and 65535")
		}
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("listen port must be between 0 and 65535")
	}
	if cfg.syntheticFediverseType == nil {
		if cfg.dsnEnv != "" || cfg.syntheticSoftDeletePolicy != "" {
			return errors.New("PostgreSQL configuration requires explicit synthetic Fediverse type")
		}
		return nil
	}
	if cfg.syntheticSoftDeletePolicy != string(nodeinfopg.CandidateExcludeDeletedRows) {
		return errors.New("synthetic mode requires explicit --synthetic-soft-delete-policy=deleted-at-is-null")
	}
	if cfg.dsnEnv == "" {
		return errors.New("synthetic mode requires --postgres-dsn-env")
	}
	// Accept an environment variable name, never a literal connection string.
	for i, r := range cfg.dsnEnv {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9') {
			return errors.New("PostgreSQL DSN must be supplied through an environment variable name")
		}
	}
	return nil
}

func makeHandler(ctx context.Context, cfg config, getenv func(string) string, open func(string, string) (*sql.DB, error)) (http.Handler, func(), error) {
	if err := cfg.validate(); err != nil {
		return nil, nil, err
	}
	if cfg.syntheticFediverseType == nil {
		return nodeinfo.NewHandler(nil, cfg.domain), func() {}, nil
	}
	dsn := getenv(cfg.dsnEnv)
	if dsn == "" {
		return nil, nil, errors.New("PostgreSQL DSN environment variable is empty")
	}
	db, err := open("pgx", dsn)
	if err != nil || db == nil {
		return nil, nil, errors.New("invalid PostgreSQL configuration (connection details suppressed)")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pingCtx)
	cancel()
	if err != nil {
		_ = db.Close()
		return nil, nil, errors.New("PostgreSQL connection failed (connection details suppressed)")
	}
	repository, err := nodeinfopg.NewSyntheticCompatibilityRepository(db, *cfg.syntheticFediverseType, nodeinfopg.SoftDeletePolicy(cfg.syntheticSoftDeletePolicy))
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return nodeinfo.NewSyntheticCompatibilityHandler(repository, cfg.domain), func() { _ = db.Close() }, nil
}

func run(ctx context.Context, cfg config) error {
	handler, closeDB, err := makeHandler(ctx, cfg, os.Getenv, sql.Open)
	if err != nil {
		return err
	}
	defer closeDB()
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return errors.New("unable to listen on development loopback address")
	}
	defer listener.Close()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	if cfg.syntheticFediverseType == nil {
		log.Print("NodeInfo is disabled: verified production policy is unavailable")
	} else {
		log.Print("SYNTHETIC COMPATIBILITY MODE: local development only; Fediverse mapping and visibility policy are candidates, not production authentication")
	}
	log.Printf("Sphere NodeInfo listening on %s", listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("development HTTP server failed")
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-done
			return errors.New("development HTTP server shutdown failed")
		}
		<-done
		return nil
	}
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
