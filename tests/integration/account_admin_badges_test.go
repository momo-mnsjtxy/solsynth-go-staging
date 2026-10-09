// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http/httptest"
	"solsynth.local/go-migration/internal/adminpg"
	"solsynth.local/go-migration/internal/authoritypg"
	a "solsynth.local/go-migration/ports/admin/accounts"
	c "solsynth.local/go-migration/ports/admin/content"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type accountAdminFixture struct {
	backendFixture
	repo  *adminpg.Repository
	guard *authoritypg.TransactionGuard
	proof authority.Principal
	ctx   context.Context
	now   *atomic.Int64
}

func newAccountAdminFixture(t *testing.T) accountAdminFixture {
	t.Helper()
	f := newBackendFixture(t)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1`, f.actor)
	now := &atomic.Int64{}
	now.Store(f.config.Now().Unix())
	f.config.Now = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
	if e != nil {
		t.Fatal(e)
	}
	proof, e := verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
	repo, e := adminpg.New(f.db, guard, f.config.Now)
	if e != nil {
		t.Fatal(e)
	}
	f.app = backendApp(t, f.db, f.config)
	return accountAdminFixture{backendFixture: f, repo: repo, guard: guard, proof: proof, ctx: guard.Bind(context.Background(), proof), now: now}
}
func administrativeBadge(kind string) progression.AdminBadge {
	return progression.AdminBadge{Type: kind, Label: postString("Synthetic display badge"), Meta: map[string]any{"color": "blue"}}
}
func grantAdministrativeBadge(t *testing.T, f accountAdminFixture, kind string) progression.Badge {
	t.Helper()
	b, e := f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, administrativeBadge(kind))
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// Compare security state without fetching any factor secret or credential value.
func accountAdminIdentityState(t *testing.T, db *sql.DB, actor, target string) string {
	t.Helper()
	var out string
	e := db.QueryRow(`SELECT jsonb_build_object(
 'accounts',(SELECT jsonb_agg(jsonb_build_object('id',id,'superuser',is_superuser,'disabled',disabled,'version',version,'factor_policy',factor_policy_version) ORDER BY id) FROM owned_authority_accounts WHERE id IN($1,$2)),
 'sessions',(SELECT jsonb_agg(jsonb_build_object('id',id,'account',account_id,'expires',expires_at,'revoked',revoked_at,'epoch',epoch,'version',account_version,'scopes',scopes) ORDER BY id) FROM owned_authority_sessions WHERE account_id IN($1,$2)),
 'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM owned_authority_grants g WHERE account_id IN($1,$2)),
 'factors',(SELECT jsonb_agg(jsonb_build_object('id',id,'account',account_id,'type',type,'enabled',enabled_at,'revision',revision,'counter',last_counter) ORDER BY id) FROM owned_authority_factors WHERE account_id IN($1,$2)))::text`, actor, target).Scan(&out)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func accountAdminBadgeState(t *testing.T, db *sql.DB, target string) string {
	t.Helper()
	var out string
	if e := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(b) ORDER BY b.id),'[]'::jsonb)::text FROM owned_progression_badges b WHERE account_id=$1`, target).Scan(&out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPGBackendAccountAdminSensitiveReadRoutes(t *testing.T) {
	f := newAccountAdminFixture(t)
	now := f.config.Now()
	email := "synthetic-admin-" + f.other + "@example.invalid"
	query(t, f.db, `INSERT INTO owned_authority_credentials(account_id,email,verified_at)VALUES($1,$2,$3)`, f.other, email, now)
	query(t, f.db, `INSERT INTO owned_authority_contacts(id,account_id,type,credential_account_id,created_at,updated_at)VALUES($1,$2,0,$2,$3,$3)`, authorityUUID(t), f.other, now)
	for _, kind := range []int{0, 3, 5} {
		query(t, f.db, `INSERT INTO owned_authority_factors(id,account_id,type,created_at,enabled_at,secret)VALUES($1,$2,$3,$4,$4,$5)`, authorityUUID(t), f.other, kind, now, []byte("synthetic-secret-must-never-leave-sql"))
	}
	query(t, f.db, `INSERT INTO owned_authority_factors(id,account_id,type,created_at,enabled_at)VALUES($1,$2,4,$3,$3)`, authorityUUID(t), f.other, now)
	for _, prefix := range []string{"/api", "/passport"} {
		base := prefix + "/admin/accounts/" + f.other
		contacts := backendDecode[[]signin.Contact](t, backendCall(t, f.app, "GET", base+"/contacts", f.token, "", 200))
		if len(contacts) != 1 || contacts[0].Content != email || !contacts[0].IsPrimary {
			t.Fatal(contacts)
		}
		w := backendCall(t, f.app, "GET", base+"/factors", f.token, "", 200)
		factors := backendDecode[[]signin.FactorSummary](t, w)
		if len(factors) != 4 || factors[0].Type != "password" || factors[1].Type != "timed_code" || factors[2].Type != "pin_code" || factors[2].HasSecret || factors[2].Trustworthy != 0 || factors[3].Type != "unspecified" {
			t.Fatal(factors)
		}
		for _, f := range factors {
			if f.Config != nil || f.ExpiredAt != nil || f.UpdatedAt != nil {
				t.Fatal("invented metadata", f)
			}
		}
		for _, bad := range []string{"synthetic-secret", "c3ludGhldGlj", `"secret":`, `"last_counter":`, `"pending_until":`, `"revision":`} {
			if strings.Contains(w.Body.String(), bad) {
				t.Fatal("sensitive material leaked", bad)
			}
		}
		backendCall(t, f.app, "GET", base+"/factors", f.otherToken, "", 403)
		backendCall(t, f.app, "GET", base+"/factors", "", "", 401)
		backendCall(t, f.app, "GET", base+"/contacts?account_id="+f.actor, f.token, "", 400)
	}
	backendCall(t, f.app, "GET", "/sphere/admin/accounts/"+f.other+"/factors", f.token, "", 404)
	base := "/api/admin/accounts/" + f.other
	r := fixtureNoBodyRequest("GET", base+"/contacts", f.token)
	r.Header.Set("X-Account-ID", f.actor)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code < 400 {
		t.Fatal("forged header accepted")
	}
	var name string
	if e := f.db.QueryRow(`SELECT name FROM owned_authority_accounts WHERE id=$1`, f.other).Scan(&name); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/passport/admin/accounts/"+strings.ToUpper(name)+"/contacts", f.token, "", 200)
}

func TestPGAccountAdminBadgeLifecycleCanonicalProjection(t *testing.T) {
	f := newAccountAdminFixture(t)
	base := "/passport/admin/accounts/" + f.other + "/badges"
	identityBefore := accountAdminIdentityState(t, f.db, f.actor, f.other)
	request := administrativeBadge("staff")
	request.Meta["is_superuser"] = true
	request.Meta["permissions"] = []string{"*"}
	b := backendDecode[progression.Badge](t, backendJSONCall(t, f.app, "POST", base, f.token, request, 200))
	second := grantAdministrativeBadge(t, f, "special.contributor")
	backendCall(t, f.app, "POST", base+"/"+b.ID+"/activate", f.token, "", 200)
	backendCall(t, f.app, "POST", base+"/"+second.ID+"/activate", f.token, "", 200)
	list := backendDecode[[]progression.Badge](t, backendCall(t, f.app, "GET", "/api/accounts/"+f.other+"/badges", "", "", 200))
	active := 0
	for _, b := range list {
		if b.ActivatedAt != nil {
			active++
			if b.ID != second.ID {
				t.Fatal("wrong active badge")
			}
		}
	}
	if len(list) != 2 || active != 1 {
		t.Fatal(list)
	}
	level := backendDecode[progression.Summary](t, backendCall(t, f.app, "GET", "/api/accounts/me/progression/level", f.otherToken, "", 200))
	if level.ActiveBadge == nil || level.ActiveBadge.ID != second.ID {
		t.Fatal("canonical active projection", level)
	}
	backendCall(t, f.app, "DELETE", base+"/"+second.ID, f.token, "", 204)
	backendCall(t, f.app, "POST", base+"/"+second.ID+"/activate", f.token, "", 404)
	backendCall(t, f.app, "DELETE", "/api/admin/accounts/"+f.actor+"/badges/"+b.ID, f.token, "", 404)
	level = backendDecode[progression.Summary](t, backendCall(t, f.app, "GET", "/api/accounts/me/progression/level", f.otherToken, "", 200))
	if level.ActiveBadge != nil {
		t.Fatal("revoked active projection remains")
	}
	var origin, issuer string
	if e := f.db.QueryRow(`SELECT origin,granted_by::text FROM owned_progression_badges WHERE id=$1`, b.ID).Scan(&origin, &issuer); e != nil || origin != "admin" || issuer != f.actor {
		t.Fatal(origin, issuer, e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_progression_grants WHERE account_id=$1`, f.other); n != 0 {
		t.Fatal("manual badge fabricated earned reward")
	}
	if got := accountAdminIdentityState(t, f.db, f.actor, f.other); got != identityBefore {
		t.Fatal("display badges changed identity authority")
	}
	backendCall(t, f.app, "GET", "/api/admin/accounts/"+f.actor+"/factors", f.otherToken, "", 403)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	if _, e := f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, administrativeBadge("disabled")); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	if e := f.repo.ActivateAccountBadge(f.ctx, f.actor, f.other, b.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	if e := f.repo.RevokeAccountBadge(f.ctx, f.actor, f.other, b.ID); e != nil {
		t.Fatal("disabled target cannot be cleaned up", e)
	}
}

func TestPGAccountAdminExplicitAuthorityAndTargetIsolation(t *testing.T) {
	f := newAccountAdminFixture(t)
	if _, e := f.repo.AccountFactors(context.Background(), f.actor, f.other); e == nil {
		t.Fatal("missing original proof accepted")
	}
	forged := f.proof
	forged.Account.ID = f.other
	if _, e := f.repo.AccountFactors(f.guard.Bind(context.Background(), forged), f.other, f.actor); e == nil {
		t.Fatal("forged proof accepted")
	}
	store := authoritypg.New(f.db)
	if e := store.ReplacePolicy(f.ctx, f.actor, nil, nil); e != nil {
		t.Fatal(e)
	}
	for _, ref := range []string{f.other, authorityUUID(t)} {
		if _, e := f.repo.AccountFactors(f.ctx, f.actor, ref); !errors.Is(e, authority.ErrForbidden) {
			t.Fatal("superuser alone or target oracle", e)
		}
	}
	if e := store.ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: a.ViewPermission}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.AccountFactors(f.ctx, f.actor, f.other); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, administrativeBadge("view-only")); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("read grant wrote badge", e)
	}
	if e := store.ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}, {Pattern: a.ViewPermission, Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.AccountContacts(f.ctx, f.actor, f.other); !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("explicit denial ignored", e)
	}
	if e := store.ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	limited, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "synthetic read scope", Scopes: []string{a.ViewPermission}})
	if e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/api/admin/accounts/"+f.other+"/factors", limited.Token, "", 200)
	backendJSONCall(t, f.app, "POST", "/api/admin/accounts/"+f.other+"/badges", limited.Token, administrativeBadge("scope-denied"), 403)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1`, f.actor)
	if _, e := f.repo.AccountContacts(f.ctx, f.actor, f.other); !errors.Is(e, c.ErrForbidden) {
		t.Fatal("nonglobal explicit grant used", e)
	}
}

func TestPGAccountAdminActivityMetricUTCWindows(t *testing.T) {
	f := newAccountAdminFixture(t)
	now := f.config.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	before, e := f.repo.AccountActivityMetrics(f.ctx, f.actor)
	if e != nil {
		t.Fatal(e)
	}
	for _, at := range []time.Time{day, day.Add(-24 * time.Hour), day.AddDate(0, 0, -6), day.AddDate(0, 0, -7), day.AddDate(0, 0, -13), day.AddDate(0, 0, -29), day.AddDate(0, 0, -30), day.AddDate(0, 0, -59), now.Add(time.Hour)} {
		id := authorityUUID(t)
		if e = authoritypg.SeedSynthetic(f.ctx, f.db, id, "synthetic_metrics_"+id, at); e != nil {
			t.Fatal(e)
		}
		query(t, f.db, `INSERT INTO owned_accounts_profiles(account_id,id,updated_at)VALUES($1,$2,$3)`, id, authorityUUID(t), at)
		query(t, f.db, `INSERT INTO owned_presence_last_seen(account_id,last_seen_at)VALUES($1,$2)`, id, at)
	}
	id := authorityUUID(t)
	if e = authoritypg.SeedSynthetic(f.ctx, f.db, id, "synthetic_metrics_noprofile_"+id, now); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_presence_last_seen(account_id,last_seen_at)VALUES($1,$2)`, id, now)
	got := backendDecode[a.ActivityMetrics](t, backendCall(t, f.app, "GET", "/passport/admin/accounts/metrics/activity", f.token, "", 200))
	if !got.CurrentDayStartedAt.Equal(day) || got.TotalProfiledAccounts-before.TotalProfiledAccounts != 9 || got.DailyActiveUsers-before.DailyActiveUsers != 1 || got.WeeklyActiveUsers-before.WeeklyActiveUsers != 3 || got.MonthlyActiveUsers-before.MonthlyActiveUsers != 6 || got.PreviousDailyActiveUsers-before.PreviousDailyActiveUsers != 1 || got.PreviousWeeklyActiveUsers-before.PreviousWeeklyActiveUsers != 2 || got.PreviousMonthlyActiveUsers-before.PreviousMonthlyActiveUsers != 2 || got.NewAccountsToday-before.NewAccountsToday != 1 || got.NewAccountsThisWeek-before.NewAccountsThisWeek != 3 || got.NewAccountsThisMonth-before.NewAccountsThisMonth != 6 {
		t.Fatal(before, got)
	}
}

func TestPGAccountAdminBadgeAuditRollbackAndDuplicateRace(t *testing.T) {
	f := newAccountAdminFixture(t)
	b := grantAdministrativeBadge(t, f, "first")
	if e := f.repo.ActivateAccountBadge(f.ctx, f.actor, f.other, b.ID); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `ALTER TABLE owned_admin_extension_audit ADD CONSTRAINT synthetic_account_badge_rollback CHECK(action NOT IN('badge.grant','badge.activate','badge.revoke')) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_admin_extension_audit DROP CONSTRAINT IF EXISTS synthetic_account_badge_rollback`)
	})
	for _, action := range []string{"grant", "activate", "revoke"} {
		var e error
		switch action {
		case "grant":
			v := administrativeBadge("rollback")
			now := f.config.Now()
			v.ActivatedAt = &now
			_, e = f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, v)
		case "activate":
			e = f.repo.ActivateAccountBadge(f.ctx, f.actor, f.other, b.ID)
		case "revoke":
			e = f.repo.RevokeAccountBadge(f.ctx, f.actor, f.other, b.ID)
		}
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.ConstraintName != "synthetic_account_badge_rollback" {
			t.Fatal(action, e)
		}
	}
	list, e := f.repo.AccountBadges(f.ctx, f.actor, f.other)
	if e != nil || len(list) != 1 || list[0].ID != b.ID || list[0].ActivatedAt == nil {
		t.Fatal("partial audit failure survived", list, e)
	}
	query(t, f.db, `ALTER TABLE owned_admin_extension_audit DROP CONSTRAINT synthetic_account_badge_rollback`)
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, e := f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, administrativeBadge("concurrent"))
			done <- e
		}()
	}
	first, second := <-done, <-done
	if !(first == nil && errors.Is(second, c.ErrConflict) || second == nil && errors.Is(first, c.ErrConflict)) {
		t.Fatal(first, second)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_admin_extension_audit WHERE target_id=$1 AND action='badge.grant'`, f.other); n != 2 {
		t.Fatal("duplicate audit", n)
	}
}

func TestPGAccountAdminOriginalProofAfterAccountWait(t *testing.T) {
	for _, kind := range []string{"revoked", "scope", "grant", "global", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := newAccountAdminFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			tx, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			var pid int
			if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := f.repo.AccountFactors(ctx, f.actor, f.other); done <- e }()
			moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
			switch kind {
			case "revoked":
				_, e = tx.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=$2 WHERE id=$1`, f.proof.Session.ID, f.config.Now())
			case "scope":
				_, e = tx.ExecContext(ctx, `UPDATE owned_authority_sessions SET scopes='["posts.read"]'::jsonb WHERE id=$1`, f.proof.Session.ID)
			case "grant":
				_, e = tx.ExecContext(ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
			case "global":
				_, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1`, f.actor)
			case "disabled":
				_, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale read authority survived", kind)
			}
		})
	}
}

func TestPGAccountAdminBadgeExpiryAndProofAfterDomainWait(t *testing.T) {
	for _, kind := range []string{"badge", "grant", "proof", "target"} {
		t.Run(kind, func(t *testing.T) {
			f := newAccountAdminFixture(t)
			v := administrativeBadge("domain-wait")
			expiry := f.config.Now().Add(time.Minute)
			if kind == "badge" {
				v.ExpiredAt = &expiry
			}
			b, e := f.repo.GrantAccountBadge(f.ctx, f.actor, f.other, v)
			if e != nil {
				t.Fatal(e)
			}
			// Provision all account-affecting policy BEFORE taking the badge lock.
			if kind == "grant" {
				if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*", ExpiresAt: &expiry}}, nil); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "target" {
				if e = authoritypg.New(f.db).ReplacePolicy(f.ctx, f.other, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account", Pattern: "*", StartsAt: &expiry}}); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			tx, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			var pid int
			if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_progression_badges WHERE id=$1 FOR UPDATE`, b.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- f.repo.ActivateAccountBadge(ctx, f.actor, f.other, b.ID) }()
			moderationWait(t, ctx, f.db, pid, "owned_progression_badges")
			advance := 2 * time.Minute
			if kind == "proof" {
				advance = 11 * time.Minute
			}
			f.now.Add(int64(advance / time.Second))
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("elapsed domain wait bypassed", kind)
			}
			var at sql.NullTime
			if e = f.db.QueryRow(`SELECT activated_at FROM owned_progression_badges WHERE id=$1`, b.ID).Scan(&at); e != nil || at.Valid {
				t.Fatal("failed activation survived", at, e)
			}
		})
	}
}

func TestPGAccountAdminSavedRestartPersistence(t *testing.T) {
	type saved struct {
		Actor, Target, Token, SessionRow string
		IdentityState, BadgeState        string
		Authority                        restartAuthoritySnapshot
		Badges                           []progression.Badge
		Audits                           int
	}
	const marker = "account-admin-badges-restart.json"
	if fixtureRestartPhase(t) == "before" {
		f := newAccountAdminFixture(t)
		at, wall := f.config.Now(), time.Now()
		f.config.AccessTTL = time.Hour
		f.config.SessionTTL = time.Hour
		f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		token := pgIssue(t, issuer, f.actor).Token
		verifier, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		p, e := verifier.Authenticate(context.Background(), token)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
		f.ctx = guard.Bind(context.Background(), p)
		f.repo, e = adminpg.New(f.db, guard, f.config.Now)
		if e != nil {
			t.Fatal(e)
		}
		b := grantAdministrativeBadge(t, f, "restart-active")
		if e = f.repo.ActivateAccountBadge(f.ctx, f.actor, f.other, b.ID); e != nil {
			t.Fatal(e)
		}
		revoked := grantAdministrativeBadge(t, f, "restart-revoked")
		if e = f.repo.RevokeAccountBadge(f.ctx, f.actor, f.other, revoked.ID); e != nil {
			t.Fatal(e)
		}
		list, e := f.repo.AccountBadges(f.ctx, f.actor, f.other)
		if e != nil || len(list) != 1 {
			t.Fatal(list, e)
		}
		var sessionRow string
		if e = f.db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, p.Session.ID).Scan(&sessionRow); e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, saved{Actor: f.actor, Target: f.other, Token: token, Authority: saveAuthoritySnapshot(t, f.config), Badges: list, Audits: commerceCount(t, f.db, `SELECT count(*)FROM owned_admin_extension_audit WHERE target_id=$1`, f.other), SessionRow: sessionRow, IdentityState: accountAdminIdentityState(t, f.db, f.actor, f.other), BadgeState: accountAdminBadgeState(t, f.db, f.other)})
		return
	}
	var v saved
	loadRestartFixture(t, marker, &v)
	db := database(t)
	config := restoreAuthoritySnapshot(t, v.Authority)
	verifier, e := authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	p, e := verifier.Authenticate(context.Background(), v.Token)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), verifier)
	repo, e := adminpg.New(db, guard, config.Now)
	if e != nil {
		t.Fatal(e)
	}
	ctx := guard.Bind(context.Background(), p)
	got, e := repo.AccountBadges(ctx, v.Actor, v.Target)
	if e != nil {
		t.Fatal(e)
	}
	app := backendApp(t, db, config)
	composed := backendDecode[[]progression.Badge](t, backendCall(t, app, "GET", "/passport/admin/accounts/"+v.Target+"/badges", v.Token, "", 200))
	composedJSON, _ := json.Marshal(composed)
	actual, _ := json.Marshal(got)
	if string(composedJSON) != string(actual) {
		t.Fatal("composed read differs after restart")
	}
	if accountAdminBadgeState(t, db, v.Target) != v.BadgeState {
		t.Fatal("original revoked state or provenance changed")
	}
	if accountAdminIdentityState(t, db, v.Actor, v.Target) != v.IdentityState {
		t.Fatal("original authority state changed")
	}
	want, _ := json.Marshal(v.Badges)
	if string(actual) != string(want) {
		t.Fatal("original badge state changed", string(actual), string(want))
	}
	if n := commerceCount(t, db, `SELECT count(*)FROM owned_admin_extension_audit WHERE target_id=$1`, v.Target); n != v.Audits {
		t.Fatal("read replay wrote audit")
	}
	var sessionRow string
	if e = db.QueryRow(`SELECT row_to_json(s)::text FROM owned_authority_sessions s WHERE id=$1`, p.Session.ID).Scan(&sessionRow); e != nil || sessionRow != v.SessionRow {
		t.Fatal("original session replaced", e)
	}
}

func TestPGAccountAdminTargetRenameAfterWait(t *testing.T) {
	for _, reassigned := range []bool{false, true} {
		name := "renamed"
		if reassigned {
			name = "reassigned"
		}
		t.Run(name, func(t *testing.T) {
			f := newAccountAdminFixture(t)
			replacement := authorityUUID(t)
			if e := authoritypg.SeedSynthetic(f.ctx, f.db, replacement, "synthetic_replacement_"+replacement, f.config.Now()); e != nil {
				t.Fatal(e)
			}
			var name string
			if e := f.db.QueryRow(`SELECT name FROM owned_authority_accounts WHERE id=$1`, f.other).Scan(&name); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			tx, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			var pid int
			if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id IN($1,$2) ORDER BY id FOR UPDATE`, f.other, replacement); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.repo.GrantAccountBadge(ctx, f.actor, name, administrativeBadge("renamed"))
				done <- e
			}()
			moderationWait(t, ctx, f.db, pid, "owned_authority_accounts")
			if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, f.other, name+"_renamed"); e != nil {
				t.Fatal(e)
			}
			if reassigned {
				if _, e = tx.ExecContext(ctx, `UPDATE owned_authority_accounts SET name=$2 WHERE id=$1`, replacement, name); e != nil {
					t.Fatal(e)
				}
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			want := error(a.ErrAccountNotFound)
			if reassigned {
				want = c.ErrConflict
			}
			if e = <-done; !errors.Is(e, want) {
				t.Fatal("stale/reassigned target name was used", e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_progression_badges WHERE account_id IN($1,$2)`, f.other, replacement); n != 0 {
				t.Fatal("badge escaped rollback")
			}
			if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_admin_extension_audit WHERE target_id IN($1,$2)`, f.other, replacement); n != 0 {
				t.Fatal("audit escaped rollback")
			}
		})
	}
}

func TestPGAccountAdminGrantAndRevokeRecheckDomainWait(t *testing.T) {
	for _, op := range []string{"grant", "revoke"} {
		for _, kind := range []string{"grant", "proof"} {
			t.Run(op+"/"+kind, func(t *testing.T) {
				f := newAccountAdminFixture(t)
				b := grantAdministrativeBadge(t, f, "original")
				expiry := f.config.Now().Add(time.Minute)
				if kind == "grant" {
					if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*", ExpiresAt: &expiry}}, nil); e != nil {
						t.Fatal(e)
					}
				}
				ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
				defer cancel()
				tx, e := f.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback()
				var pid int
				if e = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_progression_badges WHERE id=$1 FOR UPDATE`, b.ID); e != nil {
					t.Fatal(e)
				}
				done := make(chan error, 1)
				go func() {
					if op == "grant" {
						_, e := f.repo.GrantAccountBadge(ctx, f.actor, f.other, administrativeBadge("after-wait"))
						done <- e
					} else {
						done <- f.repo.RevokeAccountBadge(ctx, f.actor, f.other, b.ID)
					}
				}()
				moderationWait(t, ctx, f.db, pid, "owned_progression_badges")
				advance := 2 * time.Minute
				if kind == "proof" {
					advance = 11 * time.Minute
				}
				f.now.Add(int64(advance / time.Second))
				if e = tx.Commit(); e != nil {
					t.Fatal(e)
				}
				if e = <-done; e == nil {
					t.Fatal("stale write authority survived")
				}
				if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_progression_badges WHERE account_id=$1 AND deleted_at IS NULL`, f.other); n != 1 {
					t.Fatal("failed write changed original badge", n)
				}
				if n := commerceCount(t, f.db, `SELECT count(*)FROM owned_admin_extension_audit WHERE target_id=$1`, f.other); n != 1 {
					t.Fatal("failed write changed audit", n)
				}
			})
		}
	}
}
