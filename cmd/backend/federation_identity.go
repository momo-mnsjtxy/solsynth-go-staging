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
	"time"

	"solsynth.local/go-migration/internal/federationpg"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

// This operator-only command is separate from normal server startup. It uses
// the existing explicit DSN policy and never reads signing keys or starts I/O
// until --apply is supplied. It does not install schemas or enable federation.
func initializeFederationIdentities(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("initialize-federation-identities", flag.ContinueOnError)
	fs.SetOutput(out)
	origin := fs.String("origin", "", "exact existing canonical HTTPS federation origin")
	dsnEnv := fs.String("dsn-env", "SOLSYNTH_BACKEND_DSN", "explicit database URL environment variable")
	apply := fs.Bool("apply", false, "explicitly initialize one bounded batch; default validates only")
	publisher := fs.String("publisher", "", "optional exact publisher UUID instead of a batch")
	limit := fs.Int("limit", 100, "maximum identities in one batch, from 1 to 100")
	timeout := fs.Duration("timeout", 30*time.Second, "operation timeout, from 1 to 60 seconds")
	if e := fs.Parse(args); e != nil {
		return e
	}
	c := f.Config{Origin: *origin, MaxAttempts: 1, DeliveryTimeout: time.Second}
	if fs.NArg() != 0 || c.Validate() != nil || *limit < 1 || *limit > 100 || *timeout < time.Second || *timeout > time.Minute || *dsnEnv == "" || (*publisher != "" && !authority.ValidID(*publisher)) {
		return errors.New("explicit canonical origin, bounded batch/timeout and valid publisher UUID required")
	}
	if !*apply {
		fmt.Fprintln(out, "Federation identity initialization configuration valid; no database connection made.")
		return nil
	}
	if e := ambientPostgres(); e != nil {
		return e
	}
	dsn, e := explicitDSN(os.Getenv(*dsnEnv))
	if e != nil {
		return e
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return errors.New("federation initialization database configuration unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r, e := federationpg.New(db, c, nil, nil)
	if e != nil {
		return e
	}
	if e = r.Ready(ctx); e != nil {
		return errors.New("federation initialization schema prerequisites unavailable")
	}
	if *publisher != "" {
		if e = r.InitializeIdentity(ctx, *publisher); e != nil {
			return errors.New("federation identity initialization rejected; inspect configured origin and retained publisher identity")
		}
		fmt.Fprintln(out, "Publisher federation identity initialized or already matched.")
		return nil
	}
	n, e := r.InitializeIdentities(ctx, *limit)
	fmt.Fprintf(out, "Initialized %d federation identities in this batch.\n", n)
	if e != nil {
		return errors.New("federation identity batch rejected; prior committed identities remain anchored")
	}
	return nil
}
