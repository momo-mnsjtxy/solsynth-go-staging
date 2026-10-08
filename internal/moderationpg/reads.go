// SPDX-License-Identifier: AGPL-3.0-only
package moderationpg

import (
	"context"
	"fmt"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	"strings"
)

func validateQuery(q c.Query) error {
	if q.Take < 1 || q.Take > 200 || q.Offset < 0 || q.Offset > 100000 || len(q.Text) > 1024 {
		return c.ErrInvalid
	}
	for _, id := range []string{q.PublisherID, q.RealmID, q.AccountID} {
		if id != "" && !authority.ValidID(id) {
			return c.ErrInvalid
		}
	}
	return nil
}

type filter struct {
	where []string
	args  []any
}

func (f *filter) add(expr string, v any) {
	f.args = append(f.args, v)
	f.where = append(f.where, fmt.Sprintf(expr, len(f.args)))
}
func (f *filter) sql() string { return strings.Join(f.where, " AND ") }
func (r *Repository) List(ctx context.Context, actor string, kind c.Kind, q c.Query) (any, int64, error) {
	if e := validateQuery(q); e != nil {
		return nil, 0, e
	}
	permission := kind.Permission("read")
	tx, e := r.begin(ctx, actor, permission, true, nil)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	f := filter{where: []string{"v.deleted_at IS NULL"}}
	var table string
	switch kind {
	case c.PostKind:
		table = "owned_text_posts"
		f.where = append(f.where, `EXISTS(SELECT 1 FROM owned_publishers publisher WHERE publisher.id=v.publisher_id AND publisher.deleted_at IS NULL)`)
		if q.Text != "" {
			f.add(`strpos(lower(v.content||' '||coalesce(v.title,'')),lower($%d::text))>0`, q.Text)
		}
		if q.PublisherID != "" {
			f.add(`v.publisher_id=$%d::uuid`, q.PublisherID)
		}
		if q.RealmID != "" {
			f.add(`v.realm_id=$%d::uuid`, q.RealmID)
		}
		if q.Visibility != nil {
			if *q.Visibility < 0 || *q.Visibility > 4 {
				return nil, 0, c.ErrInvalid
			}
			f.add(`v.visibility=$%d::integer`, *q.Visibility)
		}
		if q.Locked != nil {
			f.add(`(v.locked_at IS NOT NULL)=$%d::boolean`, *q.Locked)
		}
		if q.Drafted != nil {
			f.add(`(v.drafted_at IS NOT NULL)=$%d::boolean`, *q.Drafted)
		}
	case c.PublisherKind:
		table = "owned_publishers"
		if q.Text != "" {
			f.add(`strpos(lower(v.name||' '||v.nick),lower($%d::text))>0`, q.Text)
		}
		if q.AccountID != "" {
			f.add(`v.account_id=$%d::uuid`, q.AccountID)
		}
		if q.Type != nil {
			if *q.Type < 0 || *q.Type > 1 {
				return nil, 0, c.ErrInvalid
			}
			f.add(`v.type=$%d::integer`, *q.Type)
		}
	case c.RealmKind:
		table = "owned_realms"
		if q.Text != "" {
			f.add(`strpos(lower(v.slug||' '||v.name),lower($%d::text))>0`, q.Text)
		}
		if q.AccountID != "" {
			f.add(`v.account_id=$%d::uuid`, q.AccountID)
		}
		if q.Public != nil {
			f.add(`v.is_public=$%d::boolean`, *q.Public)
		}
		if q.Community != nil {
			f.add(`v.is_community=$%d::boolean`, *q.Community)
		}
	default:
		return nil, 0, c.ErrInvalid
	}
	if kind != c.PostKind && q.Verified != nil {
		f.add(`(v.verification IS NOT NULL)=$%d::boolean`, *q.Verified)
	}
	if kind != c.RealmKind {
		if q.Banned != nil {
			f.add(`(v.shadowbanned_at IS NOT NULL)=$%d::boolean`, *q.Banned)
		}
		if q.Reason != nil {
			if !c.ValidReason(*q.Reason, true) {
				return nil, 0, c.ErrInvalid
			}
			f.add(`COALESCE(v.shadowban_reason,'none')=$%d::text`, *q.Reason)
		}
	}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)FROM `+table+` v WHERE `+f.sql(), f.args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	args := append(append([]any{}, f.args...), q.Take, q.Offset)
	rows, e := tx.QueryContext(ctx, `SELECT v.id::text FROM `+table+` v WHERE `+f.sql()+fmt.Sprintf(` ORDER BY v.created_at DESC,v.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if e != nil {
		return nil, 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	out := []any{}
	for _, id := range ids {
		v, e := r.get(ctx, tx, kind, id)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, v)
	}
	if e = r.authorize(ctx, tx, actor, permission, true); e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func (r *Repository) Members(ctx context.Context, actor, ref string, q c.Query) ([]social.Member, int64, error) {
	out := []social.Member{}
	if e := validateQuery(q); e != nil {
		return out, 0, e
	}
	permission := c.RealmKind.Permission("read")
	tx, e := r.begin(ctx, actor, permission, true, nil)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	realm, e := r.realm(ctx, tx, ref)
	if e != nil {
		return out, 0, e
	}
	f := filter{where: []string{"m.realm_id=$1::uuid", "m.leave_at IS NULL"}, args: []any{realm.ID}}
	if q.AccountID != "" {
		f.add(`m.account_id=$%d::uuid`, q.AccountID)
	}
	if q.Role != nil {
		if *q.Role != 0 && *q.Role != 50 && *q.Role != 100 {
			return out, 0, c.ErrInvalid
		}
		f.add(`m.role=$%d::integer`, *q.Role)
	}
	if q.Pending != nil && *q.Pending {
		f.where = append(f.where, `m.joined_at IS NULL`)
	} else {
		f.where = append(f.where, `m.joined_at IS NOT NULL`)
	}
	if q.Text != "" {
		f.add(`EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=m.account_id AND strpos(lower(a.name),lower($%d::text))>0)`, q.Text)
	}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)FROM owned_realm_members m WHERE `+f.sql(), f.args...).Scan(&total); e != nil {
		return out, 0, e
	}
	args := append(append([]any{}, f.args...), q.Take, q.Offset)
	rows, e := tx.QueryContext(ctx, `SELECT m.account_id::text FROM owned_realm_members m WHERE `+f.sql()+fmt.Sprintf(` ORDER BY m.created_at DESC,m.id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if e != nil {
		return out, 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, 0, e
	}
	for _, id := range ids {
		v, e := r.member(ctx, tx, actor, realm.ID, id)
		if e != nil {
			return out, 0, e
		}
		out = append(out, v)
	}
	if e = r.authorize(ctx, tx, actor, permission, true); e != nil {
		return out, 0, e
	}
	return out, total, tx.Commit()
}
