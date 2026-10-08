// SPDX-License-Identifier: AGPL-3.0-only
package apppg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
)

// RequireClient joins app validation to the caller's READ COMMITTED transaction.
// The caller owns transaction lifetime and must call this again after any long
// waits immediately before applying its mutation, to enforce wall-clock expiry.
func (r *Repository) RequireClient(ctx context.Context, tx *sql.Tx, ref, token, permission string) (apps.Client, error) {
	var out apps.Client
	if r == nil || r.db == nil || tx == nil {
		return out, apps.ErrUnavailable
	}
	var isolation string
	if e := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); e != nil || isolation != "read committed" {
		return out, apps.ErrUnavailable
	}
	var app string
	query := `SELECT a.id::text` + appJoins + ` WHERE lower(a.slug)=lower($1) AND ` + activeApp
	if authority.ValidID(ref) {
		query = `SELECT a.id::text` + appJoins + ` WHERE a.id::text=$1 AND ` + activeApp
	}
	if e := tx.QueryRowContext(ctx, query, ref).Scan(&app); e != nil {
		return out, authority.ErrInvalid
	}
	if e := authenticateClient(ctx, tx, app, token, permission); e != nil {
		return out, e
	}
	a, e := activeClientByID(ctx, tx, app, false)
	if e != nil {
		return out, e
	}
	return apps.Client{AppID: a.ID, PublisherID: a.PublisherID}, nil
}

// LockProduct returns the exact owned product document and locks it against app
// management changes until the surrounding order/stock transaction commits.
func (r *Repository) LockProduct(ctx context.Context, tx *sql.Tx, app, identifier string) (apps.Product, error) {
	var out apps.Product
	if r == nil || tx == nil || !authority.ValidID(app) || !apps.ValidKey(identifier) {
		return out, apps.ErrInvalid
	}
	var raw []byte
	if e := tx.QueryRowContext(ctx, `SELECT document FROM owned_app_products WHERE app_id=$1 AND identifier=$2 AND deleted_at IS NULL FOR UPDATE`, app, identifier).Scan(&raw); e != nil {
		return out, mapped(e)
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	if !out.State.IsEnabled {
		return out, apps.ErrNotFound
	}
	return out, nil
}
