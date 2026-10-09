// SPDX-License-Identifier: AGPL-3.0-only
// The binary creates no schema, account, credentials or default keys. Serving
// must be requested explicitly with an injected trust/configuration set.
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
	"solsynth.local/go-migration/ports/authority"
)

func keyBytes(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("explicit key file required")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(b) > 32768 {
		return nil, errors.New("key file too large")
	}
	return b, nil
}
func loadSigningKey(path string) (*rsa.PrivateKey, error) {
	b, e := keyBytes(path)
	if e != nil {
		return nil, e
	}
	p, rest := pem.Decode(b)
	if p == nil || len(rest) != 0 {
		return nil, errors.New("one PEM private key required")
	}
	if p.Type == "RSA PRIVATE KEY" {
		return x509.ParsePKCS1PrivateKey(p.Bytes)
	}
	if p.Type != "PRIVATE KEY" {
		return nil, errors.New("unsupported signing key encoding")
	}
	k, e := x509.ParsePKCS8PrivateKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("RSA signing key required")
	}
	return r, nil
}
func loadPublicKey(path string) (*rsa.PublicKey, error) {
	b, e := keyBytes(path)
	if e != nil {
		return nil, e
	}
	p, rest := pem.Decode(b)
	if p == nil || len(rest) != 0 {
		return nil, errors.New("one PEM public key required")
	}
	if p.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(p.Bytes)
	}
	if p.Type != "PUBLIC KEY" {
		return nil, errors.New("public key PEM required; certificates are not accepted")
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

// Explicit connection settings override pgx's credential/certificate defaults.
// No pgpass, service file or user-home client certificate is read or trusted.
func explicitDSN(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return "", errors.New("explicit PostgreSQL URL with user, host and database required")
	}
	q := u.Query()
	for k := range q {
		if k != "sslmode" && k != "connect_timeout" {
			return "", errors.New("database URL accepts only sslmode and connect_timeout options")
		}
	}
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "verify-full")
	}
	if q.Get("sslmode") != "verify-full" && q.Get("sslmode") != "disable" {
		return "", errors.New("database TLS must verify identity; disable is for disposable local fixtures only")
	}
	password, _ := u.User.Password()
	q.Set("password", password)
	q.Set("passfile", os.DevNull)
	q.Set("servicefile", os.DevNull)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslrootcert", "")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("authority", flag.ContinueOnError)
	fs.SetOutput(out)
	dsn := fs.String("dsn", "", "explicit PostgreSQL URL with user, host and database")
	addr := fs.String("listen", "127.0.0.1:8091", "HTTP bind address behind trusted TLS ingress")
	serve := fs.Bool("serve", false, "explicitly start service (default validates configuration only)")
	issuer := fs.String("issuer", "", "trusted issuer")
	audience := fs.String("audience", "", "trusted audience")
	kid := fs.String("key-id", "", "explicit trusted signing-key ID")
	signing := fs.String("signing-key-file", "", "injected RSA private key PEM")
	public := fs.String("trusted-public-key-file", "", "injected matching RSA public key PEM")
	hash := fs.String("refresh-hash-key-file", "", "injected 32+ raw random bytes")
	seal := fs.String("replay-seal-key-file", "", "injected independent 32 raw random bytes")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *dsn == "" || *issuer == "" || *audience == "" || *kid == "" {
		return errors.New("explicit DSN, issuer, audience and key ID required; no development credentials are discovered")
	}
	private, e := loadSigningKey(*signing)
	if e != nil {
		return e
	}
	pub, e := loadPublicKey(*public)
	if e != nil {
		return e
	}
	hashKey, e := keyBytes(*hash)
	if e != nil {
		return e
	}
	sealKey, e := keyBytes(*seal)
	if e != nil {
		return e
	}
	config := authority.Config{Issuer: *issuer, Audience: *audience, SigningKeyID: *kid, SigningKey: private, TrustedKeys: map[string]*rsa.PublicKey{*kid: pub}, RefreshHashKey: hashKey, ReplaySealKey: sealKey, RefreshGrace: 60 * time.Second}
	if _, e = authority.New(authoritypg.New(nil), config); e != nil {
		return e
	}
	sanitized, e := explicitDSN(*dsn)
	if e != nil {
		return e
	}
	if !*serve {
		_, e = fmt.Fprintln(out, "Authority configuration validated; no database connection, schema writes or listener started.")
		return e
	}
	db, e := sql.Open("pgx", sanitized)
	if e != nil {
		return errors.New("invalid database configuration")
	}
	defer db.Close()
	s, e := authority.New(authoritypg.New(db), config)
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
	server := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(end)
	}()
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	if e := run(os.Args[1:], os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "authority:", e)
		os.Exit(1)
	}
}
