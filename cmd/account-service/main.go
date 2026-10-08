// SPDX-License-Identifier: AGPL-3.0-only
// Explicit opt-in local host. No credentials, migrations or fixtures are created.
package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strconv"
	"syscall"
	"time"
)

type config struct {
	serve                                            bool
	listen, dsnEnv, keyFile, keyID, issuer, audience string
}

func parse(args []string) (config, error) {
	var c config
	f := flag.NewFlagSet("account-service", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVar(&c.serve, "serve", false, "explicitly start the service")
	f.StringVar(&c.listen, "listen", "127.0.0.1:8088", "loopback listen address")
	f.StringVar(&c.dsnEnv, "postgres-dsn-env", "", "name of injected database DSN environment variable")
	f.StringVar(&c.keyFile, "public-key-file", "", "explicit PEM public key file")
	f.StringVar(&c.keyID, "key-id", "", "trusted public key identifier")
	f.StringVar(&c.issuer, "issuer", "", "expected token issuer")
	f.StringVar(&c.audience, "audience", "", "expected token audience")
	if e := f.Parse(args); e != nil || f.NArg() != 0 {
		return c, errors.New("invalid options")
	}
	host, port, e := net.SplitHostPort(c.listen)
	ip := net.ParseIP(host)
	n, pe := strconv.Atoi(port)
	if e != nil || pe != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() {
		return c, errors.New("explicit loopback address and port required")
	}
	if c.serve && (c.dsnEnv == "" || c.keyFile == "" || c.keyID == "" || c.issuer == "" || c.audience == "") {
		return c, errors.New("serving requires explicit database and public trust configuration")
	}
	return c, nil
}
func publicKey(path string) (*rsa.PublicKey, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("cannot read configured public key")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 32769))
	if e != nil || len(b) > 32768 {
		return nil, errors.New("invalid public key size")
	}
	block, rest := pem.Decode(b)
	if block == nil || len(rest) != 0 {
		return nil, errors.New("one public key PEM required")
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if block.Type != "PUBLIC KEY" {
		return nil, errors.New("only public key PEM is supported")
	}
	v, e := x509.ParsePKIXPublicKey(block.Bytes)
	if e != nil {
		return nil, errors.New("invalid public key")
	}
	k, ok := v.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("RSA public key required")
	}
	return k, nil
}
func run(args []string) error {
	c, e := parse(args)
	if e != nil {
		return e
	}
	if !c.serve {
		return nil
	}
	dsn, ok := os.LookupEnv(c.dsnEnv)
	if !ok || dsn == "" {
		return errors.New("configured database environment variable is missing")
	}
	k, e := publicKey(c.keyFile)
	if e != nil {
		return e
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return errors.New("database configuration failed")
	}
	defer db.Close()
	verifier, e := authority.NewVerifier(authoritypg.New(db), authority.Config{Issuer: c.issuer, Audience: c.audience, TrustedKeys: map[string]*rsa.PublicKey{c.keyID: k}})
	if e != nil {
		return errors.New("invalid public trust configuration")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e = db.PingContext(check); e != nil {
		return errors.New("database unavailable")
	}
	service := &accounts.Service{Repository: accountpg.New(db), Authority: verifier}
	server := &http.Server{Addr: c.listen, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = server.Shutdown(shutdown)
	}()
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	if e != nil {
		return errors.New("account service stopped")
	}
	return nil
}
func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
