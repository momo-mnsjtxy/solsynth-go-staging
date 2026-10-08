// SPDX-License-Identifier: AGPL-3.0-only
// Loopback development host for the anonymous app-discovery compatibility candidate.
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
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/developpg"
	"solsynth.local/go-migration/ports/develop"
)

type config struct {
	listen                    string
	dsnEnv                    string
	syntheticProductionStatus *int32
	syntheticMiniAppStage     *int32
	syntheticBotCommands      bool
	packageDirectory          string
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("develop-discovery", flag.ContinueOnError)
	// Flag parser errors can contain arbitrary input, including misplaced secrets.
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.listen, "listen", "127.0.0.1:8081", "loopback IP and numeric port")
	flags.StringVar(&cfg.dsnEnv, "postgres-dsn-env", "", "environment variable containing the synthetic PostgreSQL DSN")
	packageStage := flags.String("synthetic-miniapp-stage", "", "explicit int32 fixture stage; opts in to package counter writes")
	flags.BoolVar(&cfg.syntheticBotCommands, "synthetic-bot-commands", false, "enable isolated documented-field bot command read candidate")
	flags.StringVar(&cfg.packageDirectory, "synthetic-package-directory", "", "local synthetic package directory; no production object storage")
	status := flags.String("synthetic-production-status", "", "explicit int32 candidate status; NOT a verified production enum or authentication")
	if err := flags.Parse(args); err != nil {
		return config{}, errors.New("invalid command options; use --listen, --postgres-dsn-env and --synthetic-production-status")
	}
	if flags.NArg() != 0 {
		return config{}, errors.New("positional arguments are not supported")
	}
	var supplied, packageSupplied bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "synthetic-miniapp-stage" {
			packageSupplied = true
		}
		if f.Name == "synthetic-production-status" {
			supplied = true
		}
	})
	if supplied {
		value, err := strconv.ParseInt(*status, 10, 32)
		if err != nil {
			return config{}, errors.New("synthetic production status must be an explicit signed int32")
		}
		v := int32(value)
		cfg.syntheticProductionStatus = &v
	}
	if packageSupplied {
		value, err := strconv.ParseInt(*packageStage, 10, 32)
		if err != nil {
			return config{}, errors.New("synthetic miniapp stage must be an explicit signed int32")
		}
		v := int32(value)
		cfg.syntheticMiniAppStage = &v
	}
	if err := cfg.validate(); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func (cfg config) validate() error {
	if cfg.syntheticBotCommands && cfg.syntheticProductionStatus == nil {
		return errors.New("synthetic bot commands require discovery/DSN opt-in")
	}
	if cfg.syntheticMiniAppStage != nil && (cfg.syntheticProductionStatus == nil || cfg.packageDirectory == "") {
		return errors.New("synthetic packages require discovery opt-in and a synthetic package directory")
	}
	if cfg.packageDirectory != "" && cfg.syntheticMiniAppStage == nil {
		return errors.New("package directory requires explicit synthetic miniapp stage")
	}
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
	if cfg.syntheticProductionStatus == nil {
		if cfg.dsnEnv != "" {
			return errors.New("PostgreSQL configuration requires explicit synthetic production status")
		}
		return nil
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
	if cfg.syntheticProductionStatus == nil {
		return newDevelopHandler(develop.NewHandler(nil), develop.NewPackageHandler(nil), develop.NewBotCommandsHandler(nil)), func() {}, nil
	}
	dsn := getenv(cfg.dsnEnv)
	if dsn == "" {
		return nil, nil, errors.New("PostgreSQL DSN environment variable is empty")
	}
	db, err := open("pgx", dsn)
	if err != nil {
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
	repository := developpg.NewSyntheticCompatibilityRepository(db, *cfg.syntheticProductionStatus)
	packageHandler := develop.NewPackageHandler(nil)
	closeStorage := func() {}
	if cfg.syntheticMiniAppStage != nil {
		storage, err := develop.NewSyntheticLocalPackageStorage(cfg.packageDirectory)
		if err != nil {
			_ = db.Close()
			return nil, nil, errors.New("unable to open synthetic package directory")
		}
		closeStorage = func() { _ = storage.Close() }
		packages := developpg.NewSyntheticCompatibilityPackageRepository(db, *cfg.syntheticMiniAppStage)
		service := develop.NewSyntheticCompatibilityPackageService(packages, storage)
		packageHandler = develop.NewSyntheticCompatibilityPackageHandler(service)
	}
	botHandler := develop.NewBotCommandsHandler(nil)
	if cfg.syntheticBotCommands {
		botRepository := developpg.NewSyntheticCompatibilityBotCommandsRepository(db)
		botHandler = develop.NewSyntheticCompatibilityBotCommandsHandler(develop.NewSyntheticCompatibilityBotCommandsService(botRepository))
	}
	return newDevelopHandler(develop.NewSyntheticCompatibilityHandler(repository), packageHandler, botHandler), func() { closeStorage(); _ = db.Close() }, nil
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
	if cfg.syntheticProductionStatus == nil {
		log.Print("app discovery is disabled: verified production policy is unavailable")
	} else {
		log.Print("SYNTHETIC COMPATIBILITY MODE: local development only; status mapping and visibility policy are candidates, not production authentication")
	}
	log.Printf("Develop discovery listening on %s", listener.Addr())
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

// Route families stay explicit; package operations remain disabled unless their
// independent synthetic write/storage opt-in was supplied.
func newDevelopHandler(discovery, packages http.Handler, bots ...http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/bots/") && len(bots) > 0 {
			bots[0].ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/miniapps/") {
			packages.ServeHTTP(w, r)
			return
		}
		discovery.ServeHTTP(w, r)
	})
}
