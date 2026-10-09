// SPDX-License-Identifier: AGPL-3.0-only
package realmmemberpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	m "solsynth.local/go-migration/ports/passport/realmmembers"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
	"time"
)

const labelColumns = `id::text,realm_id::text,name,description,color,icon,created_by_account_id::text,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func label(s scanner) (v social.RealmLabel, e error) {
	e = s.Scan(&v.ID, &v.RealmID, &v.Name, &v.Description, &v.Color, &v.Icon, &v.CreatedByAccountID, &v.CreatedAt, &v.UpdatedAt)
	return v, mapped(e)
}
func (r *Repository) Labels(ctx context.Context, actor, ref string) (out []social.RealmLabel, e error) {
	out = []social.RealmLabel{}
	tx, v, e := r.begin(ctx, actor, ref, false, "", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+labelColumns+` FROM owned_realm_labels WHERE realm_id=$1::uuid ORDER BY name,id`, v.ID)
	if e != nil {
		return
	}
	for rows.Next() {
		x, err := label(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	return out, tx.Commit()
}
func (r *Repository) CreateLabel(ctx context.Context, actor, ref, key string, q m.LabelRequest) (out social.RealmLabel, e error) {
	defer func() { e = mapped(e) }()
	if e = m.ValidateLabel(q); e != nil {
		return
	}
	if len(key) > 128 || strings.ContainsRune(key, 0) {
		return out, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, true, "realms.labels.manage", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	raw, _ := json.Marshal([]any{v.ID, q})
	h := sha256.Sum256(raw)
	fp := hex.EncodeToString(h[:])
	if key != "" {
		var oldRealm, oldFP, id string
		err := tx.QueryRowContext(ctx, `SELECT realm_id::text,fingerprint,label_id::text FROM owned_realm_label_requests WHERE actor_id=$1::uuid AND request_key=$2::text`, actor, key).Scan(&oldRealm, &oldFP, &id)
		if err == nil {
			if oldRealm != v.ID || oldFP != fp {
				return out, m.ErrConflict
			}
			out, e = label(tx.QueryRowContext(ctx, `SELECT `+labelColumns+` FROM owned_realm_labels WHERE realm_id=$1::uuid AND id=$2::uuid`, v.ID, id))
			if errors.Is(e, m.ErrNotFound) {
				e = m.ErrConflict
			}
			if e != nil {
				return
			}
			if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
				return
			}
			return out, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
	}
	_, cap, e := r.boosts.LabelPolicyInTransaction(ctx, tx, v.ID)
	if e != nil {
		return
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_realm_labels WHERE realm_id=$1::uuid`, v.ID).Scan(&count); e != nil {
		return
	}
	if count >= cap {
		return out, m.ErrQuota
	}
	id, e := uid()
	if e != nil {
		return
	}
	now := r.cfg.Now().UTC().Truncate(time.Microsecond)
	out, e = label(tx.QueryRowContext(ctx, `INSERT INTO owned_realm_labels(id,realm_id,name,description,color,icon,created_by_account_id,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::text,$4::text,$5::text,$6::text,$7::uuid,$8::timestamptz,$8::timestamptz)RETURNING `+labelColumns, id, v.ID, q.Name, q.Description, q.Color, q.Icon, actor, now))
	if e != nil {
		return
	}
	if key != "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_label_requests(actor_id,request_key,realm_id,fingerprint,label_id,created_at)VALUES($1::uuid,$2::text,$3::uuid,$4::text,$5::uuid,$6::timestamptz)`, actor, key, v.ID, fp, id, now)
		if e != nil {
			return
		}
	}
	if e = audit(ctx, tx, actor, v.ID, id, "label.created", now); e != nil {
		return
	}
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	_, cap, e = r.boosts.LabelPolicyInTransaction(ctx, tx, v.ID)
	if e != nil {
		return
	}
	if count+1 > cap {
		return out, m.ErrQuota
	}
	return out, tx.Commit()
}
func (r *Repository) UpdateLabel(ctx context.Context, actor, ref, id string, q m.LabelRequest) (out social.RealmLabel, e error) {
	defer func() { e = mapped(e) }()
	if !authority.ValidID(id) {
		return out, m.ErrInvalid
	}
	if e = m.ValidateLabel(q); e != nil {
		return
	}
	tx, v, e := r.begin(ctx, actor, ref, true, "realms.labels.manage", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	now := r.cfg.Now().UTC().Truncate(time.Microsecond)
	out, e = label(tx.QueryRowContext(ctx, `UPDATE owned_realm_labels SET name=$3::text,description=$4::text,color=$5::text,icon=$6::text,updated_at=$7::timestamptz WHERE realm_id=$1::uuid AND id=$2::uuid RETURNING `+labelColumns, v.ID, id, q.Name, q.Description, q.Color, q.Icon, now))
	if e != nil {
		return
	}
	if e = audit(ctx, tx, actor, v.ID, id, "label.updated", now); e != nil {
		return
	}
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	return out, tx.Commit()
}
func (r *Repository) DeleteLabel(ctx context.Context, actor, ref, id string) (e error) {
	defer func() { e = mapped(e) }()
	if !authority.ValidID(id) {
		return m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, true, "realms.labels.manage", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	result, e := tx.ExecContext(ctx, `DELETE FROM owned_realm_labels WHERE realm_id=$1::uuid AND id=$2::uuid`, v.ID, id)
	if e != nil {
		return
	}
	n, e := result.RowsAffected()
	if e != nil {
		return
	}
	if n == 0 {
		return m.ErrNotFound
	}
	if e = audit(ctx, tx, actor, v.ID, id, "label.deleted", r.cfg.Now().UTC()); e != nil {
		return
	}
	if e = r.authorize(ctx, tx, actor, v, "realms.labels.manage", true); e != nil {
		return
	}
	return tx.Commit()
}
func (r *Repository) AssignLabel(ctx context.Context, actor, ref, account string, id *string) (out social.Member, e error) {
	defer func() { e = mapped(e) }()
	if !authority.ValidID(account) || id != nil && !authority.ValidID(*id) {
		return out, m.ErrInvalid
	}
	tx, v, e := r.begin(ctx, actor, ref, true, "realms.members.manage", []string{account})
	if e != nil {
		return
	}
	defer tx.Rollback()
	if e = r.authorize(ctx, tx, actor, v, "realms.members.manage", true); e != nil {
		return
	}
	out, e = member(ctx, tx, v.ID, account)
	if e != nil {
		return
	}
	now := r.cfg.Now().UTC().Truncate(time.Microsecond)
	action := "label.cleared"
	if id == nil {
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_realm_label_assignments WHERE member_id=$1::uuid AND realm_id=$2::uuid`, out.ID, v.ID)
	} else {
		var exists bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_realm_labels WHERE id=$1::uuid AND realm_id=$2::uuid)`, *id, v.ID).Scan(&exists); e != nil {
			return
		}
		if !exists {
			return out, m.ErrLabelRealm
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_realm_label_assignments(member_id,realm_id,label_id,joined_at,assigned_by,updated_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::timestamptz,$5::uuid,$6::timestamptz)ON CONFLICT(member_id)DO UPDATE SET realm_id=EXCLUDED.realm_id,label_id=EXCLUDED.label_id,joined_at=EXCLUDED.joined_at,assigned_by=EXCLUDED.assigned_by,updated_at=EXCLUDED.updated_at`, out.ID, v.ID, *id, out.JoinedAt, actor, now)
		action = "label.assigned"
	}
	if e != nil {
		return
	}
	if e = audit(ctx, tx, actor, v.ID, out.ID, action, now); e != nil {
		return
	}
	if e = r.EnrichMemberInTransaction(ctx, tx, &out); e != nil {
		return
	}
	if e = currentAccount(ctx, tx, account); e != nil {
		return
	}
	if e = r.authorize(ctx, tx, actor, v, "realms.members.manage", true); e != nil {
		return
	}
	return out, tx.Commit()
}
