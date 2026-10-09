// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
)

func tag(row interface{ Scan(...any) error }) (locations.Tag, string, error) {
	var t locations.Tag
	var owner string
	e := row.Scan(&t.ID, &owner, &t.UID, &t.Label, &t.Active, &t.CreatedAt)
	return t, owner, mapped(e)
}

const tagFields = `id::text,account_id::text,uid,label,is_active,created_at`

func (r *Repository) RegisterTag(ctx context.Context, actor string, v locations.RegisterTag) (locations.Tag, error) {
	normalized, e := locations.NormalizeUID(v.UID)
	if e != nil || !locations.Text(v.Label, 64) {
		return locations.Tag{}, locations.ErrInvalid
	}
	tx, p, e := r.begin(ctx, actor, "nfc.admin.manage", true)
	if e != nil {
		return locations.Tag{}, e
	}
	defer tx.Rollback()
	if r.guard == nil && !p.Account.Superuser {
		return locations.Tag{}, locations.ErrForbidden
	}
	if e = lockNFCUID(ctx, tx, normalized); e != nil {
		return locations.Tag{}, e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_encrypted_nfc_tags WHERE uid=$1)`, normalized).Scan(&exists); e != nil {
		return locations.Tag{}, e
	}
	if exists {
		return locations.Tag{}, locations.ErrConflict
	}
	id, e := uid()
	if e != nil {
		return locations.Tag{}, e
	}
	now := r.now().UTC()
	out, _, e := tag(tx.QueryRowContext(ctx, `INSERT INTO owned_nfc_tags(id,account_id,uid,label,is_active,created_at,updated_at) VALUES($1,$2,$3,$4,true,$5,$5) RETURNING `+tagFields, id, actor, normalized, v.Label, now))
	if e != nil {
		return locations.Tag{}, e
	}
	if e = audit(ctx, tx, actor, id, "nfc.register", now); e != nil {
		return locations.Tag{}, e
	}
	if e = r.recheck(ctx, tx, actor, "nfc.admin.manage"); e != nil {
		return locations.Tag{}, e
	}
	return out, tx.Commit()
}
func (r *Repository) Tags(ctx context.Context, actor string) ([]locations.Tag, error) {
	tx, _, e := r.begin(ctx, actor, "", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+tagFields+` FROM owned_nfc_tags WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC,id`, actor)
	if e != nil {
		return nil, e
	}
	out := []locations.Tag{}
	for rows.Next() {
		v, _, e := tag(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if e = r.recheck(ctx, tx, actor, ""); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	encrypted, e := r.encryptedTags(ctx, actor, false)
	if e != nil {
		return nil, e
	}
	for _, v := range encrypted {
		out = append(out, v.Tag)
	}
	return out, nil
}
func (r *Repository) UpdateTag(ctx context.Context, actor, id string, v locations.TagPatch) (locations.Tag, error) {
	if !authority.ValidID(id) || !locations.Text(v.Label, 64) {
		return locations.Tag{}, locations.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, "nfc.tags.update", true)
	if e != nil {
		return locations.Tag{}, e
	}
	defer tx.Rollback()
	t, _, e := tag(tx.QueryRowContext(ctx, `SELECT `+tagFields+` FROM owned_nfc_tags WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, actor))
	if e == locations.ErrNotFound {
		tx.Rollback()
		return r.ChangeEncryptedTag(ctx, actor, id, v, false)
	}
	if e != nil {
		return t, e
	}
	if v.Label != nil {
		t.Label = v.Label
	}
	if v.Active != nil {
		t.Active = *v.Active
	}
	now := r.now().UTC()
	if _, e = tx.ExecContext(ctx, `UPDATE owned_nfc_tags SET label=$2,is_active=$3,updated_at=$4 WHERE id=$1`, id, t.Label, t.Active, now); e != nil {
		return locations.Tag{}, e
	}
	if e = audit(ctx, tx, actor, id, "nfc.update", now); e != nil {
		return locations.Tag{}, e
	}
	return t, tx.Commit()
}
func (r *Repository) DeleteTag(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return locations.ErrNotFound
	}
	tx, _, e := r.begin(ctx, actor, "nfc.tags.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	res, e := tx.ExecContext(ctx, `UPDATE owned_nfc_tags SET is_active=false,deleted_at=$3,updated_at=$3 WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, actor, now)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		tx.Rollback()
		_, e = r.ChangeEncryptedTag(ctx, actor, id, locations.TagPatch{}, true)
		return e
	}
	if e = audit(ctx, tx, actor, id, "nfc.delete", now); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) ResolveTag(ctx context.Context, actor, selector string, byID bool) (locations.TagResolution, error) {
	var out locations.TagResolution
	var e error
	if byID {
		if !authority.ValidID(selector) {
			return out, locations.ErrNotFound
		}
	} else {
		selector, e = locations.NormalizeUID(selector)
		if e != nil {
			return out, e
		}
	}
	tx, _, e := r.begin(ctx, actor, "", false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	where := "uid=$1"
	if byID {
		where = "id::text=$1"
	}
	t, owner, e := tag(tx.QueryRowContext(ctx, `SELECT `+tagFields+` FROM owned_nfc_tags WHERE `+where+` AND is_active AND deleted_at IS NULL FOR SHARE`, selector))
	if e != nil {
		return out, e
	}
	blocked, friend, e := socialState(ctx, tx, actor, owner)
	if e != nil {
		return out, e
	}
	if blocked {
		return out, locations.ErrNotFound
	}
	a, e := account(ctx, tx, owner)
	if e != nil {
		return out, e
	}
	out = locations.TagResolution{ID: t.ID, Account: a, Friend: friend && actor != owner, Claimed: true, Actions: []string{"view_profile"}}
	if actor != "" && actor != owner && !friend {
		out.Actions = append(out.Actions, "add_friend")
	}
	return out, tx.Commit()
}
