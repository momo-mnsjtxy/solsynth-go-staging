// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	if _, e = strconv.ParseUint(port, 10, 16); e != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pgx also consumes libpq environment settings. Reject ambient configuration
// rather than letting an unrelated shell silently change this explicit host.
func ambientPostgres() error {
	for _, name := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING", "PGREQUIREAUTH"} {
		if value := os.Getenv(name); value != "" {
			return errors.New("ambient PostgreSQL configuration is unsupported; use the explicit DSN environment variable")
		}
	}
	return nil
}

// Suppress ambient pgpass/service/client-key files. Credentials are read solely
// from the explicitly named DSN environment variable, never logged.
func explicitDSN(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || u.Hostname() == "" || strings.ContainsAny(u.Host, ", \t\r\n") || strings.Trim(u.Path, "/") == "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("explicit PostgreSQL URL with user, host and database required")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return "", errors.New("invalid database port")
		}
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return "", errors.New("invalid database URL options")
	}
	for key, values := range q {
		if len(values) != 1 || (key != "sslmode" && key != "connect_timeout" && key != "host") {
			return "", errors.New("unsupported database URL options")
		}
	}
	if timeout := q.Get("connect_timeout"); timeout != "" {
		if n, err := strconv.ParseUint(timeout, 10, 16); err != nil || n == 0 || n > 60 {
			return "", errors.New("database connection timeout must be 1 to 60 seconds")
		}
	} else {
		q.Set("connect_timeout", "5")
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
