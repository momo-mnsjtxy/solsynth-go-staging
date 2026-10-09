// SPDX-License-Identifier: AGPL-3.0-only
// Explicit configuration only; this service never creates schema or credentials.
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
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func publicKey(path string) (*rsa.PublicKey, error) {
	if path == "" {
		return nil, errors.New("explicit public key file required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(b) > 32768 {
		return nil, errors.New("public key file too large")
	}
	p, rest := pem.Decode(b)
	if p == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("one PEM public key required")
	}
	if p.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(p.Bytes)
	}
	if p.Type != "PUBLIC KEY" {
		return nil, errors.New("public key PEM required")
	}
	k, e := x509.ParsePKIXPublicKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	r, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("RSA public key required")
	}
	return r, nil
}
func explicitDSN(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return "", errors.New("explicit PostgreSQL URL with user, host and database required")
	}
	q := u.Query()
	for k, v := range q {
		if len(v) != 1 || k != "sslmode" && k != "connect_timeout" {
			return "", errors.New("database URL accepts only sslmode and connect_timeout")
		}
	}
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "verify-full")
	}
	if q.Get("sslmode") != "verify-full" && q.Get("sslmode") != "disable" {
		return "", errors.New("database TLS identity verification required")
	}
	p, _ := u.User.Password()
	q.Set("password", p)
	q.Set("passfile", os.DevNull)
	q.Set("servicefile", os.DevNull)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslrootcert", "")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("post-service", flag.ContinueOnError)
	fs.SetOutput(out)
	dsn := fs.String("dsn", "", "explicit PostgreSQL URL")
	addr := fs.String("listen", "127.0.0.1:8094", "address behind TLS ingress")
	serve := fs.Bool("serve", false, "explicitly open database and listener")
	issuer := fs.String("issuer", "", "trusted authority issuer")
	audience := fs.String("audience", "", "trusted audience")
	kid := fs.String("key-id", "", "trusted key ID")
	keyFile := fs.String("trusted-public-key-file", "", "injected RSA public key PEM")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *dsn == "" || *issuer == "" || *audience == "" || *kid == "" {
		return errors.New("explicit DSN, issuer, audience and key ID required")
	}
	key, e := publicKey(*keyFile)
	if e != nil {
		return e
	}
	cfg := authority.Config{Issuer: *issuer, Audience: *audience, TrustedKeys: map[string]*rsa.PublicKey{*kid: key}}
	if _, e = authority.NewVerifier(authoritypg.New(nil), cfg); e != nil {
		return e
	}
	safe, e := explicitDSN(*dsn)
	if e != nil {
		return e
	}
	if !*serve {
		_, e = fmt.Fprintln(out, "Post configuration validated; no connection, migration, credentials or listener created.")
		return e
	}
	db, e := sql.Open("pgx", safe)
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	verifier, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = db.PingContext(ping)
	cancel()
	if e != nil {
		return errors.New("database unavailable")
	}
	server := &http.Server{Addr: *addr, Handler: posts.NewVerifiedHandler(postpg.New(db), verifier), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(end)
	}()
	if e = server.ListenAndServe(); !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return nil
}
func main() {
	if e := run(os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "post-service:", e)
		os.Exit(1)
	}
}
