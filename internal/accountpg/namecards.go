// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"time"
)

// These helpers compose a paid-card transaction; they never own commit or an
// entitlement. The caller must consume its verified card in the same transaction.
func (r *Repository) RenameAccountForCard(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, name string, now time.Time) (string, error) {
	if r == nil || r.db != db || tx == nil || !r.requireAuthority || r.guard == nil {
		return "", accounts.ErrUnavailable
	}
	q := namecards.UseRequest{Target: "account", NewName: name}
	if e := namecards.ValidateUse(&q); e != nil {
		return "", e
	}
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "accounts.manage"); e != nil {
		return "", e
	}
	var old string
	if e := tx.QueryRowContext(ctx, `SELECT name FROM owned_authority_accounts WHERE id=$1::uuid AND NOT disabled FOR UPDATE`, actor).Scan(&old); e != nil {
		return "", mapped(e)
	}
	if old == name {
		return "", namecards.ErrUse
	}
	if _, e := tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET name=$2::text WHERE id=$1::uuid`, actor, name); e != nil {
		return "", mapped(e)
	}
	// A conflicting-name wait must not extend the proof's authority. Advance
	// the canonical version only after this final check succeeds.
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "accounts.manage"); e != nil {
		return "", e
	}
	if _, e := tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1::uuid`, actor); e != nil {
		return "", e
	}
	return old, audit(ctx, tx, actor, "accounts.name.card", actor, now)
}
func (r *Repository) RenamePublisherForCard(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, id, name string, now time.Time) (string, error) {
	if r == nil || r.db != db || tx == nil || !r.requireAuthority || r.guard == nil {
		return "", accounts.ErrUnavailable
	}
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "publishers.update"); e != nil {
		return "", e
	}
	p, e := r.lockPublisher(ctx, tx, actor, id)
	if e != nil {
		return "", e
	}
	if e = accounts.ValidatePublisher(accounts.PublisherPatch{Name: &name, Nick: &p.Nick}, true); e != nil {
		return "", e
	}
	if e = r.require(ctx, tx, id, actor, accounts.Manager); e != nil {
		return "", e
	}
	if p.Name == name {
		return "", namecards.ErrUse
	}
	var federation sql.NullString
	if e = tx.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_publishers')::text`).Scan(&federation); e != nil {
		return "", e
	}
	if federation.Valid {
		var registered bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_publishers WHERE publisher_id=$1::uuid)`, id).Scan(&registered); e != nil {
			return "", e
		}
		if registered {
			return "", namecards.ErrActorMigration
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET name=$2::text,updated_at=$3::timestamptz WHERE id=$1::uuid`, id, name, now); e != nil {
		return "", mapped(e)
	}
	if _, e = r.guard.Authorize(ctx, db, tx, actor, "publishers.update"); e != nil {
		return "", e
	}
	if e = r.require(ctx, tx, id, actor, accounts.Manager); e != nil {
		return "", e
	}
	return p.Name, audit(ctx, tx, actor, "publishers.name.card", id, now)
}
