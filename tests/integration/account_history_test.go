// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"solsynth.local/go-migration/internal/accounthistorypg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/presence"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func historyRepository(f activityFixture) *accounthistorypg.Repository {
	return accounthistorypg.New(f.db, f.guard, f.repo)
}
func historyCredit(t *testing.T, db *sql.DB, account string, delta float64, created time.Time, expired *time.Time) string {
	t.Helper()
	id := authorityUUID(t)
	query(t, db, `INSERT INTO owned_account_credit_records(id,account_id,reason_type,reason,delta,created_at,expired_at,source_kind,source_ref,source_record)VALUES($1::uuid,$2::uuid,'synthetic.policy','Synthetic authored formula fixture',$3::double precision,$4::timestamptz,$5::timestamptz,'owned-policy','synthetic:history-fixture',$1::uuid::text)`, id, account, delta, created, expired)
	return id
}
func historyConnection(t *testing.T, db *sql.DB, account, provider string) string {
	t.Helper()
	id := authorityUUID(t)
	query(t, db, `INSERT INTO owned_account_connections(id,account_id,provider,provided_identifier,email,username,display_name,last_used_at,created_at,updated_at,verified_at,source_ref)VALUES($1,$2,$3,$4,'synthetic@example.invalid','synthetic','Synthetic account',clock_timestamp(),clock_timestamp(),clock_timestamp(),clock_timestamp(),'synthetic:established-provider-link-fixture')`, id, account, provider, "synthetic-provider-id-"+id)
	return id
}
func TestPGBackendAccountHistoryAuthoredRoutes(t *testing.T) {
	f := newActivityFixture(t)
	status := backendDecode[presence.Status](t, backendCall(t, f.app, "POST", "/passport/accounts/me/statuses", f.token, `{"type":0,"attitude":0,"label":"Historical status","meta":{"private_actor":{"id":"must-not-leak"}}}`, 200))
	activity := activityStart(t, f, "account-history")
	if _, e := f.repo.End(f.ctx, f.actor, "account-history"); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "DELETE", "/passport/accounts/me/statuses", f.token, "", 204)
	backendCall(t, f.app, "PATCH", "/passport/accounts/me", f.token, `{"bio":"history audit fixture"}`, 200)
	for _, prefix := range []string{"/api", "/passport", "/stargate"} {
		w := backendCall(t, f.app, "GET", prefix+"/accounts/"+f.actor+"/timeline", "", "", 200)
		rows := backendDecode[[]history.TimelineItem](t, w)
		if len(rows) != 2 || w.Header().Get("X-Total") != "2" {
			t.Fatal(rows, w.Header())
		}
		seen := map[string]bool{}
		for _, v := range rows {
			seen[v.ID] = true
			if v.Status != nil && string(v.Status.Meta) != "null" {
				t.Fatal("private status metadata", v)
			}
		}
		if !seen[status.ID] || !seen[activity.ID] || strings.Contains(w.Body.String(), "must-not-leak") {
			t.Fatal(w.Body.String())
		}
		logs := backendDecode[[]history.ActionLog](t, backendCall(t, f.app, "GET", prefix+"/accounts/me/actions?action=accounts.profile.update", f.token, "", 200))
		if len(logs) != 1 || logs[0].AccountID != f.actor || logs[0].Meta["source"] != "owned_accounts_audit" || logs[0].IPAddress != nil || logs[0].SessionID != nil {
			t.Fatal(logs)
		}
		backendCall(t, f.app, "GET", prefix+"/accounts/me/actions", "", "", 401)
		backendCall(t, f.app, "GET", prefix+"/accounts/"+f.actor+"/credits", "invalid", "", 401)
	}
	w := backendCall(t, f.app, "HEAD", "/api/accounts/"+f.actor+"/timeline", "", "", 200)
	if w.Body.Len() != 0 || w.Header().Get("X-Total") != "2" {
		t.Fatal(w.Body.String(), w.Header())
	}
	if e := f.repo.Delete(f.ctx, f.actor, activity.ID, ""); e != nil {
		t.Fatal(e)
	}
	page := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", "", "", 200)
	if page.Header().Get("X-Total") != "1" || strings.Contains(page.Body.String(), activity.ID) {
		t.Fatal(page.Body.String())
	}
	backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/connections", f.otherToken, "", 404)
	backendCall(t, f.app, "POST", "/api/accounts/me/actions", f.token, `{}`, 405)
}
func TestPGAccountTimelineStablePaginationPrivacyAndTimeBounds(t *testing.T) {
	f := newActivityFixture(t)
	at := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	status := backendDecode[presence.Status](t, backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"type":0,"attitude":0,"label":"status"}`, 200))
	public := activityStart(t, f, "public")
	req := activityRequest("private")
	req.Visibility = activities.Private
	req.Meta = json.RawMessage(`{"message":{"body":"private-history-payload"}}`)
	private, e := f.repo.Set(f.ctx, f.actor, req, true, "")
	if e != nil {
		t.Fatal(e)
	}
	req = activityRequest("friends")
	req.Visibility = activities.Friends
	friend, e := f.repo.Set(f.ctx, f.actor, req, true, "")
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_presence_statuses SET created_at=$2 WHERE id=$1`, status.ID, at)
	query(t, f.db, `UPDATE owned_presence_activities SET created_at=$2 WHERE account_id=$1`, f.actor, at)
	own, n, e := f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 20})
	if e != nil || n != 4 || len(own) != 4 || own[0].ID != status.ID {
		t.Fatal(own, n, e)
	}
	for i, want := range own {
		got, total, e := f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 1, Offset: i})
		if e != nil || total != 4 || len(got) != 1 || got[0].ID != want.ID {
			t.Fatal(got, total, e)
		}
	}
	empty, n, e := f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 1, Offset: 4})
	if e != nil || n != 4 || len(empty) != 0 {
		t.Fatal(empty, n, e)
	}
	next := at.Add(time.Microsecond)
	rows, n, e := f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 20, From: &at, To: &next})
	if e != nil || n != 4 || len(rows) != 4 {
		t.Fatal(rows, n, e)
	}
	rows, n, e = f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 20, To: &at})
	if e != nil || n != 0 || len(rows) != 0 {
		t.Fatal(rows, n, e)
	}
	w := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", f.otherToken, "", 200)
	if w.Header().Get("X-Total") != "2" || strings.Contains(w.Body.String(), private.ID) || strings.Contains(w.Body.String(), friend.ID) || !strings.Contains(w.Body.String(), public.ID) {
		t.Fatal(w.Body.String(), w.Header())
	}
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/friends", f.token, "", 200)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.actor+"/friends/accept", f.otherToken, "", 200)
	w = backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", f.otherToken, "", 200)
	if w.Header().Get("X-Total") != "3" || !strings.Contains(w.Body.String(), friend.ID) || strings.Contains(w.Body.String(), "private-history-payload") {
		t.Fatal(w.Body.String(), w.Header())
	}
	query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1,'private',clock_timestamp())`, f.actor)
	w = backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", f.otherToken, "", 200)
	if w.Header().Get("X-Total") != "0" || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String(), w.Header())
	}
	rows, n, e = f.repo.Timeline(f.ctx, f.actor, f.actor, history.Filter{Take: 20})
	if e != nil || n != 4 || len(rows) != 4 {
		t.Fatal(rows, n, e)
	}
}
func TestPGAccountHistoryCreditDecayImmutableProvenance(t *testing.T) {
	f := newActivityFixture(t)
	repo := historyRepository(f)
	if value, e := repo.Credits(f.ctx, f.actor, f.actor); e != nil || value != 100 {
		t.Fatal("source-backed empty owned ledger", value, e)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	later := at.Add(24 * time.Hour)
	expired := at.Add(-time.Hour)
	permanent := historyCredit(t, f.db, f.actor, 7, at, nil)
	historyCredit(t, f.db, f.actor, 40, at.Add(-24*time.Hour), &later)
	historyCredit(t, f.db, f.actor, -10, at.Add(-24*time.Hour), &later)
	historyCredit(t, f.db, f.actor, 900, at.Add(-48*time.Hour), &expired)
	historyCredit(t, f.db, f.other, 900, at, nil)
	value, e := repo.Credits(f.ctx, f.actor, f.actor)
	if e != nil || math.Abs(value-122) > 0.02 {
		t.Fatal(value, e)
	}
	rows, total, e := repo.CreditHistory(f.ctx, f.actor, history.Filter{Take: 2})
	if e != nil || total != 4 || len(rows) != 2 || rows[0].ID != permanent {
		t.Fatal(rows, total, e)
	}
	empty, total, e := repo.CreditHistory(f.ctx, f.actor, history.Filter{Take: 2, Offset: 100})
	if e != nil || total != 4 || len(empty) != 0 {
		t.Fatal(empty, total, e)
	}
	for _, statement := range []string{`UPDATE owned_account_credit_records SET delta=999 WHERE id=$1`, `DELETE FROM owned_account_credit_records WHERE id=$1`} {
		if _, e = f.db.Exec(statement, permanent); e == nil {
			t.Fatal("immutable credit changed")
		}
	}
	if _, e = f.db.Exec(`INSERT INTO owned_account_credit_records(id,account_id,reason_type,reason,delta,created_at,source_kind,source_ref,source_record)VALUES($1,$2,'bad','bad',1,clock_timestamp(),'owned-policy','','bad')`, authorityUUID(t), f.actor); e == nil {
		t.Fatal("missing provenance accepted")
	}
	if _, e = f.db.Exec(`INSERT INTO owned_account_credit_records(id,account_id,reason_type,reason,delta,created_at,source_kind,source_ref,source_record)VALUES($1,$2,'bad','bad','NaN',clock_timestamp(),'owned-policy','synthetic:bad','bad')`, authorityUUID(t), f.actor); e == nil {
		t.Fatal("NaN accepted")
	}
	if _, e = f.db.Exec(`INSERT INTO owned_account_credit_records(id,account_id,reason_type,reason,delta,created_at,source_kind,source_ref,source_record)SELECT $1,account_id,reason_type,reason,delta,created_at,source_kind,source_ref,source_record FROM owned_account_credit_records WHERE id=$2`, authorityUUID(t), permanent); e == nil {
		t.Fatal("provenance replay duplicated credit")
	}
	backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/credits", "", "", 200)
	query(t, f.db, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1,'private',clock_timestamp())`, f.actor)
	backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/credits", "", "", 404)
	if value, e = repo.Credits(f.ctx, f.actor, f.actor); e != nil || math.Abs(value-122) > 0.02 {
		t.Fatal(value, e)
	}
}
func TestPGAccountHistoryConnectionMetadataIsolationAndPermission(t *testing.T) {
	f := newActivityFixture(t)
	repo := historyRepository(f)
	first := historyConnection(t, f.db, f.actor, "github")
	second := historyConnection(t, f.db, f.actor, "steam")
	other := historyConnection(t, f.db, f.other, "github")
	revoked := historyConnection(t, f.db, f.actor, "revoked")
	future := historyConnection(t, f.db, f.actor, "unverified")
	query(t, f.db, `UPDATE owned_account_connections SET revoked_at=clock_timestamp() WHERE id=$1`, revoked)
	query(t, f.db, `UPDATE owned_account_connections SET verified_at=clock_timestamp()+interval '1 day' WHERE id=$1`, future)
	// A caller-selected credential column can never enter the fixed projection.
	query(t, f.db, `ALTER TABLE owned_account_connections ADD COLUMN IF NOT EXISTS provider_token text`)
	query(t, f.db, `UPDATE owned_account_connections SET provider_token='synthetic-never-return-token' WHERE id=$1`, first)
	for _, prefix := range []string{"/api", "/passport", "/stargate"} {
		w := backendCall(t, f.app, "GET", prefix+"/accounts/me/connections", f.token, "", 200)
		v := backendDecode[[]history.Connection](t, w)
		if len(v) != 2 || v[0].ID != first || v[1].ID != second || strings.Contains(w.Body.String(), other) || strings.Contains(w.Body.String(), "synthetic-never-return-token") || strings.Contains(w.Body.String(), "source_ref") {
			t.Fatal(w.Body.String())
		}
	}
	v, e := repo.Connections(f.ctx, f.actor, "github")
	if e != nil || len(v) != 1 || v[0].ID != first || v[0].Meta["email"] != "synthetic@example.invalid" {
		t.Fatal(v, e)
	}
	if _, e = repo.Connections(context.Background(), f.actor, ""); e == nil {
		t.Fatal("forged actor accepted")
	}
	if _, e = repo.Connections(f.ctx, f.other, ""); e == nil {
		t.Fatal("original proof rebound to another account")
	}
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "posts.read"}}, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.app, "GET", "/api/accounts/me/connections", f.token, "", 403)
}
func TestPGAccountHistoryActionsCanonicalAuditSnapshot(t *testing.T) {
	f := newActivityFixture(t)
	repo := historyRepository(f)
	for i := 0; i < 3; i++ {
		backendCall(t, f.app, "PATCH", "/api/accounts/me", f.token, fmt.Sprintf(`{"bio":"version-%d"}`, i), 200)
	}
	backendCall(t, f.app, "PATCH", "/api/accounts/me", f.otherToken, `{"bio":"other account"}`, 200)
	at := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)
	query(t, f.db, `UPDATE owned_accounts_audit SET occurred_at=$2 WHERE actor_id=$1`, f.actor, at)
	v, n, e := repo.Actions(f.ctx, f.actor, history.Filter{Take: 20, Action: "accounts.profile.update"})
	if e != nil || n != 3 || len(v) != 3 {
		t.Fatal(v, n, e)
	}
	for i, want := range v {
		got, total, e := repo.Actions(f.ctx, f.actor, history.Filter{Take: 1, Offset: i, Action: "accounts.profile.update"})
		if e != nil || total != 3 || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatal(got, total, e)
		}
	}
	v, n, e = repo.Actions(f.ctx, f.actor, history.Filter{Take: 20, Action: "%"})
	if e != nil || n != 0 || len(v) != 0 {
		t.Fatal("SQL wildcard action was not literal", v, n, e)
	}
	v, n, e = repo.Actions(f.ctx, f.actor, history.Filter{Take: 20, To: &at})
	if e != nil || n != 0 || len(v) != 0 {
		t.Fatal("exclusive bound", v, n, e)
	}
	if _, _, e = repo.Actions(context.Background(), f.actor, history.Filter{Take: 20}); e == nil {
		t.Fatal("forged actor read audit")
	}
	// Inject failure only for this fixture's next audit. NOT VALID preserves the
	// original audit rows, so rollback cannot be confused with deleting history.
	prior, count, e := repo.Actions(f.ctx, f.actor, history.Filter{Take: 20})
	if e != nil {
		t.Fatal(e)
	}
	var before, after string
	if e = f.db.QueryRow(`SELECT row_to_json(p)::text FROM owned_accounts_profiles p WHERE account_id=$1`, f.actor).Scan(&before); e != nil {
		t.Fatal(e)
	}
	constraint := "synthetic_history_audit_" + strings.ReplaceAll(f.actor, "-", "")
	query(t, f.db, `ALTER TABLE owned_accounts_audit ADD CONSTRAINT `+constraint+` CHECK(actor_id<>'`+f.actor+`'::uuid OR action<>'accounts.profile.update') NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_accounts_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	backendCall(t, f.app, "PATCH", "/api/accounts/me", f.token, `{"bio":"must roll back with audit"}`, 503)
	if e = f.db.QueryRow(`SELECT row_to_json(p)::text FROM owned_accounts_profiles p WHERE account_id=$1`, f.actor).Scan(&after); e != nil || after != before {
		t.Fatal("failed audit changed profile", e, before, after)
	}
	got, gotCount, e := repo.Actions(f.ctx, f.actor, history.Filter{Take: 20})
	if e != nil || gotCount != count || !reflect.DeepEqual(got, prior) {
		t.Fatal("failed mutation altered canonical history", got, gotCount, e)
	}
	query(t, f.db, `ALTER TABLE owned_accounts_audit DROP CONSTRAINT `+constraint)
}
func TestPGAccountHistoryOriginalProofAfterAccountWait(t *testing.T) {
	for _, op := range []string{"actions", "credits", "credit-history", "connections", "timeline"} {
		t.Run(op, func(t *testing.T) {
			f := newActivityFixture(t)
			repo := historyRepository(f)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			blocker, pid := atomicBlocker(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, "owned_authority_accounts", f.actor)
			done := make(chan error, 1)
			go func() {
				var e error
				switch op {
				case "actions":
					_, _, e = repo.Actions(ctx, f.actor, history.Filter{Take: 20})
				case "credits":
					_, e = repo.Credits(ctx, f.actor, f.actor)
				case "credit-history":
					_, _, e = repo.CreditHistory(ctx, f.actor, history.Filter{Take: 20})
				case "connections":
					_, e = repo.Connections(ctx, f.actor, "")
				case "timeline":
					_, _, e = repo.Timeline(ctx, f.actor, f.actor, history.Filter{Take: 20})
				}
				done <- e
			}()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_authority_accounts")
			if _, e := blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, f.actor); e != nil {
				t.Fatal(e)
			}
			if e := blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e := <-done; e == nil {
				t.Fatal("revoked original proof survived account wait", op)
			}
		})
	}
}
func TestPGAccountHistoryPublicWaitObservesPrivacyAndRename(t *testing.T) {
	for _, change := range []string{"private", "rename", "blocked", "disabled"} {
		t.Run(change, func(t *testing.T) {
			f := newActivityFixture(t)
			activityStart(t, f, "public-wait")
			repo := historyRepository(f)
			var name string
			if e := f.db.QueryRow(`SELECT name FROM owned_authority_accounts WHERE id=$1`, f.actor).Scan(&name); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_authority_accounts", f.actor)
			done := make(chan error, 1)
			go func() { _, e := repo.Credits(ctx, "", name); done <- e }()
			waitAtomicBlocked(t, locks, pid, "owned_authority_accounts")
			var e error
			switch change {
			case "private":
				_, e = blocker.ExecContext(ctx, `INSERT INTO owned_presence_preferences(account_id,status_visibility,updated_at)VALUES($1,'private',clock_timestamp())`, f.actor)
			case "rename":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET name=name||'_new' WHERE id=$1`, f.actor)
			case "disabled":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "blocked":
				_, e = blocker.ExecContext(ctx, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1,'disable_account',clock_timestamp())`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, history.ErrNotFound) {
				t.Fatal("public stale read survived", change, e)
			}
		})
	}
}
func TestPGBackendAccountHistorySavedRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	db := database(t)
	type savedState struct {
		Actor, Other, Token, OtherToken                        string
		Authority                                              restartAuthoritySnapshot
		Timeline, Actions, Credits, CreditHistory, Connections string
	}
	var saved savedState
	if phase == "before" {
		f := newActivityFixture(t)
		activityStart(t, f, "history-restart")
		if _, e := f.repo.End(f.ctx, f.actor, "history-restart"); e != nil {
			t.Fatal(e)
		}
		backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"type":0,"attitude":0,"label":"durable history"}`, 200)
		backendCall(t, f.app, "PATCH", "/api/accounts/me", f.token, `{"bio":"durable audit"}`, 200)
		historyCredit(t, f.db, f.actor, 13, time.Now().UTC(), nil)
		historyConnection(t, f.db, f.actor, "github")
		saved = savedState{Actor: f.actor, Other: f.other, Token: f.token, OtherToken: f.otherToken, Authority: saveAuthoritySnapshot(t, f.config)}
		for path, p := range map[string]*string{"timeline": &saved.Timeline, "actions": &saved.Actions, "credits": &saved.Credits, "credits/history": &saved.CreditHistory, "connections": &saved.Connections} {
			*p = backendCall(t, f.app, "GET", "/api/accounts/me/"+path, f.token, "", 200).Body.String()
		}
		saveRestartFixture(t, "account-history-state.json", saved)
		return
	}
	loadRestartFixture(t, "account-history-state.json", &saved)
	cfg := restoreAuthoritySnapshot(t, saved.Authority)
	if cfg.SigningKey != nil {
		t.Fatal("restart imported private signing key")
	}
	app := backendApp(t, db, cfg)
	for path, want := range map[string]string{"timeline": saved.Timeline, "actions": saved.Actions, "credits": saved.Credits, "credits/history": saved.CreditHistory, "connections": saved.Connections} {
		got := backendCall(t, app, "GET", "/passport/accounts/me/"+path, saved.Token, "", 200).Body.String()
		if got != want {
			t.Fatal("durable read changed across real database/process restart", path, got, want)
		}
	}
	// The original other credential still cannot select this account's private data.
	w := backendCall(t, app, "GET", "/api/accounts/me/connections", saved.OtherToken, "", 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
}
func TestPGBackendAccountHistoryForgedChannels(t *testing.T) {
	f := newActivityFixture(t)
	for _, path := range []string{"/api/accounts/me/actions", "/passport/accounts/me/connections", "/stargate/accounts/" + f.actor + "/timeline"} {
		r := fixtureNoBodyRequest("GET", path, f.token)
		r.Header.Set("X-Account-ID", f.other)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/accounts/me/credits?account_id=" + f.other, "/api/accounts/me/actions?take=1&take=2", "/api/accounts/me/connections?token=synthetic"} {
		backendCall(t, f.app, "GET", path, f.token, "", 400)
	}
}

func TestPGAccountHistoryBlockDisabledAndCurrentSession(t *testing.T) {
	for _, change := range []string{"logout", "expired"} {
		t.Run("historical_source_"+change, func(t *testing.T) {
			f := newActivityFixture(t)
			// Both original proofs precede target history; no post-logout reissuance.
			ownerReader := pgIssue(t, f.issuer, f.actor).Token
			a := activityStart(t, f, "source-history")
			backendCall(t, f.app, "POST", "/api/accounts/me/statuses", f.token, `{"type":0,"attitude":0,"label":"independent status","meta":{"private":"historical-private-meta"}}`, 200)
			if _, e := f.repo.End(f.ctx, f.actor, "source-history"); e != nil {
				t.Fatal(e)
			}
			if change == "logout" {
				if e := f.issuer.Revoke(f.ctx, f.token, "current"); e != nil {
					t.Fatal(e)
				}
			} else {
				query(t, f.db, `UPDATE owned_authority_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT session_id FROM owned_presence_activities WHERE id=$1)`, a.ID)
			}
			for _, viewer := range []string{"", f.otherToken} {
				page := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", viewer, "", 200)
				if page.Header().Get("X-Total") != "2" || !strings.Contains(page.Body.String(), a.ID) || strings.Contains(page.Body.String(), "historical-private-meta") || strings.Contains(page.Body.String(), "source-history") {
					t.Fatal("history erased or private fields leaked", page.Body.String(), page.Header())
				}
				empty := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline?take=1&offset=2", viewer, "", 200)
				if empty.Header().Get("X-Total") != "2" || strings.TrimSpace(empty.Body.String()) != "[]" {
					t.Fatal("historical pagination changed", empty.Body.String(), empty.Header())
				}
			}
			// Explicit current privacy withdrawal hides history even though sign-out alone did not.
			backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", ownerReader, `{"online_visibility":"private","status_visibility":"private","last_seen_visibility":"private"}`, 200)
			private := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", "", "", 200)
			if private.Header().Get("X-Total") != "0" || strings.TrimSpace(private.Body.String()) != "[]" {
				t.Fatal("privacy withdrawal leaked history", private.Body.String())
			}
			own := backendCall(t, f.app, "GET", "/api/accounts/me/timeline", ownerReader, "", 200)
			if own.Header().Get("X-Total") != "2" || !strings.Contains(own.Body.String(), a.ID) {
				t.Fatal("owner lost historical event", own.Body.String())
			}
			backendCall(t, f.app, "PUT", "/api/accounts/me/presence/preferences", ownerReader, `{"online_visibility":"public","status_visibility":"public","last_seen_visibility":"friends"}`, 200)
			backendCall(t, f.app, "DELETE", "/api/activities?id="+a.ID, ownerReader, "", 204)
			redacted := backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/timeline", "", "", 200)
			if redacted.Header().Get("X-Total") != "1" || strings.Contains(redacted.Body.String(), a.ID) {
				t.Fatal("explicit redaction left activity history", redacted.Body.String())
			}
		})
	}
	f := newActivityFixture(t)
	activityStart(t, f, "blocked-history")
	historyCredit(t, f.db, f.actor, 7, time.Now().UTC(), nil)
	backendCall(t, f.app, "POST", "/stargate/relationships/"+f.other+"/block", f.token, "", 200)
	for _, suffix := range []string{"timeline", "credits"} {
		backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/"+suffix, f.otherToken, "", 404)
		backendCall(t, f.app, "GET", "/api/accounts/"+f.other+"/"+suffix, f.token, "", 404)
	}
	// Blocking does not manufacture anonymous identity or hide the owner's reads.
	backendCall(t, f.app, "GET", "/api/accounts/me/timeline", f.token, "", 200)
	backendCall(t, f.app, "GET", "/api/accounts/me/credits", f.token, "", 200)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	for _, suffix := range []string{"timeline", "credits"} {
		backendCall(t, f.app, "GET", "/api/accounts/"+f.actor+"/"+suffix, "", "", 404)
	}
	for _, suffix := range []string{"actions", "credits/history", "connections"} {
		w := httptest.NewRecorder()
		r := fixtureNoBodyRequest("GET", "/api/accounts/me/"+suffix, f.token)
		f.app.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Fatal("disabled account read", suffix, w.Body.String())
		}
	}
}
func TestPGAccountTimelineArtworkWaitRechecksProofAndRetention(t *testing.T) {
	for _, change := range []string{"proof", "retention", "source"} {
		t.Run(change, func(t *testing.T) {
			f := newArtworkFixture(t)
			if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
				t.Fatal(e)
			}
			artworkApp(t, &f)
			asset := artworkPost(t, f.app, f.token, f.data)
			backendCall(t, f.app, "POST", "/api/activities", f.token, fmt.Sprintf(`{"type":"coding","manual_id":"history-artwork","large_image":%q,"visibility":1}`, asset.Hash), 200)
			base := f.config.Now()
			var shift atomic.Int64
			cfg := f.config
			cfg.Now = func() time.Time { return base.Add(time.Duration(shift.Load())) }
			verifier, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
			if e != nil {
				t.Fatal(e)
			}
			proof, e := verifier.Authenticate(context.Background(), f.token)
			if e != nil {
				t.Fatal(e)
			}
			guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), verifier)
			ctx, cancel := context.WithTimeout(guard.Bind(context.Background(), proof), 15*time.Second)
			defer cancel()
			var deadline time.Time
			if change == "retention" {
				if e = f.db.QueryRow(`UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp()-(retention_seconds*interval '1 second')+interval '2 seconds' WHERE hash=$1 RETURNING last_referenced_at+(retention_seconds*interval '1 second')`, asset.Hash).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			}
			if change == "source" {
				if e = f.db.QueryRow(`UPDATE owned_authority_sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=(SELECT session_id FROM owned_presence_activities WHERE account_id=$1 AND manual_id='history-artwork') RETURNING expires_at`, f.actor).Scan(&deadline); e != nil {
					t.Fatal(e)
				}
			}
			var file string
			if e = f.db.QueryRow(`SELECT file_id::text FROM owned_presence_artworks WHERE hash=$1`, asset.Hash).Scan(&file); e != nil {
				t.Fatal(e)
			}
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			blocker, pid := atomicBlocker(t, locks, "owned_media_files", file)
			type result struct {
				rows []history.TimelineItem
				err  error
			}
			done := make(chan result, 1)
			go func() {
				actor := f.actor
				if change == "source" {
					actor = ""
				}
				rows, _, e := activitypg.New(f.db, guard).Timeline(ctx, actor, f.actor, history.Filter{Take: 20})
				done <- result{rows, e}
			}()
			waitAtomicBlocked(t, locks, pid, "owned_media_files")
			if change == "proof" {
				shift.Store(int64(24 * time.Hour))
			} else {
				organizationWaitExpired(t, ctx, f.db, deadline)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			got := <-done
			if change == "source" {
				if got.err != nil || len(got.rows) != 1 || got.rows[0].Activity == nil || got.rows[0].Activity.LargeImage == nil {
					t.Fatal("source expiry incorrectly erased public historical event", got)
				}
			} else if change == "proof" {
				if got.err == nil || len(got.rows) != 0 {
					t.Fatal("expired original proof leaked timeline", got)
				}
			} else if got.err != nil || len(got.rows) != 1 || got.rows[0].Activity == nil || got.rows[0].Activity.LargeImage != nil {
				t.Fatal("expired artwork remained in timeline", got)
			}
		})
	}
}
