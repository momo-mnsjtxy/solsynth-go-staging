// SPDX-License-Identifier: AGPL-3.0-only
// Independently owned wallet runtime. This command never generates credentials,
// enrolls accounts, applies schema, imports legacy data, or contacts a provider.
package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"syscall"
	"time"
)

func publicKey(path string) (*rsa.PublicKey, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("trusted public key file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 32769))
	if e != nil || len(b) > 32768 {
		return nil, errors.New("invalid public key file")
	}
	block, rest := pem.Decode(b)
	if block == nil || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errors.New("one public key PEM required")
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	if block.Type != "PUBLIC KEY" {
		return nil, errors.New("public key PEM required; private keys and certificates are not accepted")
	}
	key, e := x509.ParsePKIXPublicKey(block.Bytes)
	if e != nil {
		return nil, errors.New("invalid public key")
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("RSA public key required")
	}
	return rsaKey, nil
}
func loopback(addr string) bool {
	host, port, e := net.SplitHostPort(addr)
	if e != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Suppress ambient pgpass/service/client-key files. Credentials are read solely
// from the explicitly named DSN environment variable, never logged.
func explicitDSN(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", errors.New("explicit PostgreSQL URL with user, host and database required")
	}
	q := u.Query()
	for key, values := range q {
		if len(values) != 1 || (key != "sslmode" && key != "connect_timeout" && key != "host") {
			return "", errors.New("unsupported database URL options")
		}
	}
	host := u.Hostname()
	if override := q.Get("host"); override != "" {
		if !strings.HasPrefix(override, "/") {
			return "", errors.New("host override is only for an explicit Unix socket directory")
		}
		host = override
	}
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "verify-full")
	}
	switch q.Get("sslmode") {
	case "verify-full":
	case "disable":
		ip := net.ParseIP(host)
		if host != "localhost" && !strings.HasPrefix(host, "/") && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("unencrypted database connection is allowed only for local disposable environments")
		}
	default:
		return "", errors.New("database TLS must verify the server identity")
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
func authorityError(e error) error {
	var f *authority.Failure
	if errors.As(e, &f) {
		switch f.Status {
		case 401:
			return accounts.ErrUnauthorized
		case 403, 423:
			return accounts.ErrForbidden
		case 503:
			return accounts.ErrUnavailable
		}
	}
	return accounts.ErrUnavailable
}
func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("wallet-service", flag.ContinueOnError)
	flags.SetOutput(out)
	listen := flags.String("listen", "127.0.0.1:8094", "loopback bind behind an authenticated TLS ingress")
	dsnEnv := flags.String("dsn-env", "SOLSYNTH_WALLET_DSN", "environment variable holding explicit PostgreSQL URL")
	issuer := flags.String("issuer", "", "exact trusted issuer")
	audience := flags.String("audience", "", "exact trusted audience")
	keyID := flags.String("key-id", "", "trusted public key ID")
	keyPath := flags.String("trusted-public-key-file", "", "RSA public PEM; never a private key")
	pepperEnv := flags.String("pin-pepper-env", "", "optional explicit environment variable holding base64 32+ byte PIN pepper")
	pepperID := flags.String("pin-pepper-id", "", "PIN pepper key ID matching the provisioned policies")
	accessTTL := flags.Duration("max-access-lifetime", 10*time.Minute, "maximum signed access-token lifetime; match issuer")
	interval := flags.Duration("settlement-interval", 15*time.Minute, "bounded pending/frozen settlement interval")
	serve := flags.Bool("serve", false, "explicitly connect and serve; default only validates configuration")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 || !loopback(*listen) || *issuer == "" || *audience == "" || *keyID == "" || *interval < time.Second {
		return errors.New("loopback listen address and explicit authority trust settings are required")
	}
	sanitized, e := explicitDSN(os.Getenv(*dsnEnv))
	if e != nil {
		return e
	}
	key, e := publicKey(*keyPath)
	if e != nil {
		return e
	}
	config := authority.Config{Issuer: *issuer, Audience: *audience, TrustedKeys: map[string]*rsa.PublicKey{*keyID: key}, AccessTTL: *accessTTL}
	if _, e = authority.NewVerifier(authoritypg.New(nil), config); e != nil {
		return errors.New("invalid authority trust configuration")
	}
	peppers := map[string][]byte{}
	if (*pepperEnv == "") != (*pepperID == "") {
		return errors.New("both PIN pepper environment name and key ID are required")
	}
	if *pepperEnv != "" {
		b, e := base64.StdEncoding.DecodeString(os.Getenv(*pepperEnv))
		if e != nil || len(b) < 32 || len(*pepperID) > 64 {
			return errors.New("invalid externally supplied PIN pepper")
		}
		peppers[*pepperID] = b
		defer clear(b)
	}
	if !*serve {
		_, e = fmt.Fprintln(out, "Wallet configuration validated. No database connection, account, key, schema, listener or transfer was created.")
		return e
	}
	db, e := sql.Open("pgx", sanitized)
	if e != nil {
		return errors.New("database configuration invalid")
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ping, cancel := context.WithTimeout(ctx, 5*time.Second)
	e = db.PingContext(ping)
	cancel()
	if e != nil {
		return errors.New("database unavailable")
	}
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		return errors.New("authority verifier unavailable")
	}
	repository := walletpg.New(db)
	handler := accounts.NewAuthorityHandler(repository, verifier, func(p authority.Principal) accounts.Principal {
		return accounts.Principal{AccountID: p.Account.ID, SessionID: p.Session.ID}
	}, walletpg.NewRealmAuthority(db), walletpg.NewPINVerifier(db, peppers), authorityError)
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				job, cancel := context.WithTimeout(ctx, 20*time.Second)
				_, err := repository.ProcessDue(job, 100)
				cancel()
				if err != nil {
					fmt.Fprintln(out, "Wallet settlement batch failed; committed items remain durable and pending work will retry.")
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return errors.New("wallet listener stopped unexpectedly")
}
func main() {
	if e := run(os.Args[1:], os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "wallet-service:", e)
		os.Exit(1)
	}
}
