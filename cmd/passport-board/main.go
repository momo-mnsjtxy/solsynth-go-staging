// SPDX-License-Identifier: AGPL-3.0-only
// Loopback-only board HTTP compatibility host. No production identity adapter.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/ports/passport/board"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:8084", "loopback listen address")
	fixturePath := flag.String("synthetic-fixture", "", "explicit synthetic principals, grants and widget contracts JSON")
	dsnEnv := flag.String("postgres-dsn-env", "PG_TEST_DSN", "environment variable holding a disposable local PostgreSQL DSN")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return errors.New("listen address must contain an IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("board fixture host requires a loopback IP")
	}
	var handler http.Handler = board.NewHTTPHandler(board.HTTPOptions{})
	var db *sql.DB
	if *fixturePath != "" {
		file, err := os.Open(*fixturePath)
		if err != nil {
			return errors.New("cannot open synthetic fixture")
		}
		config, decodeErr := board.DecodeSyntheticConfig(file)
		_ = file.Close()
		if decodeErr != nil {
			return fmt.Errorf("invalid synthetic fixture: %w", decodeErr)
		}
		fixture, err := board.NewSyntheticFixture(config)
		if err != nil {
			return err
		}
		dsn := os.Getenv(*dsnEnv)
		if dsn == "" {
			return errors.New("synthetic mode requires a disposable PostgreSQL DSN")
		}
		pgConfig, err := pgx.ParseConfig(dsn)
		if err != nil {
			return errors.New("invalid PostgreSQL configuration")
		}
		local := func(host string) bool {
			if len(host) > 0 && host[0] == '/' {
				return true
			}
			ip := net.ParseIP(host)
			return ip != nil && ip.IsLoopback()
		}
		if !local(pgConfig.Host) {
			return errors.New("synthetic PostgreSQL host must be a local socket or loopback IP")
		}
		for _, fallback := range pgConfig.Fallbacks {
			if !local(fallback.Host) {
				return errors.New("synthetic PostgreSQL fallback must be local")
			}
		}
		db = stdlib.OpenDB(*pgConfig)
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err != nil {
			return errors.New("disposable PostgreSQL unavailable")
		}
		repo := boardpg.NewForCompatibilityTests(db, nil)
		handler = fixture.HTTPHandler(repo, repo)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("unable to listen on loopback address")
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Passport board listening on %s", listener.Addr())
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("board HTTP server failed")
	}
	return nil
}
