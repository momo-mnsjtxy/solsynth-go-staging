// SPDX-License-Identifier: AGPL-3.0-only
package domaintrustpg

import (
	"context"
	"database/sql"
	d "solsynth.local/go-migration/ports/passport/domaintrust"
)

func (r *Repository) Evaluate(ctx context.Context, raw string) (d.Result, error) {
	return r.evaluate(ctx, raw, false)
}
func (r *Repository) Validate(ctx context.Context, raw string) (d.Result, error) {
	return r.evaluate(ctx, raw, true)
}
func (r *Repository) evaluate(ctx context.Context, raw string, metrics bool) (out d.Result, e error) {
	if r == nil || r.db == nil {
		return out, d.ErrUnavailable
	}
	a, e := d.Parse(raw)
	if e != nil {
		return d.InvalidResult(), nil
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = state(ctx, tx, metrics); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_domain_trust_rules WHERE deleted_at IS NULL AND is_active ORDER BY priority DESC,domain_pattern COLLATE "C",id LIMIT $1`, r.cfg.MaxRules+1)
	if e != nil {
		return out, e
	}
	rules := []d.Rule{}
	for rows.Next() {
		v, x := scan(rows)
		if x != nil {
			rows.Close()
			return out, x
		}
		rules = append(rules, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(rules) > r.cfg.MaxRules {
		return out, d.ErrCapacity
	}
	out, e = d.EvaluateRules(a, r.cfg.Policy, rules)
	if e != nil {
		return d.Result{}, e
	}
	if metrics {
		var exists bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_domain_trust_metrics WHERE domain=$1::text)`, a.Host).Scan(&exists); e != nil {
			return d.Result{}, e
		}
		if !exists {
			var count int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_domain_trust_metrics`).Scan(&count); e != nil {
				return d.Result{}, e
			}
			if count >= r.cfg.MaxMetricDomains {
				return d.Result{}, d.ErrCapacity
			}
		}
		blocked, verified := 0, 0
		if !out.IsAllowed {
			blocked = 1
		}
		if out.IsVerified {
			verified = 1
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_domain_trust_metrics(domain,check_count,blocked_count,verified_count,last_checked_at)VALUES($1::text,1,$2::bigint,$3::bigint,$4::timestamptz)ON CONFLICT(domain)DO UPDATE SET check_count=owned_domain_trust_metrics.check_count+1,blocked_count=owned_domain_trust_metrics.blocked_count+excluded.blocked_count,verified_count=owned_domain_trust_metrics.verified_count+excluded.verified_count,last_checked_at=GREATEST(owned_domain_trust_metrics.last_checked_at,excluded.last_checked_at)`, a.Host, blocked, verified, r.cfg.Now().UTC()); e != nil {
			return d.Result{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return d.Result{}, e
	}
	return out, nil
}
