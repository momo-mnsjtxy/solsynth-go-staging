// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/domaintrustpg"
	admin "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	d "solsynth.local/go-migration/ports/passport/domaintrust"
	"strings"
	"sync"
	"testing"
	"time"
)

type domainFixture struct {
	*moderationFixture
	trust *domaintrustpg.Repository
	host  string
}

func newDomainFixture(t *testing.T) *domainFixture {
	t.Helper()
	f := &domainFixture{moderationFixture: newModerationFixture(t)}
	query(t, f.db, domaintrustpg.Schema)
	f.host = "synthetic-" + f.actor + ".test"
	// Cleanup is registered before writes and touches only this synthetic actor's
	// rule/audit rows and its unique host namespace; global fixture rows survive.
	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM owned_domain_trust_audit WHERE rule_id IN(SELECT id FROM owned_domain_trust_rules WHERE created_by_account_id=$1::uuid)`, f.outside)
		_, _ = f.db.Exec(`DELETE FROM owned_domain_trust_rules WHERE created_by_account_id=$1::uuid`, f.outside)
		_, _ = f.db.Exec(`DELETE FROM owned_domain_trust_metrics WHERE domain=$1::text OR domain LIKE $2::text`, f.host, "%."+f.host)
	})
	var e error
	f.trust, e = domaintrustpg.New(f.db, f.guard, domaintrustpg.Config{Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.trust.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *domainFixture) rule(t *testing.T, level int) d.Rule {
	t.Helper()
	r, e := f.trust.Create(f.outsideCtx, f.outside, d.Create{DomainPattern: f.host, TrustLevel: level, Priority: 100})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func domainConstraint(t *testing.T, e error, name string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != name {
		t.Fatal("expected exact rollback constraint", name, e)
	}
}
func domainWait(t *testing.T, db *sql.DB, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		e := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid))`, pid).Scan(&n)
		if e != nil {
			t.Fatal(e)
		}
		if n > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected lock wait", ctx.Err())
		case <-ticker.C:
		}
	}
}
func TestPGDomainTrustRuleLifecycleAndGlobalAuthority(t *testing.T) {
	f := newDomainFixture(t)
	if _, e := f.trust.Create(f.ctx, f.actor, d.Create{DomainPattern: f.host}); !errors.Is(e, admin.ErrForbidden) {
		t.Fatal("publisher owner became global administrator", e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}, {Pattern: "domain.trust.create", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.trust.Create(f.outsideCtx, f.outside, d.Create{DomainPattern: f.host}); e == nil {
		t.Fatal("explicit denial bypass")
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.outside, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	rule := f.rule(t, d.Blocked)
	if !rule.IsActive || rule.CreatedByAccountID == nil || *rule.CreatedByAccountID != f.outside {
		t.Fatal(rule)
	}
	if _, _, e := f.trust.List(f.ctx, f.actor, d.Page{}); !errors.Is(e, admin.ErrForbidden) {
		t.Fatal(e)
	}
	level := d.Verified
	updated, e := f.trust.Update(f.outsideCtx, f.outside, rule.ID, d.Patch{TrustLevel: &level})
	if e != nil || updated.TrustLevel != d.Verified {
		t.Fatal(updated, e)
	}
	if e = f.trust.Delete(f.outsideCtx, f.outside, rule.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.trust.Get(f.outsideCtx, f.outside, rule.ID); !errors.Is(e, d.ErrNotFound) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_domain_trust_audit WHERE rule_id=$1::uuid`, rule.ID); n != 3 {
		t.Fatal(n)
	}
}
func TestPGDomainTrustLocalEvaluationAndQueryPrivacy(t *testing.T) {
	f := newDomainFixture(t)
	f.rule(t, d.Verified)
	raw := "https://" + f.host + "/private/path?credential=SYNTHETIC#private-fragment"
	result, e := f.trust.Validate(f.ctx, raw)
	if e != nil || !result.IsAllowed || !result.IsVerified || result.MatchedRule != nil {
		t.Fatal(result, e)
	}
	metric, e := f.trust.Metric(f.outsideCtx, f.outside, strings.ToUpper(f.host))
	if e != nil || metric.Domain != f.host || metric.CheckCount != 1 || metric.VerifiedCount != 1 {
		t.Fatal(metric, e)
	}
	var doc string
	if e = f.db.QueryRow(`SELECT row_to_json(m)::text FROM owned_domain_trust_metrics m WHERE domain=$1::text`, f.host).Scan(&doc); e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"SYNTHETIC", "private/path", "credential", "private-fragment"} {
		if strings.Contains(doc, secret) {
			t.Fatal("raw URL persisted", doc)
		}
	}
	if _, _, e = f.trust.Metrics(f.ctx, f.actor, d.Page{}); !errors.Is(e, admin.ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = f.trust.Evaluate(f.ctx, raw); e != nil {
		t.Fatal(e)
	}
	metric, e = f.trust.Metric(f.outsideCtx, f.outside, f.host)
	if e != nil || metric.CheckCount != 1 {
		t.Fatal("typed evaluator recorded metrics", metric, e)
	}
	v, e := f.trust.Validate(f.ctx, "https://user:password@"+f.host)
	if e != nil || v.IsAllowed {
		t.Fatal(v, e)
	}
}
func TestPGDomainTrustPriorityDefaultsAndCurrentRules(t *testing.T) {
	f := newDomainFixture(t)
	low := f.rule(t, d.Verified)
	high, e := f.trust.Create(f.outsideCtx, f.outside, d.Create{DomainPattern: f.host, TrustLevel: d.Blocked, Priority: 200})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.trust.Evaluate(f.ctx, "https://"+f.host)
	if e != nil || v.IsAllowed {
		t.Fatal(v, e)
	}
	off := false
	if _, e = f.trust.Update(f.outsideCtx, f.outside, high.ID, d.Patch{IsActive: &off}); e != nil {
		t.Fatal(e)
	}
	v, e = f.trust.Evaluate(f.ctx, "https://"+f.host)
	if e != nil || !v.IsVerified {
		t.Fatal(v, e)
	}
	v, e = f.trust.Evaluate(f.ctx, "http://"+f.host)
	if e != nil || v.IsAllowed || v.MatchedSource == nil || *v.MatchedSource != "default_http_block" {
		t.Fatal(v, e)
	}
	if e = f.trust.Delete(f.outsideCtx, f.outside, low.ID); e != nil {
		t.Fatal(e)
	}
	v, e = f.trust.Evaluate(f.ctx, "https://"+f.host)
	if e != nil || !v.IsAllowed || v.IsVerified {
		t.Fatal(v, e)
	}
}
func TestPGDomainTrustConcurrentMetricsAndCardinality(t *testing.T) {
	f := newDomainFixture(t)
	var baseline int
	if e := f.db.QueryRow(`SELECT count(*) FROM owned_domain_trust_metrics`).Scan(&baseline); e != nil {
		t.Fatal(e)
	}
	r, e := domaintrustpg.New(f.db, f.guard, domaintrustpg.Config{MaxMetricDomains: baseline + 1, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := r.Validate(context.Background(), "https://"+f.host+"/?ignored=SYNTHETIC")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	m, e := r.Metric(f.outsideCtx, f.outside, f.host)
	if e != nil || m.CheckCount != 16 {
		t.Fatal(m, e)
	}
	if _, e = r.Validate(f.ctx, "https://second."+f.host); !errors.Is(e, d.ErrCapacity) {
		t.Fatal(e)
	}
	if _, e = r.Validate(f.ctx, "https://"+f.host); e != nil {
		t.Fatal("existing host denied at capacity", e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_domain_trust_metrics WHERE domain=$1::text`, "second."+f.host); n != 0 {
		t.Fatal(n)
	}
}
func TestPGDomainTrustAuditRollbackAndFreshAuthority(t *testing.T) {
	f := newDomainFixture(t)
	rule := f.rule(t, d.Blocked)
	name := "synthetic_domain_audit_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_domain_trust_audit ADD CONSTRAINT `+name+` CHECK(rule_id<>'`+rule.ID+`'::uuid OR action<>'update') NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_domain_trust_audit DROP CONSTRAINT IF EXISTS ` + name) })
	level := d.Verified
	_, e := f.trust.Update(f.outsideCtx, f.outside, rule.ID, d.Patch{TrustLevel: &level})
	domainConstraint(t, e, name)
	current, e := f.trust.Get(f.outsideCtx, f.outside, rule.ID)
	if e != nil || current.TrustLevel != d.Blocked {
		t.Fatal(current, e)
	}
	query(t, f.db, `ALTER TABLE owned_domain_trust_audit DROP CONSTRAINT `+name)
	// A revocation holds the canonical account row while the request's preflight
	// sees the earlier committed snapshot; final locking authorization must deny.
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1::uuid`, f.outside); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(f.outsideCtx, 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, e := f.trust.Update(ctx, f.outside, rule.ID, d.Patch{TrustLevel: &level}); result <- e }()
	domainWait(t, f.db, pid)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-result; !errors.Is(e, admin.ErrForbidden) {
		t.Fatal("stale global flag", e)
	}
}
func TestPGDomainTrustRuleUpdateWinsQueuedValidation(t *testing.T) {
	f := newDomainFixture(t)
	rule := f.rule(t, d.Verified)
	tx, e := f.db.BeginTx(f.ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var pid int
	if e = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	queryTx := func(q string, args ...any) {
		if _, e := tx.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	queryTx(`SELECT generation FROM owned_domain_trust_state WHERE singleton FOR UPDATE`)
	queryTx(`UPDATE owned_domain_trust_rules SET trust_level=0 WHERE id=$1::uuid`, rule.ID)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	type outcome struct {
		v d.Result
		e error
	}
	result := make(chan outcome, 1)
	go func() { v, e := f.trust.Validate(ctx, "https://"+f.host); result <- outcome{v, e} }()
	domainWait(t, f.db, pid)
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	out := <-result
	if out.e != nil || out.v.IsAllowed {
		t.Fatal(out)
	}
	m, e := f.trust.Metric(f.outsideCtx, f.outside, f.host)
	if e != nil || m.CheckCount != 1 || m.BlockedCount != 1 {
		t.Fatal(m, e)
	}
}
func TestPGDomainTrustMetricFailureRollsBackDecision(t *testing.T) {
	f := newDomainFixture(t)
	name := "synthetic_domain_metric_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_domain_trust_metrics ADD CONSTRAINT `+name+` CHECK(domain<>'`+f.host+`') NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_domain_trust_metrics DROP CONSTRAINT IF EXISTS ` + name) })
	v, e := f.trust.Validate(f.ctx, "https://"+f.host)
	domainConstraint(t, e, name)
	if v.IsAllowed || commerceCount(t, f.db, `SELECT count(*) FROM owned_domain_trust_metrics WHERE domain=$1::text`, f.host) != 0 {
		t.Fatal("partial metric commit", v)
	}
}
func TestPGDomainTrustRestartPersistence(t *testing.T) {
	const host = "synthetic-domain-restart.test"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var checks int
		var level int
		if e := db.QueryRow(`SELECT check_count FROM owned_domain_trust_metrics WHERE domain=$1::text`, host).Scan(&checks); e != nil || checks != 1 {
			t.Fatal(checks, e)
		}
		if e := db.QueryRow(`SELECT trust_level FROM owned_domain_trust_rules WHERE domain_pattern=$1::text AND deleted_at IS NULL`, host).Scan(&level); e != nil || level != d.Verified {
			t.Fatal(level, e)
		}
		return
	}
	f := newModerationFixture(t)
	query(t, f.db, domaintrustpg.Schema)
	r, e := domaintrustpg.New(f.db, f.guard, domaintrustpg.Config{Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Create(f.outsideCtx, f.outside, d.Create{DomainPattern: host, TrustLevel: d.Verified}); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Validate(f.ctx, "https://"+host+"/private?value=SYNTHETIC"); e != nil {
		t.Fatal(e)
	}
}
