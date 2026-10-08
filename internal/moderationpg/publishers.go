// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"database/sql"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/passport/accounts"
)

func (r *Repository) mutatePublisher(ctx context.Context, actor, ref string, cmd c.Command) (any, error) {
	switch cmd.Action {
	case "update":
		if cmd.Publisher == nil {
			return nil, c.ErrInvalid
		}
		v := cmd.Publisher
		if v.GatekeptFollows != nil || v.ModerateSubscription != nil {
			return nil, c.ErrUnsupported
		}
		nick := v.Nick
		if nick == nil {
			fallback := "Name"
			nick = &fallback
		}
		if e := accounts.ValidatePublisher(accounts.PublisherPatch{Name: v.Name, Nick: nick, Bio: v.Bio}, v.Name != nil); e != nil {
			return nil, c.ErrInvalid
		}
	case "shadowban":
		if cmd.Reason == nil || !c.ValidReason(*cmd.Reason, false) {
			return nil, c.ErrInvalid
		}
	case "unshadowban", "delete":
	default:
		return nil, c.ErrInvalid
	}
	var observed c.Publisher
	permission := c.PublisherKind.Permission(cmd.Action)
	tx, e := r.begin(ctx, actor, permission, true, func(tx *sql.Tx) ([]string, error) {
		var e error
		observed, e = r.publisher(ctx, tx, ref)
		return []string{ptr(observed.AccountID)}, e
	})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = lockIDs(ctx, tx, "owned_realms", []string{ptr(observed.RealmID)}); e != nil {
		return nil, e
	}
	if e = lockIDs(ctx, tx, "owned_publishers", []string{observed.ID}); e != nil {
		return nil, e
	}
	p, e := r.publisher(ctx, tx, ref)
	if e != nil {
		return nil, e
	}
	if p.ID != observed.ID || !same(p.RealmID, observed.RealmID) || !same(p.AccountID, observed.AccountID) {
		return nil, c.ErrConflict
	}
	if e = r.authorize(ctx, tx, actor, permission, true); e != nil {
		return nil, e
	}
	now := r.config.Now().UTC()
	before := map[string]any{"name": p.Name, "shadowban_reason": p.ShadowbanReason}
	var deleted any
	switch cmd.Action {
	case "update":
		v := cmd.Publisher
		if v.Name != nil && *v.Name != p.Name {
			var installed bool
			if e = tx.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_publishers')IS NOT NULL`).Scan(&installed); e != nil {
				return nil, e
			}
			if installed {
				var registered bool
				if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_publishers WHERE publisher_id=$1)`, p.ID).Scan(&registered); e != nil {
					return nil, e
				}
				if registered {
					return nil, c.ErrConflict
				}
			}
			p.Name = *v.Name
		}
		if v.Nick != nil {
			p.Nick = *v.Nick
		}
		if v.Bio != nil {
			p.Bio = *v.Bio
		}
	case "shadowban":
		p.ShadowbanReason = *cmd.Reason
		p.ShadowbannedAt = &now
	case "unshadowban":
		p.ShadowbanReason = "none"
		p.ShadowbannedAt = nil
	case "delete":
		deleted = now
	}
	p.UpdatedAt = now
	_, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET name=$2,nick=$3,bio=$4,shadowban_reason=NULLIF($5::text,'none'),shadowbanned_at=$6,deleted_at=$7,updated_at=$8 WHERE id=$1`, p.ID, p.Name, p.Nick, p.Bio, p.ShadowbanReason, p.ShadowbannedAt, deleted, now)
	if e != nil {
		return nil, mapped(e)
	}
	if cmd.Action == "delete" {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$2,updated_at=$2 WHERE publisher_id=$1 AND revoked_at IS NULL`, p.ID, now); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_publishing_settings WHERE default_publisher_id=$1`, p.ID); e != nil {
			return nil, e
		}
	}
	if e = r.audit(ctx, tx, actor, c.PublisherKind, p.ID, cmd.Action, map[string]any{"before": before, "name": p.Name, "shadowban_reason": p.ShadowbanReason}); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_accounts_audit(actor_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4)`, actor, "admin.publishers."+cmd.Action, p.ID, now); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return p, nil
}
