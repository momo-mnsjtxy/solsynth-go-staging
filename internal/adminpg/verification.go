// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"database/sql"
	"encoding/json"
	c "solsynth.local/go-migration/ports/admin/content"
	v "solsynth.local/go-migration/ports/admin/verification"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"strings"
)

type target struct{ id, owner, realm string }

func observeVerification(ctx context.Context, tx *sql.Tx, kind v.Kind, ref string) (target, error) {
	var out target
	if ref == "" || len(ref) > 256 || strings.TrimSpace(ref) != ref {
		return out, c.ErrInvalid
	}
	var table, name, predicate string
	switch kind {
	case v.Account:
		table = "owned_authority_accounts"
		name = "name"
	case v.Publisher:
		table = "owned_publishers"
		name = "name"
	case v.Realm:
		table = "owned_realms"
		name = "slug"
	default:
		return out, c.ErrInvalid
	}
	predicate = `lower(` + name + `)=lower($1::text)`
	if authority.ValidID(ref) {
		predicate = `id=$1::uuid`
	}
	fields := `id::text,id::text,''`
	if kind == v.Publisher {
		fields = `id::text,COALESCE(account_id::text,''),COALESCE(realm_id::text,'')`
		predicate += ` AND deleted_at IS NULL`
	}
	if kind == v.Realm {
		fields = `id::text,account_id::text,id::text`
		predicate += ` AND deleted_at IS NULL`
	}
	e := tx.QueryRowContext(ctx, `SELECT `+fields+` FROM `+table+` WHERE `+predicate, ref).Scan(&out.id, &out.owner, &out.realm)
	return out, mapped(e)
}
func lockTarget(ctx context.Context, tx *sql.Tx, table, id string) error {
	if id == "" {
		return nil
	}
	var held string
	return mapped(tx.QueryRowContext(ctx, `SELECT id::text FROM `+table+` WHERE id=$1 FOR UPDATE`, id).Scan(&held))
}
func (r *Repository) SetVerification(ctx context.Context, actor string, kind v.Kind, ref string, mark *accounts.VerificationMark) (any, error) {
	permission := kind.Permission()
	if permission == "" || accounts.ValidateVerification(mark) != nil {
		return nil, c.ErrInvalid
	}
	var observed target
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, permission, func(tx *sql.Tx) ([]string, error) {
		var e error
		observed, e = observeVerification(ctx, tx, kind, ref)
		return []string{observed.owner}, e
	})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = lockTarget(ctx, tx, "owned_realms", observed.realm); e != nil {
		return nil, e
	}
	if kind == v.Publisher {
		if e = lockTarget(ctx, tx, "owned_publishers", observed.id); e != nil {
			return nil, e
		}
	}
	current, e := observeVerification(ctx, tx, kind, ref)
	if e != nil {
		return nil, e
	}
	if current != observed {
		return nil, c.ErrConflict
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		return nil, e
	}
	var payload any
	if mark != nil {
		raw, e := json.Marshal(mark)
		if e != nil {
			return nil, c.ErrInvalid
		}
		payload = string(raw)
	}
	now := r.now().UTC()
	switch kind {
	case v.Account:
		if mark == nil {
			_, e = tx.ExecContext(ctx, `UPDATE owned_accounts_profiles SET verification=NULL,updated_at=$2 WHERE account_id=$1`, current.id, now)
		} else {
			var disabled bool
			if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1`, current.id).Scan(&disabled); e != nil {
				return nil, e
			}
			if disabled {
				return nil, c.ErrConflict
			}
			id, e := newID()
			if e != nil {
				return nil, e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_accounts_profiles(account_id,id,verification,updated_at)VALUES($1,$2,$3::jsonb,$4)ON CONFLICT(account_id)DO UPDATE SET verification=EXCLUDED.verification,updated_at=EXCLUDED.updated_at`, current.id, id, payload, now)
			if e != nil {
				return nil, e
			}
		}
	case v.Publisher:
		_, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET verification=$2::jsonb,updated_at=$3 WHERE id=$1`, current.id, payload, now)
	case v.Realm:
		_, e = tx.ExecContext(ctx, `UPDATE owned_realms SET verification=$2::jsonb,updated_at=$3 WHERE id=$1`, current.id, payload, now)
	}
	if e != nil {
		return nil, e
	}
	action := "verification.set"
	if mark == nil {
		action = "verification.clear"
	}
	if e = r.audit(ctx, tx, actor, string(kind), current.id, action, map[string]any{"verification": mark}); e != nil {
		return nil, e
	}
	var out any = mark
	if kind == v.Publisher {
		p, e := r.global.InspectionInTransaction(ctx, tx, actor, c.PublisherKind, current.id)
		if e != nil {
			return nil, e
		}
		out = p.(c.PublisherDetail).Publisher
	}
	if kind == v.Realm {
		p, e := r.global.InspectionInTransaction(ctx, tx, actor, c.RealmKind, current.id)
		if e != nil {
			return nil, e
		}
		out = p.(c.RealmDetail).Realm
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
