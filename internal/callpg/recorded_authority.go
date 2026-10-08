// SPDX-License-Identifier: AGPL-3.0-only
package callpg

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/messager/calls"
)

// Every ongoing call is visited fairly by updated_at, including apparently
// healthy calls: canonical ancestor/punishment checks must not depend on the
// SQL prefilter knowing every current authority rule. This only removes an
// existing admission and never creates or restores one.
func (r *Repository) reconcileAuthority(ctx context.Context, tx *sql.Tx, cid string) error {
	// The room is already locked. Secondary account locks must never wait on a
	// writer holding an account while waiting for that room. NOWAIT rolls the
	// entire maintenance transition back; the next bounded pass retries it.
	locked, e := tx.QueryContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id IN(SELECT account_id FROM call_v1.members WHERE call_id=$1 AND state='admitted') ORDER BY id LIMIT 101 FOR UPDATE NOWAIT`, cid)
	if e != nil {
		return recordedLockError(e)
	}
	count := 0
	for locked.Next() {
		count++
		var id string
		if e = locked.Scan(&id); e != nil {
			locked.Close()
			return e
		}
	}
	e = locked.Err()
	locked.Close()
	if e != nil {
		return recordedLockError(e)
	}
	if count > 100 {
		return calls.ErrUnavailable
	}
	rows, e := tx.QueryContext(ctx, `SELECT identity::text,account_id::text,session_id::text,account_version,session_epoch FROM call_v1.members WHERE call_id=$1 AND state='admitted' ORDER BY identity LIMIT 101`, cid)
	if e != nil {
		return e
	}
	type capture struct {
		identity, actor, session string
		version, epoch           int64
	}
	list := []capture{}
	for rows.Next() {
		var v capture
		if e = rows.Scan(&v.identity, &v.actor, &v.session, &v.version, &v.epoch); e != nil {
			rows.Close()
			return e
		}
		list = append(list, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(list) > 100 {
		return calls.ErrUnavailable
	}
	for _, v := range list {
		ok, e := r.guard.StoredAuthenticationEligible(ctx, r.db, tx, v.actor, v.session, v.version, v.epoch)
		if e != nil {
			return e
		}
		if !ok {
			if _, e = tx.ExecContext(ctx, `UPDATE call_v1.members SET state='removing' WHERE call_id=$1 AND identity=$2 AND state='admitted'`, cid, v.identity); e != nil {
				return e
			}
		}
	}
	return nil
}

func recordedLockError(e error) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "55P03" {
		return calls.ErrConflict
	}
	return e
}
