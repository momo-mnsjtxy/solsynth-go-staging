// SPDX-License-Identifier: AGPL-3.0-only
// Loopback-only synthetic taxonomy host. Never configures production authority.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	"solsynth.local/go-migration/internal/taxonomypg"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type config struct {
	listen, dsnEnv, authorityFile string
	publicVisibility              *int32
}

func parseConfig(args []string) (config, error) {
	var cfg config
	f := flag.NewFlagSet("taxonomy", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&cfg.listen, "listen", "127.0.0.1:8083", "loopback IP and port")
	f.StringVar(&cfg.dsnEnv, "postgres-dsn-env", "", "environment variable containing disposable PostgreSQL DSN")
	f.StringVar(&cfg.authorityFile, "synthetic-authority-file", "", "local JSON fixture; never production authentication")
	visibility := f.String("synthetic-public-visibility", "", "explicit signed int32 fixture enum; enables synthetic SQL projections")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return config{}, errors.New("invalid taxonomy command options")
	}
	var supplied bool
	f.Visit(func(flag *flag.Flag) {
		if flag.Name == "synthetic-public-visibility" {
			supplied = true
		}
	})
	if supplied {
		v, err := strconv.ParseInt(*visibility, 10, 32)
		if err != nil {
			return config{}, errors.New("synthetic visibility must be an explicit int32")
		}
		x := int32(v)
		cfg.publicVisibility = &x
	}
	return cfg, cfg.validate()
}
func (c config) validate() error {
	host, port, err := net.SplitHostPort(c.listen)
	if err != nil {
		return errors.New("listen requires loopback IP and numeric port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("taxonomy host must bind a loopback IP")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("invalid listen port")
		}
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("invalid listen port")
	}
	if c.publicVisibility == nil {
		if c.dsnEnv != "" || c.authorityFile != "" {
			return errors.New("database and authority fixture require explicit synthetic visibility")
		}
		return nil
	}
	if c.dsnEnv == "" {
		return errors.New("synthetic mode requires a DSN environment variable")
	}
	for i, r := range c.dsnEnv {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9') {
			return errors.New("DSN must be an environment variable name")
		}
	}
	return nil
}
func loadAuthority(path string) (taxonomy.FixtureAuthority, error) {
	var a taxonomy.FixtureAuthority
	if path == "" {
		return a, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return a, errors.New("unable to open synthetic authority fixture")
	}
	defer file.Close()
	const maxAuthorityBytes = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(file, maxAuthorityBytes+1))
	if err != nil || len(raw) > maxAuthorityBytes {
		return a, errors.New("synthetic authority fixture exceeds size limit or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var decoded *taxonomy.FixtureAuthority
	if err = d.Decode(&decoded); err != nil || decoded == nil {
		return a, errors.New("invalid synthetic authority fixture")
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return a, errors.New("invalid synthetic authority fixture")
	}
	a = *decoded
	if err = a.Validate(); err != nil {
		return a, errors.New("invalid synthetic authority fixture")
	}
	return a, nil
}
func makeHandler(ctx context.Context, c config, getenv func(string) string, open func(string, string) (*sql.DB, error)) (http.Handler, func(), error) {
	if err := c.validate(); err != nil {
		return nil, nil, err
	}
	if c.publicVisibility == nil {
		return taxonomy.NewHandler(nil), func() {}, nil
	}
	authority, err := loadAuthority(c.authorityFile)
	if err != nil {
		return nil, nil, err
	}
	dsn := getenv(c.dsnEnv)
	if dsn == "" {
		return nil, nil, errors.New("DSN environment variable is empty")
	}
	db, err := open("pgx", dsn)
	if err != nil {
		return nil, nil, errors.New("invalid PostgreSQL configuration (details suppressed)")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(15 * time.Minute)
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(ping)
	cancel()
	if err != nil {
		db.Close()
		return nil, nil, errors.New("PostgreSQL connection failed (details suppressed)")
	}
	svc, err := taxonomy.NewSyntheticService(taxonomypg.NewSyntheticRepository(db, *c.publicVisibility), authority, nil)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return taxonomy.NewSyntheticHandler(svc), func() { db.Close() }, nil
}
func run(ctx context.Context, c config) error {
	h, closeDB, err := makeHandler(ctx, c, os.Getenv, sql.Open)
	if err != nil {
		return err
	}
	defer closeDB()
	listener, err := net.Listen("tcp", c.listen)
	if err != nil {
		return errors.New("unable to listen on loopback address")
	}
	defer listener.Close()
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	log.Print("taxonomy production compatibility disabled; opt-in projection host is for disposable synthetic data only")
	log.Printf("Taxonomy listening on %s", listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("taxonomy HTTP server failed")
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			server.Close()
		}
		<-done
		return err
	}
}
func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
