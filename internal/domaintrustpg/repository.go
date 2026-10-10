// SPDX-License-Identifier: AGPL-3.0-only
package domaintrustpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/moderationpg"
	"solsynth.local/go-migration/ports/authority"
	d "solsynth.local/go-migration/ports/passport/domaintrust"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Policy                     d.Policy
	MaxRules, MaxMetricDomains int
	Now                        func() time.Time
}
type Repository struct {
	db     *sql.DB
	global *moderationpg.Repository
	cfg    Config
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || guard == nil {
		return nil, d.ErrUnavailable
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxRules == 0 {
		cfg.MaxRules = 1000
	}
	if cfg.MaxRules < 1 || cfg.MaxRules > 1000 {
		return nil, d.ErrInvalid
	}
	if cfg.MaxMetricDomains == 0 {
		cfg.MaxMetricDomains = 10000
	}
	if cfg.MaxMetricDomains < 1 || cfg.MaxMetricDomains > 100000 {
		return nil, d.ErrInvalid
	}
	if cfg.Policy.BlockedPorts != nil {
		cfg.Policy.BlockedPorts = append([]int{}, cfg.Policy.BlockedPorts...)
		for _, p := range cfg.Policy.BlockedPorts {
			if p < 1 || p > 65535 {
				return nil, d.ErrInvalid
			}
		}
	}
	g, e := moderationpg.New(db, guard, moderationpg.Config{Now: cfg.Now})
	if e != nil {
		return nil, e
	}
	return &Repository{db, g, cfg}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return d.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT generation FROM owned_domain_trust_state LIMIT 0;SELECT domain_pattern FROM owned_domain_trust_rules LIMIT 0;SELECT domain FROM owned_domain_trust_metrics LIMIT 0;SELECT rule_id FROM owned_domain_trust_audit LIMIT 0`)
	return e
}
func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func missing(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return d.ErrNotFound
	}
	return e
}
func state(ctx context.Context, tx *sql.Tx, write bool) error {
	suffix := " FOR SHARE"
	if write {
		suffix = " FOR UPDATE"
	}
	var generation int64
	return tx.QueryRowContext(ctx, `SELECT generation FROM owned_domain_trust_state WHERE singleton`+suffix).Scan(&generation)
}
func (r *Repository) begin(ctx context.Context, actor, permission string, write bool) (*sql.Tx, error) {
	if r == nil {
		return nil, d.ErrUnavailable
	}
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, permission, nil)
	if e != nil {
		return nil, e
	}
	if e = state(ctx, tx, write); e != nil {
		tx.Rollback()
		return nil, e
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func (r *Repository) commit(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if e := r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		return e
	}
	return tx.Commit()
}

const fields = `id::text,domain_pattern,protocol,port_restriction,reason,priority,trust_level,is_active,created_by_account_id::text,created_at,updated_at,deleted_at`

func scan(row interface{ Scan(...any) error }) (v d.Rule, e error) {
	e = row.Scan(&v.ID, &v.DomainPattern, &v.Protocol, &v.PortRestriction, &v.Reason, &v.Priority, &v.TrustLevel, &v.IsActive, &v.CreatedByAccountID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	return v, missing(e)
}
func rule(ctx context.Context, tx *sql.Tx, rid string) (d.Rule, error) {
	if !authority.ValidID(rid) {
		return d.Rule{}, d.ErrNotFound
	}
	return scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_domain_trust_rules WHERE id=$1::uuid AND deleted_at IS NULL`, rid))
}
func (r *Repository) List(ctx context.Context, actor string, p d.Page) (out []d.Rule, total int, e error) {
	out = []d.Rule{}
	p = p.Bounds()
	tx, e := r.begin(ctx, actor, "domain.trust.validate", false)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_domain_trust_rules WHERE deleted_at IS NULL`).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_domain_trust_rules WHERE deleted_at IS NULL ORDER BY priority DESC,domain_pattern COLLATE "C",id OFFSET $1 LIMIT $2`, p.Offset, p.Limit)
	if e != nil {
		return
	}
	for rows.Next() {
		v, x := scan(rows)
		if x != nil {
			rows.Close()
			return out, 0, x
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "domain.trust.validate")
	return
}
func (r *Repository) Get(ctx context.Context, actor, rid string) (out d.Rule, e error) {
	tx, e := r.begin(ctx, actor, "domain.trust.validate", false)
	if e != nil {
		return
	}
	defer tx.Rollback()
	out, e = rule(ctx, tx, rid)
	if e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "domain.trust.validate")
	return
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor, action string, v d.Rule) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_domain_trust_audit(actor_id,rule_id,action,detail,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::jsonb,$5::timestamptz)`, actor, v.ID, action, b, r.cfg.Now().UTC())
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_domain_trust_state SET generation=generation+1 WHERE singleton`)
	return e
}
func (r *Repository) Create(ctx context.Context, actor string, q d.Create) (out d.Rule, e error) {
	active := true
	if q.IsActive != nil {
		active = *q.IsActive
	}
	out, e = d.ValidateRule(d.Rule{DomainPattern: q.DomainPattern, Protocol: q.Protocol, PortRestriction: q.PortRestriction, Reason: q.Reason, Priority: q.Priority, TrustLevel: q.TrustLevel, IsActive: active})
	if e != nil {
		return
	}
	tx, e := r.begin(ctx, actor, "domain.trust.create", true)
	if e != nil {
		return
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_domain_trust_rules WHERE deleted_at IS NULL`).Scan(&count); e != nil {
		return
	}
	if count >= r.cfg.MaxRules {
		return out, d.ErrCapacity
	}
	out.ID, e = id()
	if e != nil {
		return
	}
	out.CreatedAt = r.cfg.Now().UTC()
	out.UpdatedAt = out.CreatedAt
	out.CreatedByAccountID = &actor
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_domain_trust_rules(id,domain_pattern,protocol,port_restriction,reason,priority,trust_level,is_active,created_by_account_id,created_at,updated_at)VALUES($1::uuid,$2::text,$3::text,$4::integer,$5::text,$6::integer,$7::integer,$8::boolean,$9::uuid,$10::timestamptz,$10::timestamptz)`, out.ID, out.DomainPattern, out.Protocol, out.PortRestriction, out.Reason, out.Priority, out.TrustLevel, out.IsActive, actor, out.CreatedAt)
	if e != nil {
		return
	}
	if e = r.audit(ctx, tx, actor, "create", out); e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "domain.trust.create")
	return
}
func (r *Repository) Update(ctx context.Context, actor, rid string, q d.Patch) (out d.Rule, e error) {
	tx, e := r.begin(ctx, actor, "domain.trust.update", true)
	if e != nil {
		return
	}
	defer tx.Rollback()
	out, e = rule(ctx, tx, rid)
	if e != nil {
		return
	}
	if q.DomainPattern != nil {
		out.DomainPattern = *q.DomainPattern
	}
	if q.Protocol != nil {
		out.Protocol = q.Protocol
	}
	if q.PortRestriction != nil {
		out.PortRestriction = q.PortRestriction
	}
	if q.Reason != nil {
		out.Reason = q.Reason
	}
	if q.Priority != nil {
		out.Priority = *q.Priority
	}
	if q.TrustLevel != nil {
		out.TrustLevel = *q.TrustLevel
	}
	if q.IsActive != nil {
		out.IsActive = *q.IsActive
	}
	out, e = d.ValidateRule(out)
	if e != nil {
		return
	}
	out.UpdatedAt = r.cfg.Now().UTC()
	_, e = tx.ExecContext(ctx, `UPDATE owned_domain_trust_rules SET domain_pattern=$2::text,protocol=$3::text,port_restriction=$4::integer,reason=$5::text,priority=$6::integer,trust_level=$7::integer,is_active=$8::boolean,updated_at=$9::timestamptz WHERE id=$1::uuid`, rid, out.DomainPattern, out.Protocol, out.PortRestriction, out.Reason, out.Priority, out.TrustLevel, out.IsActive, out.UpdatedAt)
	if e != nil {
		return
	}
	if e = r.audit(ctx, tx, actor, "update", out); e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "domain.trust.update")
	return
}
func (r *Repository) Delete(ctx context.Context, actor, rid string) error {
	tx, e := r.begin(ctx, actor, "domain.trust.delete", true)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := rule(ctx, tx, rid)
	if e != nil {
		return e
	}
	at := r.cfg.Now().UTC()
	v.DeletedAt = &at
	v.UpdatedAt = at
	if _, e = tx.ExecContext(ctx, `UPDATE owned_domain_trust_rules SET deleted_at=$2::timestamptz,updated_at=$2::timestamptz,is_active=false WHERE id=$1::uuid`, rid, at); e != nil {
		return e
	}
	if e = r.audit(ctx, tx, actor, "delete", v); e != nil {
		return e
	}
	return r.commit(ctx, tx, actor, "domain.trust.delete")
}
func (r *Repository) Metrics(ctx context.Context, actor string, p d.Page) (out []d.Metric, total int, e error) {
	out = []d.Metric{}
	p = p.Bounds()
	tx, e := r.begin(ctx, actor, "domain.trust.validate", false)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_domain_trust_metrics`).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT domain,check_count,blocked_count,verified_count,last_checked_at FROM owned_domain_trust_metrics ORDER BY check_count DESC,domain COLLATE "C" OFFSET $1 LIMIT $2`, p.Offset, p.Limit)
	if e != nil {
		return
	}
	for rows.Next() {
		var m d.Metric
		if e = rows.Scan(&m.Domain, &m.CheckCount, &m.BlockedCount, &m.VerifiedCount, &m.LastCheckedAt); e != nil {
			rows.Close()
			return
		}
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "domain.trust.validate")
	return
}
func (r *Repository) Metric(ctx context.Context, actor, host string) (out d.Metric, e error) {
	normalized, _, e := d.NormalizeHost(host)
	if e != nil {
		return out, d.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "domain.trust.validate", false)
	if e != nil {
		return
	}
	defer tx.Rollback()
	e = tx.QueryRowContext(ctx, `SELECT domain,check_count,blocked_count,verified_count,last_checked_at FROM owned_domain_trust_metrics WHERE domain=$1::text`, normalized).Scan(&out.Domain, &out.CheckCount, &out.BlockedCount, &out.VerifiedCount, &out.LastCheckedAt)
	if e != nil {
		return out, missing(e)
	}
	e = r.commit(ctx, tx, actor, "domain.trust.validate")
	return
}

func (r *Repository) PurgeMetrics(ctx context.Context, actor string, q d.MetricMaintenance) (out d.MetricPurgeResult, e error) {
	if q.Limit < 1 || q.Limit > 10000 || q.OlderThan.IsZero() || q.OlderThan.After(r.cfg.Now().UTC().Add(-time.Hour)) {
		return out, d.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, "domain.trust.metrics.purge", true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `DELETE FROM owned_domain_trust_metrics WHERE domain IN(SELECT domain FROM owned_domain_trust_metrics WHERE last_checked_at<$1::timestamptz ORDER BY last_checked_at,domain COLLATE "C" LIMIT $2::integer FOR UPDATE SKIP LOCKED)`, q.OlderThan.UTC(), q.Limit)
	if e != nil {
		return out, e
	}
	out.Purged, e = res.RowsAffected()
	if e != nil {
		return out, e
	}
	e = r.commit(ctx, tx, actor, "domain.trust.metrics.purge")
	return out, e
}

// PurgeMetricsDue is host-policy retention. It accepts no user identity and is
// reachable only from the explicitly configured backend maintenance worker.
func (r *Repository) PurgeMetricsDue(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if r == nil || r.db == nil || retention < time.Hour || retention > 10*365*24*time.Hour || limit < 1 || limit > 10000 {
		return 0, d.ErrInvalid
	}
	result, e := r.db.ExecContext(ctx, `DELETE FROM owned_domain_trust_metrics WHERE domain IN(SELECT domain FROM owned_domain_trust_metrics WHERE last_checked_at<clock_timestamp()-($1::bigint*interval '1 microsecond') ORDER BY last_checked_at,domain COLLATE "C" LIMIT $2::integer FOR UPDATE SKIP LOCKED)`, retention.Microseconds(), limit)
	if e != nil {
		return 0, e
	}
	return result.RowsAffected()
}

var _ d.Repository = (*Repository)(nil)
