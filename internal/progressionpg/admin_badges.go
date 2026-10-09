// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"time"
)

// AdministrativeBadgeRowsInTransaction is the canonical redacted badge view.
// The administrative caller supplies a target account lock and current global
// authority; these helpers never create or infer permissions or credentials.
func AdministrativeBadgeRowsInTransaction(ctx context.Context, tx *sql.Tx, target string, now time.Time) (out []progression.Badge, e error) {
	out = []progression.Badge{}
	rows, e := tx.QueryContext(ctx, `SELECT `+badgeColumns+` FROM owned_progression_badges WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC,id`, target)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanBadge(rows)
		if e != nil {
			return nil, e
		}
		if v.ExpiredAt != nil && !now.Before(*v.ExpiredAt) {
			v.ActivatedAt = nil
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Account locks serialize every canonical badge writer, including earned
// grants. Lock rows in a stable order before revalidating the caller's proof.
func LockAccountBadgesInTransaction(ctx context.Context, tx *sql.Tx, target string) error {
	if tx == nil || !authority.ValidID(target) {
		return authority.ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_progression_badges WHERE account_id=$1 ORDER BY id FOR UPDATE`, target)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return e
		}
	}
	return rows.Err()
}
func GrantAdministrativeBadgeInTransaction(ctx context.Context, tx *sql.Tx, actor, target string, v progression.AdminBadge, now time.Time) (progression.Badge, error) {
	id, e := newID()
	if e != nil {
		return progression.Badge{}, e
	}
	meta := v.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	raw, e := json.Marshal(meta)
	if e != nil {
		return progression.Badge{}, e
	}
	if v.ActivatedAt != nil {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=NULL,updated_at=$2 WHERE account_id=$1 AND activated_at IS NOT NULL`, target, now); e != nil {
			return progression.Badge{}, e
		}
	}
	return scanBadge(tx.QueryRowContext(ctx, `INSERT INTO owned_progression_badges(id,account_id,type,label,caption,meta,expired_at,activated_at,created_at,updated_at,origin,granted_by)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,'admin',$10)RETURNING `+badgeColumns, id, target, v.Type, v.Label, v.Caption, raw, v.ExpiredAt, v.ActivatedAt, now, actor))
}
func ReadAdministrativeBadgeInTransaction(ctx context.Context, tx *sql.Tx, target, id string) (progression.Badge, error) {
	return scanBadge(tx.QueryRowContext(ctx, `SELECT `+badgeColumns+` FROM owned_progression_badges WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL`, target, id))
}
func ActivateAdministrativeBadgeInTransaction(ctx context.Context, tx *sql.Tx, target, id string, now time.Time) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=NULL,updated_at=$2 WHERE account_id=$1 AND activated_at IS NOT NULL`, target, now); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=$3,updated_at=$3 WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL`, target, id, now)
	return e
}
func RevokeAdministrativeBadgeInTransaction(ctx context.Context, tx *sql.Tx, target, id string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `UPDATE owned_progression_badges SET activated_at=NULL,deleted_at=$3,updated_at=$3 WHERE account_id=$1 AND id=$2 AND deleted_at IS NULL`, target, id, now)
	return e
}
