// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"solsynth.local/go-migration/internal/federationpg"
	"time"
)

// Explicit offline validation by default; --apply initializes only the permanent
// server URI/key fingerprint/handle after the operator applies the SQL schema.
func initializeFederationServer(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("initialize-federation-server", flag.ContinueOnError)
	fs.SetOutput(out)
	file := fs.String("federation-config", "", "absolute explicit federation JSON configuration")
	apply := fs.Bool("apply", false, "connect and initialize the permanent server identity; never creates keys")
	dsnEnv := fs.String("dsn-env", "SOLSYNTH_BACKEND_DSN", "explicit database URL environment variable")
	timeout := fs.Duration("timeout", 30*time.Second, "bounded database operation timeout")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *dsnEnv == "" || *timeout < time.Second || *timeout > time.Minute {
		return errors.New("invalid server identity initialization arguments")
	}
	cfg, e := loadFederationConfig(*file, false)
	if e != nil {
		return e
	}
	if cfg.Discovery.Server == nil {
		return errors.New("server_actor configuration required")
	}
	if !*apply {
		fmt.Fprintln(out, "Server identity configuration valid; no database connection or network discovery made.")
		return nil
	}
	if e = ambientPostgres(); e != nil {
		return e
	}
	dsn, e := explicitDSN(os.Getenv(*dsnEnv))
	if e != nil {
		return e
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return errors.New("server identity database configuration unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	repo, e := federationpg.NewWithDiscovery(db, cfg.Policy, cfg.Keys, nil, cfg.Discovery)
	if e != nil {
		return e
	}
	if e = repo.InitializeServerIdentity(ctx); e != nil {
		return errors.New("server identity rejected; verify explicit schema, immutable origin/key/handle, and publisher name collision")
	}
	fmt.Fprintln(out, "Server actor identity initialized or already matched; federation remains explicitly configured.")
	return nil
}
