// SPDX-License-Identifier: AGPL-3.0-only
// Local fixture host. Not a production authenticator or complete Messager service.
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
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/messager"
)

type config struct {
	unreadPolicy              *messagerpg.SyntheticUnreadPolicy
	listen, dsnEnv, accountID string
	synthetic                 bool
	noneMode                  *int32
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	flags := flag.NewFlagSet("messager-sync", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.listen, "listen", "127.0.0.1:8085", "loopback IP and port")
	flags.BoolVar(&cfg.synthetic, "synthetic-projection", false, "enable incomplete synthetic message projection; never production")
	flags.StringVar(&cfg.dsnEnv, "postgres-dsn-env", "", "environment variable containing synthetic PostgreSQL DSN")
	flags.StringVar(&cfg.accountID, "synthetic-account", "", "fixed fixture account GUID; omit for anonymous public message reads")
	none := flags.String("synthetic-none-encryption-mode", "", "explicit signed int32 fixture mapping for unencrypted rooms")

	all := flags.String("synthetic-notify-all", "", "explicit int32 fixture All enum; requires all three notify flags")
	mentions := flags.String("synthetic-notify-mentions", "", "explicit int32 fixture Mentions enum")
	noNotify := flags.String("synthetic-notify-none", "", "explicit int32 fixture None enum")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return config{}, errors.New("invalid command options")
	}
	var supplied bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "synthetic-none-encryption-mode" {
			supplied = true
		}
	})
	if supplied {
		value, err := strconv.ParseInt(*none, 10, 32)
		if err != nil {
			return config{}, errors.New("encryption mapping must be an explicit signed int32")
		}
		v := int32(value)
		cfg.noneMode = &v
	}

	notifyCount := 0
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "synthetic-notify-all" || f.Name == "synthetic-notify-mentions" || f.Name == "synthetic-notify-none" {
			notifyCount++
		}
	})
	if notifyCount != 0 {
		if notifyCount != 3 {
			return config{}, errors.New("unread requires all three synthetic notify mappings")
		}
		policy := messagerpg.SyntheticUnreadPolicy{}
		for _, pair := range []struct {
			s string
			p *int32
		}{{*all, &policy.All}, {*mentions, &policy.Mentions}, {*noNotify, &policy.None}} {
			value, err := strconv.ParseInt(pair.s, 10, 32)
			if err != nil {
				return config{}, errors.New("notify mapping must be an explicit signed int32")
			}
			*pair.p = int32(value)
		}
		cfg.unreadPolicy = &policy
	}
	return cfg, cfg.validate()
}
func (cfg config) validate() error {
	host, port, err := net.SplitHostPort(cfg.listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen address must use a loopback IP")
	}
	if port == "" {
		return errors.New("listen port must be numeric")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("listen port must be numeric")
		}
	}
	if _, err = strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("listen port out of range")
	}
	if !cfg.synthetic {
		if cfg.dsnEnv != "" || cfg.accountID != "" || cfg.noneMode != nil || cfg.unreadPolicy != nil {
			return errors.New("database, fixture identity and enum require --synthetic-projection")
		}
		return nil
	}
	if cfg.dsnEnv == "" || cfg.noneMode == nil {
		return errors.New("synthetic projection requires --postgres-dsn-env and --synthetic-none-encryption-mode")
	}
	for i, r := range cfg.dsnEnv {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9') {
			return errors.New("DSN must be an environment variable name")
		}
	}

	if cfg.unreadPolicy != nil {
		p := cfg.unreadPolicy
		if p.All == p.Mentions || p.All == p.None || p.Mentions == p.None {
			return errors.New("notify mappings must be distinct")
		}
	}
	if cfg.accountID != "" && messager.ValidateSyncID(cfg.accountID) != nil {
		return errors.New("fixture account must be a canonical GUID")
	}
	return nil
}
func makeHandler(ctx context.Context, cfg config, getenv func(string) string, open func(string, string) (*sql.DB, error)) (http.Handler, func(), error) {
	if err := cfg.validate(); err != nil {
		return nil, nil, err
	}
	if !cfg.synthetic {
		return messager.NewSyncHandler(nil), func() {}, nil
	}
	dsn := getenv(cfg.dsnEnv)
	if dsn == "" {
		return nil, nil, errors.New("synthetic PostgreSQL DSN is empty")
	}
	db, err := open("pgx", dsn)
	if err != nil {
		return nil, nil, errors.New("invalid PostgreSQL configuration (details suppressed)")
	}
	if db == nil {
		return nil, nil, errors.New("PostgreSQL database unavailable")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pingCtx)
	cancel()
	if err != nil {
		db.Close()
		return nil, nil, errors.New("PostgreSQL connection failed (details suppressed)")
	}
	repo := messagerpg.NewSyntheticProjectionRepository(db, *cfg.noneMode)
	if cfg.unreadPolicy != nil {
		repo, err = messagerpg.NewSyntheticProjectionRepositoryWithUnread(db, *cfg.noneMode, *cfg.unreadPolicy)
		if err != nil {
			db.Close()
			return nil, nil, err
		}
	}

	handler, err := messager.NewSyntheticProjectionSyncHandler(messager.NewSyntheticProjectionSyncService(repo), cfg.accountID)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return handler, func() { _ = db.Close() }, nil
}
func run(ctx context.Context, cfg config) error {
	handler, cleanup, err := makeHandler(ctx, cfg, os.Getenv, sql.Open)
	if err != nil {
		return err
	}
	defer cleanup()
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return errors.New("unable to listen on loopback address")
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	if cfg.synthetic {
		log.Print("SYNTHETIC MESSAGE PROJECTION: incomplete wire DTO; fixed fixture identity; not production authentication")
	} else {
		log.Print("Messager disabled: production policy and hydration unverified")
	}
	log.Printf("Messager sync listening on %s", listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("HTTP server failed")
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("HTTP shutdown failed")
		}
		return nil
	}
}
func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = run(ctx, cfg); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
