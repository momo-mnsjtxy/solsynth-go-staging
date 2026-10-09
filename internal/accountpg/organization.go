// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/passport/accounts"
)

func (r *Repository) CreateOrganizationPublisher(ctx context.Context, actor, realm string, patch accounts.PublisherPatch) (out accounts.Publisher, err error) {
	if e := accounts.ValidatePublisher(patch, true); e != nil {
		return out, e
	}
	if r == nil || r.realms == nil {
		return out, accounts.ErrUnsupported
	}
	err = r.write(ctx, actor, "publishers.create", func(tx *sql.Tx, now time.Time) error {
		realmID, e := r.realms.ResolveRealmForPublisher(ctx, tx, actor, realm)
		if e != nil {
			return e
		}
		var total, used int64
		e = tx.QueryRowContext(ctx, `SELECT total FROM owned_publisher_quota WHERE account_id=$1 AND starts_at<=$2 AND(expires_at IS NULL OR expires_at>$2) FOR UPDATE`, actor, now).Scan(&total)
		if errors.Is(e, sql.ErrNoRows) {
			return accounts.ErrPolicy
		}
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, publisherQuotaUsed, actor).Scan(&used); e != nil {
			return e
		}
		if used >= total {
			return accounts.ErrQuota
		}
		id, e := newID()
		if e != nil {
			return e
		}
		mid, e := newID()
		if e != nil {
			return e
		}
		out = accounts.Publisher{ID: id, Type: 1, Name: *patch.Name, Nick: *patch.Nick, RealmID: &realmID, CreatedAt: now, UpdatedAt: now}
		if patch.Bio != nil {
			out.Bio = *patch.Bio
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publishers(id,type,name,nick,bio,realm_id,created_at,updated_at) VALUES($1,1,$2,$3,$4,$5,$6,$6)`, id, out.Name, out.Nick, out.Bio, realmID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5,$5)`, mid, id, actor, accounts.Owner, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publishers.create", id, now)
	})
	if err != nil {
		return accounts.Publisher{}, err
	}
	return
}

// The creator is the explicit Publisher Owner. Realm membership alone does not
// consume another account's quota or grant publisher ownership.
const publisherQuotaUsed = `SELECT count(*) FROM owned_publishers p WHERE p.deleted_at IS NULL AND (p.account_id=$1 OR(p.type=1 AND EXISTS(SELECT 1 FROM owned_publisher_members m WHERE m.publisher_id=p.id AND m.account_id=$1 AND m.role=200 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL)))`
