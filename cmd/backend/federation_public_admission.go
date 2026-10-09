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

// Explicit offline validation by default; --apply initializes immutable public
// origin policies after the operator applies the optional SQL migration.
func initializeFederationPublicAdmission(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("initialize-federation-public-admission", flag.ContinueOnError)
	fs.SetOutput(out)
	file := fs.String("federation-config", "", "absolute explicit federation JSON configuration")
	apply := fs.Bool("apply", false, "connect and initialize the permanent public admission policy; never creates keys")
	dsnEnv := fs.String("dsn-env", "SOLSYNTH_BACKEND_DSN", "explicit database URL environment variable")
	timeout := fs.Duration("timeout", 30*time.Second, "bounded database operation timeout")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *dsnEnv == "" || *timeout < time.Second || *timeout > time.Minute {
		return errors.New("invalid public admission policy initialization arguments")
	}
	cfg, e := loadFederationConfig(*file, false)
	if e != nil {
		return e
	}
	if len(cfg.PublicAdmission) == 0 {
		return errors.New("public_admission configuration required")
	}
	if !*apply {
		fmt.Fprintln(out, "Public admission policy configuration valid; no database connection or network discovery made.")
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
		return errors.New("public admission policy database configuration unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	repo, e := federationpg.NewWithDiscovery(db, cfg.Policy, cfg.Keys, nil, cfg.Discovery)
	if e != nil {
		return e
	}
	repo, e = repo.WithPublicAdmission(cfg.PublicAdmission)
	if e != nil {
		return e
	}
	if e = repo.InitializePublicAdmission(ctx); e != nil {
		return errors.New("public admission policy rejected; verify explicit schema, immutable origin/incarnation and prior revocation")
	}
	fmt.Fprintln(out, "Public admission policy initialized or already matched; federation remains explicitly configured.")
	return nil
}
