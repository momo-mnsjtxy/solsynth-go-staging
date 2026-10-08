// SPDX-License-Identifier: AGPL-3.0-only
package accountpg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"time"
)

func (r *Repository) CreatePublisher(ctx context.Context, actor string, patch accounts.PublisherPatch) (accounts.Publisher, error) {
	var p accounts.Publisher
	if e := accounts.ValidatePublisher(patch, true); e != nil {
		return p, e
	}
	e := r.write(ctx, actor, "publishers.create", func(tx *sql.Tx, now time.Time) error {
		var total, used int64
		e := tx.QueryRowContext(ctx, `SELECT total FROM owned_publisher_quota WHERE account_id=$1 AND starts_at<=$2 AND(expires_at IS NULL OR expires_at>$2) FOR UPDATE`, actor, now).Scan(&total)
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
		p = accounts.Publisher{ID: id, Type: 0, Name: *patch.Name, Nick: *patch.Nick, AccountID: &actor, CreatedAt: now, UpdatedAt: now}
		if patch.Bio != nil {
			p.Bio = *patch.Bio
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publishers(id,type,name,nick,bio,account_id,created_at,updated_at) VALUES($1,0,$2,$3,$4,$5,$6,$6)`, id, p.Name, p.Nick, p.Bio, actor, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5,$5)`, mid, id, actor, accounts.Owner, now); e != nil {
			return e
		}
		if e = hydrate(ctx, tx, &p); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publishers.create", id, now)
	})
	return p, e
}
func (r *Repository) UpdatePublisher(ctx context.Context, actor, ref string, patch accounts.PublisherPatch) (accounts.Publisher, error) {
	var p accounts.Publisher
	if e := accounts.ValidatePublisher(patch, false); e != nil {
		return p, e
	}
	e := r.write(ctx, actor, "publishers.update", func(tx *sql.Tx, now time.Time) error {
		var e error
		p, e = r.lockPublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if e = r.require(ctx, tx, p.ID, actor, accounts.Manager); e != nil {
			return e
		}
		if patch.Nick != nil {
			p.Nick = *patch.Nick
		}
		if patch.Bio != nil {
			p.Bio = *patch.Bio
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET nick=$2,bio=$3,updated_at=$4 WHERE id=$1`, p.ID, p.Nick, p.Bio, now); e != nil {
			return e
		}
		p.UpdatedAt = now
		if e = hydrate(ctx, tx, &p); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publishers.update", p.ID, now)
	})
	return p, e
}
func (r *Repository) DeletePublisher(ctx context.Context, actor, ref string) error {
	return r.write(ctx, actor, "publishers.delete", func(tx *sql.Tx, now time.Time) error {
		p, e := r.lockPublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if e = r.require(ctx, tx, p.ID, actor, accounts.Owner); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publishers SET deleted_at=$2,updated_at=$2 WHERE id=$1`, p.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$2,updated_at=$2 WHERE publisher_id=$1 AND revoked_at IS NULL`, p.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_publishing_settings WHERE default_publisher_id=$1`, p.ID); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "publishers.delete", p.ID, now)
	})
}
func (r *Repository) ChangeMember(ctx context.Context, actor, ref string, c accounts.MemberChange) (accounts.Member, error) {
	var out accounts.Member
	if !authority.ValidID(actor) {
		return out, accounts.ErrForbidden
	}
	if c.Action == accounts.AcceptInvite || c.Action == accounts.DeclineInvite || c.Action == accounts.LeavePublisher {
		c.AccountID = actor
	}
	if !authority.ValidID(c.AccountID) {
		return out, accounts.ErrInvalid
	}
	if (c.Action == accounts.InviteMember || c.Action == accounts.UpdateRole) && (!c.Role.Valid() || c.Role == accounts.Owner) {
		return out, accounts.ErrInvalid
	}
	permission, e := memberPermission(c.Action)
	if e != nil {
		return out, e
	}
	e = r.write(ctx, actor, permission, func(tx *sql.Tx, now time.Time) error {
		p, e := r.lockPublisher(ctx, tx, actor, ref)
		if e != nil {
			return e
		}
		if p.Type == 1 && (c.Action == accounts.InviteMember || c.Action == accounts.AcceptInvite || c.Action == accounts.UpdateRole) {
			var joined bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p WHERE p.id=$1 AND `+r.publisherMember("p", "$2")+`)`, p.ID, c.AccountID).Scan(&joined); e != nil {
				return e
			}
			if !joined {
				return accounts.ErrForbidden
			}
		}
		m, e := membership(ctx, tx, p.ID, c.AccountID)
		if e != nil && !errors.Is(e, accounts.ErrNotFound) {
			return e
		}
		found := e == nil
		switch c.Action {
		case accounts.InviteMember:
			if e = r.require(ctx, tx, p.ID, actor, accounts.Manager); e != nil {
				return e
			}
			if e = r.require(ctx, tx, p.ID, actor, c.Role); e != nil {
				return e
			}
			if found {
				return accounts.ErrConflict
			}
			if _, e = lookupAccount(ctx, tx, c.AccountID); e != nil {
				return e
			}
			id, e := newID()
			if e != nil {
				return e
			}
			m = accounts.Member{ID: id, PublisherID: p.ID, AccountID: c.AccountID, Role: c.Role, CreatedAt: now, UpdatedAt: now}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(publisher_id,account_id) DO UPDATE SET role=EXCLUDED.role,joined_at=NULL,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at,revoked_at=NULL WHERE owned_publisher_members.revoked_at IS NOT NULL`, id, p.ID, c.AccountID, c.Role, now); e != nil {
				return e
			}
			m, e = membership(ctx, tx, p.ID, c.AccountID)
			if e != nil {
				return e
			}
		case accounts.AcceptInvite:
			if !found || m.JoinedAt != nil {
				return accounts.ErrNotFound
			}
			m.JoinedAt = &now
			if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET joined_at=$3,updated_at=$3 WHERE publisher_id=$1 AND account_id=$2`, p.ID, actor, now); e != nil {
				return e
			}
		case accounts.DeclineInvite:
			if !found || m.JoinedAt != nil {
				return accounts.ErrNotFound
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$3,updated_at=$3 WHERE publisher_id=$1 AND account_id=$2`, p.ID, actor, now); e != nil {
				return e
			}
		case accounts.LeavePublisher, accounts.RemoveMember:
			if !found {
				return accounts.ErrNotFound
			}
			if m.Role == accounts.Owner {
				return accounts.ErrForbidden
			}
			if c.Action == accounts.LeavePublisher {
				if m.JoinedAt == nil {
					return accounts.ErrNotFound
				}
			} else {
				if e = r.require(ctx, tx, p.ID, actor, accounts.Manager); e != nil {
					return e
				}
				if e = r.require(ctx, tx, p.ID, actor, m.Role); e != nil {
					return e
				}
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=$3,updated_at=$3 WHERE publisher_id=$1 AND account_id=$2`, p.ID, c.AccountID, now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `DELETE FROM owned_publishing_settings WHERE account_id=$1 AND default_publisher_id=$2`, c.AccountID, p.ID); e != nil {
				return e
			}
		case accounts.UpdateRole:
			if !found || m.JoinedAt == nil {
				return accounts.ErrNotFound
			}
			if m.Role == accounts.Owner {
				return accounts.ErrForbidden
			}
			need := accounts.Manager
			if m.Role > need {
				need = m.Role
			}
			if c.Role > need {
				need = c.Role
			}
			if e = r.require(ctx, tx, p.ID, actor, need); e != nil {
				return e
			}
			m.Role = c.Role
			if _, e = tx.ExecContext(ctx, `UPDATE owned_publisher_members SET role=$3,updated_at=$4 WHERE publisher_id=$1 AND account_id=$2`, p.ID, c.AccountID, c.Role, now); e != nil {
				return e
			}
		default:
			return accounts.ErrInvalid
		}
		m.UpdatedAt = now
		out = m
		return audit(ctx, tx, actor, "publishers.members."+string(c.Action), p.ID, now)
	})
	return out, e
}

func memberPermission(action accounts.Mutation) (string, error) {
	switch action {
	case accounts.InviteMember, accounts.AcceptInvite, accounts.DeclineInvite:
		return "publishers.invites.manage", nil
	case accounts.RemoveMember, accounts.UpdateRole:
		return "publishers.members.manage", nil
	case accounts.LeavePublisher:
		return "", nil
	default:
		return "", accounts.ErrInvalid
	}
}
func (r *Repository) ListMembers(ctx context.Context, ref string, offset, take int) ([]accounts.Member, int64, error) {
	return r.ListMembersForViewer(ctx, "", ref, offset, take)
}
func (r *Repository) ListMembersForViewer(ctx context.Context, viewer, ref string, offset, take int) ([]accounts.Member, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, accounts.ErrUnavailable
	}
	if offset < 0 || take < 1 || take > 100 {
		return nil, 0, accounts.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if viewer != "" {
		if !authority.ValidID(viewer) {
			return nil, 0, accounts.ErrForbidden
		}
		if _, e = lookupAccount(ctx, tx, viewer); e != nil {
			return nil, 0, e
		}
	}
	p, e := r.visiblePublisher(ctx, tx, viewer, ref)
	if e != nil {
		return nil, 0, e
	}
	var total int64
	where := ` FROM owned_publisher_members m JOIN owned_authority_accounts a ON a.id=m.account_id JOIN owned_publishers p ON p.id=m.publisher_id WHERE m.publisher_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND NOT a.disabled AND ` + r.publisherMember("p", "m.account_id")
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, p.ID).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+memberColumns+where+` ORDER BY m.created_at,m.id OFFSET $2 LIMIT $3`, p.ID, offset, take)
	if e != nil {
		return nil, 0, e
	}
	out := []accounts.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		a, e := lookupAccount(ctx, tx, out[i].AccountID)
		if e != nil {
			return nil, 0, e
		}
		out[i].Account = &a
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
func (r *Repository) ListInvites(ctx context.Context, actor string) ([]accounts.Member, error) {
	if r == nil || r.db == nil {
		return nil, accounts.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, accounts.ErrInvalid
	}
	if _, e := r.LookupAccount(ctx, actor); e != nil {
		return nil, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT `+memberColumns+` FROM owned_publisher_members m JOIN owned_publishers p ON p.id=m.publisher_id WHERE m.account_id=$1 AND m.joined_at IS NULL AND m.revoked_at IS NULL AND p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND `+r.publisherMember("p", "$1")+` ORDER BY m.created_at,m.id`, actor)
	if e != nil {
		return nil, e
	}
	out := []accounts.Member{}
	for rows.Next() {
		m, e := scanMember(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		p, e := r.LookupPublisherForViewer(ctx, actor, out[i].PublisherID)
		if e != nil {
			return nil, e
		}
		out[i].Publisher = &p
	}
	return out, nil
}
