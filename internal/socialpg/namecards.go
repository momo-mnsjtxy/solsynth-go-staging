// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/social"
	"time"
)

func (r *Repository) RenameRealmForCard(ctx context.Context, db *sql.DB, tx *sql.Tx, actor, id, name string, now time.Time) (string, error) {
	if r == nil || r.db != db || tx == nil || !r.requireGuard || r.guard == nil {
		return "", social.ErrUnavailable
	}
	if e := social.ValidateRealm(social.RealmPatch{Slug: &name}, false); e != nil {
		return "", e
	}
	if _, e := r.guard.Authorize(ctx, db, tx, actor, "realms.update"); e != nil {
		return "", e
	}
	v, e := r.realm(ctx, tx, id, true)
	if e != nil {
		return "", e
	}
	if v.AccountID != actor {
		return "", authority.ErrForbidden
	}
	if _, e = requireRole(ctx, tx, id, actor, social.Owner); e != nil {
		return "", e
	}
	if v.Slug == name {
		return "", namecards.ErrUse
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_realms SET slug=$2::text,updated_at=$3::timestamptz WHERE id=$1::uuid`, id, name, now); e != nil {
		return "", mapped(e)
	}
	if _, e = r.guard.Authorize(ctx, db, tx, actor, "realms.update"); e != nil {
		return "", e
	}
	if _, e = requireRole(ctx, tx, id, actor, social.Owner); e != nil {
		return "", e
	}
	return v.Slug, audit(ctx, tx, actor, "realms.name.card", id, now)
}
