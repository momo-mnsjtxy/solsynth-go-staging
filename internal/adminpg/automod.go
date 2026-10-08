// SPDX-License-Identifier: AGPL-3.0-only
package adminpg

import (
	"context"
	"database/sql"
	"errors"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/sphere/automod"
	"strings"
)

const ruleFields = `id::text,name,description,type,default_action,pattern,is_regex,derank_weight,is_enabled,priority,expires_at,created_at,updated_at`

func scanRule(s interface{ Scan(...any) error }) (a.Rule, error) {
	var v a.Rule
	e := s.Scan(&v.ID, &v.Name, &v.Description, &v.Type, &v.DefaultAction, &v.Pattern, &v.IsRegex, &v.DerankWeight, &v.IsEnabled, &v.Priority, &v.ExpiresAt, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = a.ErrNotFound
	}
	return v, e
}
func rule(ctx context.Context, tx *sql.Tx, id string, lock bool) (a.Rule, error) {
	if !authority.ValidID(id) {
		return a.Rule{}, c.ErrInvalid
	}
	q := `SELECT ` + ruleFields + ` FROM owned_automod_rules WHERE id=$1 AND deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanRule(tx.QueryRowContext(ctx, q, id))
}
func (r *Repository) Rules(ctx context.Context, actor string, enabled *bool) ([]a.Rule, error) {
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "automod.rules.manage", nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	only := enabled != nil && *enabled
	rows, e := tx.QueryContext(ctx, `SELECT `+ruleFields+` FROM owned_automod_rules WHERE deleted_at IS NULL AND(NOT $1::boolean OR(is_enabled AND(expires_at IS NULL OR expires_at>$2)))ORDER BY priority,name,id LIMIT 1001`, only, r.now().UTC())
	if e != nil {
		return nil, e
	}
	out := []a.Rule{}
	for rows.Next() {
		v, e := scanRule(rows)
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
	if len(out) > a.MaxRules {
		return nil, c.ErrUnavailable
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.manage"); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Rule(ctx context.Context, actor, id string) (a.Rule, error) {
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "automod.rules.manage", nil)
	if e != nil {
		return a.Rule{}, e
	}
	defer tx.Rollback()
	v, e := rule(ctx, tx, id, false)
	if e != nil {
		return v, e
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.manage"); e != nil {
		return a.Rule{}, e
	}
	return v, tx.Commit()
}
func (r *Repository) ruleWrite(ctx context.Context, actor string, fn func(*sql.Tx) error) error {
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "automod.rules.manage", nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var generation int64
	if e = tx.QueryRowContext(ctx, `SELECT generation FROM owned_automod_state WHERE singleton FOR UPDATE`).Scan(&generation); e != nil {
		return mapped(e)
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.manage"); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return mapped(e)
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.manage"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_automod_state SET generation=generation+1 WHERE singleton`); e != nil {
		return e
	}
	return tx.Commit()
}
func ruleCapacity(ctx context.Context, tx *sql.Tx) error {
	var total int
	if e := tx.QueryRowContext(ctx, `SELECT count(*)FROM owned_automod_rules WHERE deleted_at IS NULL`).Scan(&total); e != nil {
		return e
	}
	if total >= a.MaxRules {
		return c.ErrConflict
	}
	return nil
}
func (r *Repository) saveRule(ctx context.Context, tx *sql.Tx, actor string, v *a.Rule, create bool) error {
	now := r.now().UTC()
	v.UpdatedAt = now
	action := "automod.update"
	var e error
	if create {
		if e = ruleCapacity(ctx, tx); e != nil {
			return e
		}
		v.ID, e = newID()
		if e != nil {
			return e
		}
		v.CreatedAt = now
		action = "automod.create"
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_automod_rules(id,name,description,type,default_action,pattern,is_regex,derank_weight,is_enabled,priority,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`, v.ID, v.Name, v.Description, v.Type, v.DefaultAction, v.Pattern, v.IsRegex, v.DerankWeight, v.IsEnabled, v.Priority, now)
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE owned_automod_rules SET name=$2,description=$3,type=$4,default_action=$5,pattern=$6,is_regex=$7,derank_weight=$8,is_enabled=$9,priority=$10,updated_at=$11 WHERE id=$1`, v.ID, v.Name, v.Description, v.Type, v.DefaultAction, v.Pattern, v.IsRegex, v.DerankWeight, v.IsEnabled, v.Priority, now)
	}
	if e != nil {
		return mapped(e)
	}
	return r.audit(ctx, tx, actor, "automod", v.ID, action, map[string]any{"rule": v})
}
func (r *Repository) CreateRule(ctx context.Context, actor string, p a.Patch) (a.Rule, error) {
	v, e := p.Apply(a.Rule{}, true)
	if e != nil {
		return v, e
	}
	e = r.ruleWrite(ctx, actor, func(tx *sql.Tx) error { return r.saveRule(ctx, tx, actor, &v, true) })
	return v, e
}
func (r *Repository) UpdateRule(ctx context.Context, actor, id string, p a.Patch) (a.Rule, error) {
	var v a.Rule
	e := r.ruleWrite(ctx, actor, func(tx *sql.Tx) error {
		var e error
		v, e = rule(ctx, tx, id, true)
		if e != nil {
			return e
		}
		v, e = p.Apply(v, false)
		if e != nil {
			return e
		}
		return r.saveRule(ctx, tx, actor, &v, false)
	})
	return v, e
}
func (r *Repository) DeleteRule(ctx context.Context, actor, id string) error {
	return r.ruleWrite(ctx, actor, func(tx *sql.Tx) error {
		if _, e := rule(ctx, tx, id, true); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE owned_automod_rules SET deleted_at=$2,updated_at=$2 WHERE id=$1`, id, r.now().UTC())
		if e != nil {
			return e
		}
		return r.audit(ctx, tx, actor, "automod", id, "automod.delete", map[string]any{})
	})
}
func (r *Repository) BulkRules(ctx context.Context, actor string, patches []a.Patch) ([]a.Rule, error) {
	if len(patches) < 1 || len(patches) > a.MaxBulk {
		return nil, c.ErrInvalid
	}
	out := []a.Rule{}
	seen := map[string]bool{}
	for _, p := range patches {
		v, e := p.Apply(a.Rule{}, true)
		if e != nil {
			return nil, e
		}
		key := strings.ToLower(v.Name)
		if seen[key] {
			return nil, c.ErrInvalid
		}
		seen[key] = true
		out = append(out, v)
	}
	e := r.ruleWrite(ctx, actor, func(tx *sql.Tx) error {
		for i := range out {
			old, e := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleFields+` FROM owned_automod_rules WHERE lower(name)=lower($1)AND deleted_at IS NULL FOR UPDATE`, out[i].Name))
			create := errors.Is(e, a.ErrNotFound)
			if e != nil && !create {
				return e
			}
			if !create {
				out[i].ID = old.ID
				out[i].CreatedAt = old.CreatedAt
				out[i].ExpiresAt = old.ExpiresAt
				out[i].Name = old.Name
			}
			if e = r.saveRule(ctx, tx, actor, &out[i], create); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	return out, nil
}
func (r *Repository) TestRule(ctx context.Context, actor, id, content string) ([]a.Result, error) {
	if !authority.ValidID(id) || len(content) > a.MaxContentBytes {
		return nil, c.ErrInvalid
	}
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "automod.rules.test", nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	v, e := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleFields+` FROM owned_automod_rules WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id))
	if e != nil {
		return nil, e
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.test"); e != nil {
		return nil, e
	}
	out, e := a.Match(ctx, v, content)
	if e != nil {
		return nil, e
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, "automod.rules.test"); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}

var _ a.Repository = (*Repository)(nil)
