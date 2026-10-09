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
	f "solsynth.local/go-migration/ports/federation"
	"time"
)

// Explicit offline validation by default. --apply revokes a durable key or
// origin policy without deleting any permanent identity reservation.
func revokeFederationPublicAdmission(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("revoke-federation-public-admission", flag.ContinueOnError)
	fs.SetOutput(out)
	file := fs.String("federation-config", "", "absolute explicit federation JSON configuration")
	origin := fs.String("origin", "", "exact configured public origin")
	actor := fs.String("actor", "", "exact admitted actor URI; empty revokes the origin policy")
	apply := fs.Bool("apply", false, "connect and revoke public admission; permanent reservations remain")
	dsnEnv := fs.String("dsn-env", "SOLSYNTH_BACKEND_DSN", "explicit database URL environment variable")
	timeout := fs.Duration("timeout", 30*time.Second, "bounded database operation timeout")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if fs.NArg() != 0 || *dsnEnv == "" || *timeout < time.Second || *timeout > time.Minute {
		return errors.New("invalid public admission revocation arguments")
	}
	cfg, e := loadFederationConfig(*file, false)
	if e != nil {
		return e
	}
	if len(cfg.PublicAdmission) == 0 {
		return errors.New("public_admission configuration required")
	}
	matched := false
	for _, p := range cfg.PublicAdmission {
		if p.Origin == *origin {
			matched = true
		}
	}
	if !matched {
		return errors.New("configured public admission origin required")
	}
	if *actor != "" {
		o, e := f.Origin(*actor)
		if e != nil || o != *origin {
			return errors.New("actor must belong to configured public origin")
		}
	}
	if !*apply {
		fmt.Fprintln(out, "Public admission revocation valid; no database connection or network discovery made.")
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
	if e = repo.RevokePublicAdmission(ctx, *origin, *actor); e != nil {
		return errors.New("public admission policy rejected; verify explicit schema, immutable origin/incarnation and prior revocation")
	}
	fmt.Fprintln(out, "Public admission revoked or already absent; permanent reservations retained.")
	return nil
}
