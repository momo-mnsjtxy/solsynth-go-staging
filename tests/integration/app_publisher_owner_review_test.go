// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/passport/board"
)

func publisherOwnerApp(t *testing.T) (developerFixture, apps.Project, apps.App, apps.Secret) {
	t.Helper()
	f := newDeveloperFixture(t)
	p, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":1}`, 200)
	backendCall(t, f.handler, "POST", root+"/board"+q, f.token, `{"key":"owner-check","name":"Owner check","is_enabled":true,"payload_type":"object","renderer_type":"data"}`, 200)
	secret := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{}`, 201))
	return f, p, a, secret
}

func TestPGAppPublisherOwnerEligibility(t *testing.T) {
	f, p, a, secret := publisherOwnerApp(t)
	ctx := context.Background()
	check := func(allowed bool) {
		t.Helper()
		widgets, e := f.repo.AppWidgets(ctx, a.ID, *secret.Secret)
		if allowed {
			if e != nil || len(widgets) != 1 {
				t.Fatal("healthy credential rejected", e)
			}
		} else if !errors.Is(e, authority.ErrInvalid) || len(widgets) != 0 {
			t.Fatal("ineligible owner exposed credential projection", len(widgets), e)
		}
	}
	check(true)
	before, later := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for _, punishment := range []authority.Punishment{{Kind: "disable_account", StartsAt: &later}, {Kind: "disable_account", ExpiresAt: &before}, {Kind: "block_login"}} {
		if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{punishment}); e != nil {
			t.Fatal(e)
		}
		check(true)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
		t.Fatal(e)
	}
	check(false)
	if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	check(false)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	// Fresh operations resolve the current owner. Stale old-owner status cannot
	// authorize a disabled successor or deny a healthy successor.
	query(t, f.db, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, a.PublisherID, f.other)
	check(true)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	check(true)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	check(false)
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id IN($1,$2)`, f.actor, f.other)
	for _, ancestor := range []struct{ table, id string }{{"owned_publishers", a.PublisherID}, {"owned_dev_projects", p.ID}, {"owned_custom_apps", a.ID}} {
		query(t, f.db, `UPDATE `+ancestor.table+` SET deleted_at=clock_timestamp() WHERE id=$1`, ancestor.id)
		check(false)
		query(t, f.db, `UPDATE `+ancestor.table+` SET deleted_at=NULL WHERE id=$1`, ancestor.id)
	}
	check(true)
	// Organization app ownership remains explicitly unsupported. Imported rows
	// cannot use the NULL direct account as a credential or scoped-management gate.
	query(t, f.db, socialpg.Schema)
	realm := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at)VALUES($1,$1::text,'Synthetic app realm',$2,now(),now())`, realm, f.other)
	query(t, f.db, `UPDATE owned_publishers SET type=1,account_id=NULL,realm_id=$2 WHERE id=$1`, a.PublisherID, realm)
	check(false)
	s := apps.Scope{Actor: f.actor, Developer: f.dev, Project: p.ID, App: a.ID}
	if values, e := f.repo.Secrets(ctx, s, "", nil, "read"); !errors.Is(e, apps.ErrForbidden) || len(values) != 0 {
		t.Fatal("organization private secret projection", e)
	}
	proof, e := f.v.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	if values, e := f.repo.Secrets(f.guard.Bind(ctx, proof), s, "", &apps.SecretRequest{}, "create"); !errors.Is(e, apps.ErrForbidden) || len(values) != 0 {
		t.Fatal("organization credential issuance", e)
	}
	tx, e := f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = f.repo.RequireClient(ctx, tx, a.ID, *secret.Secret, apps.BoardScope); e == nil {
		t.Fatal("organization scoped client accepted")
	}
	if _, _, _, e = f.repo.OAuthRegistration(ctx, f.db, tx, a.Slug, false); e == nil {
		t.Fatal("organization OAuth registration accepted")
	}
	query(t, f.db, `UPDATE owned_realms SET deleted_at=clock_timestamp() WHERE id=$1`, realm)
	check(false)
}

func TestPGAppPublisherOwnerRecheckedAfterWait(t *testing.T) {
	for _, mode := range []string{"scheduled_owner_disable", "owner_changed", "project_changed"} {
		t.Run(mode, func(t *testing.T) {
			f, p, a, secret := publisherOwnerApp(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			blockTable, blockID := "owned_publishers", a.PublisherID
			var starts time.Time
			if mode == "scheduled_owner_disable" {
				blockTable, blockID = "owned_app_secrets", secret.ID
				if e := f.db.QueryRowContext(ctx, `SELECT clock_timestamp()+interval '3 seconds'`).Scan(&starts); e != nil {
					t.Fatal(e)
				}
				if e := authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account", StartsAt: &starts}}); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "project_changed" {
				blockTable, blockID = "owned_custom_apps", a.ID
			}
			blocker, e := f.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback()
			var pid int
			if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.ExecContext(ctx, `SELECT id FROM `+blockTable+` WHERE id=$1 FOR UPDATE`, blockID); e != nil {
				t.Fatal(e)
			}
			type result struct {
				widgets []board.Manifest
				err     error
			}
			done := make(chan result, 1)
			go func() { widgets, err := f.repo.AppWidgets(ctx, a.ID, *secret.Secret); done <- result{widgets, err} }()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, blockTable)
			switch mode {
			case "scheduled_owner_disable":
				for {
					var active bool
					if e = f.db.QueryRowContext(ctx, `SELECT clock_timestamp()>=$1`, starts).Scan(&active); e != nil {
						t.Fatal(e)
					}
					if active {
						break
					}
					select {
					case <-time.After(10 * time.Millisecond):
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			case "owner_changed":
				if _, e = blocker.ExecContext(ctx, `UPDATE owned_publishers SET account_id=$2 WHERE id=$1`, a.PublisherID, f.other); e != nil {
					t.Fatal(e)
				}
			case "project_changed":
				otherProject := authorityUUID(t)
				if _, e = blocker.ExecContext(ctx, `INSERT INTO owned_dev_projects(id,developer_id,slug,name,created_at,updated_at)SELECT $1,developer_id,$1::text,'Changed ancestry',now(),now() FROM owned_dev_projects WHERE id=$2`, otherProject, p.ID); e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `UPDATE owned_custom_apps SET project_id=$2 WHERE id=$1`, a.ID, otherProject); e != nil {
					t.Fatal(e)
				}
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			select {
			case result := <-done:
				if !errors.Is(result.err, authority.ErrInvalid) || len(result.widgets) != 0 {
					t.Fatal("stale owner or ancestry crossed wait", len(result.widgets), result.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func publisherEditorContext(t *testing.T, f developerFixture, p apps.Project, a apps.App) (context.Context, apps.Scope) {
	t.Helper()
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,50,now(),now(),now())`, authorityUUID(t), a.PublisherID, f.other)
	proof, e := f.v.Authenticate(context.Background(), f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	return f.guard.Bind(context.Background(), proof), apps.Scope{Actor: f.other, Developer: f.dev, Project: p.ID, App: a.ID}
}

func TestPGAppSecretIssuerOwnerPolicy(t *testing.T) {
	f, p, a, secret := publisherOwnerApp(t)
	ctx, s := publisherEditorContext(t, f, p, a)
	created, e := f.repo.Secrets(ctx, s, "", &apps.SecretRequest{}, "create")
	if e != nil || len(created) != 1 || created[0].Secret == nil {
		t.Fatal("healthy editor issuance", e)
	}
	rotated, e := f.repo.Secrets(ctx, s, secret.ID, &apps.SecretRequest{}, "rotate")
	if e != nil || len(rotated) != 1 || rotated[0].Secret == nil {
		t.Fatal("healthy editor rotation", e)
	}
	if _, e = f.repo.AppWidgets(ctx, a.ID, *secret.Secret); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("rotation retained old token", e)
	}
	secret = rotated[0]
	count := func() int64 {
		t.Helper()
		var n int64
		if e := f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, a.ID).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	assertDenied := func() {
		t.Helper()
		auditBefore := count()
		for _, operation := range []struct{ action, ref string }{{"create", ""}, {"rotate", secret.ID}} {
			values, e := f.repo.Secrets(ctx, s, operation.ref, &apps.SecretRequest{}, operation.action)
			if !errors.Is(e, apps.ErrForbidden) || len(values) != 0 {
				t.Fatal("disabled owner permitted credential issuance", operation.action, e)
			}
		}
		if count() != auditBefore {
			t.Fatal("denied issuance left audit")
		}
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	assertDenied()
	// Explicit credential revocation remains available for cleanup.
	if _, e = f.repo.Secrets(ctx, s, created[0].ID, nil, "delete"); e != nil {
		t.Fatal("disabled-owner cleanup denied", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	if e = authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
		t.Fatal(e)
	}
	assertDenied()
	if e = authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.AppWidgets(ctx, a.ID, *secret.Secret); e != nil {
		t.Fatal("denied rotation changed existing token", e)
	}
	// An original proof cannot authorize a substituted actor.
	wrong := s
	wrong.Actor = f.actor
	if values, e := f.repo.Secrets(ctx, wrong, "", &apps.SecretRequest{}, "create"); !errors.Is(e, authority.ErrInvalid) || len(values) != 0 {
		t.Fatal("cross-actor secret issuer", e)
	}
	// Public app metadata is intentionally unaffected by issuance eligibility.
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
	if _, e = f.repo.PublicApp(ctx, a.Slug); e != nil {
		t.Fatal("public metadata policy changed", e)
	}
}

func TestPGAppSecretIssuerOwnerWaitRollback(t *testing.T) {
	f, p, a, secret := publisherOwnerApp(t)
	editor, s := publisherEditorContext(t, f, p, a)
	ctx, cancel := context.WithTimeout(editor, 15*time.Second)
	defer cancel()
	var auditBefore int64
	if e := f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, a.ID).Scan(&auditBefore); e != nil {
		t.Fatal(e)
	}
	blocker, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_app_secrets WHERE id=$1 FOR UPDATE`, secret.ID); e != nil {
		t.Fatal(e)
	}
	type result struct {
		values []apps.Secret
		err    error
	}
	done := make(chan result, 1)
	go func() {
		v, e := f.repo.Secrets(ctx, s, secret.ID, &apps.SecretRequest{}, "rotate")
		done <- result{v, e}
	}()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_app_secrets")
	// The editor's actor lock does not lock the distinct publisher owner. This
	// committed disable must be observed after the secret-row wait.
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1`, f.actor)
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	select {
	case got := <-done:
		if !errors.Is(got.err, apps.ErrForbidden) || len(got.values) != 0 {
			t.Fatal("post-wait issuance leaked credential", len(got.values), got.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var auditAfter int64
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, a.ID).Scan(&auditAfter); e != nil || auditAfter != auditBefore {
		t.Fatal("rolled-back issuance left audit", auditBefore, auditAfter, e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor)
	if _, e = f.repo.AppWidgets(ctx, a.ID, *secret.Secret); e != nil {
		t.Fatal("rolled-back rotation changed original secret", e)
	}
	// A late audit failure must not return newly generated plaintext or persist
	// its secret row. The constraint only rejects this synthetic app's issuance.
	constraint := "owner_review_" + strings.ReplaceAll(authorityUUID(t), "-", "")
	query(t, f.db, `ALTER TABLE owned_apps_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(app_id='`+a.ID+`'::uuid AND action='developers.apps.secrets.create')) NOT VALID`)
	defer query(t, f.db, `ALTER TABLE owned_apps_audit DROP CONSTRAINT `+constraint)
	var before, after int64
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_app_secrets WHERE app_id=$1`, a.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if values, e := f.repo.Secrets(ctx, s, "", &apps.SecretRequest{}, "create"); e == nil || len(values) != 0 {
		t.Fatal("audit failure exposed generated secret", e)
	}
	if e = f.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_app_secrets WHERE app_id=$1`, a.ID).Scan(&after); e != nil || after != before {
		t.Fatal("audit failure retained secret", before, after, e)
	}
}

func TestPGBotImportedOrganizationAppBoundary(t *testing.T) {
	f := newBotFixture(t)
	b := botCreate(t, f, "owner-boundary")
	mini := botMini(t, f, "owner-boundary")
	ctx := f.ctx
	if _, e := f.repo.PublicBot(ctx, b.ID); e != nil {
		t.Fatal("healthy direct bot", e)
	}
	if _, e := f.repo.PublicMiniApp(ctx, mini.Slug); e != nil {
		t.Fatal("healthy direct miniapp", e)
	}
	query(t, f.db, socialpg.Schema)
	realm := authorityUUID(t)
	query(t, f.db, `INSERT INTO owned_realms(id,slug,name,account_id,created_at,updated_at)VALUES($1,$1::text,'Synthetic imported app realm',$2,now(),now())`, realm, f.other)
	query(t, f.db, `UPDATE owned_publishers SET type=1,account_id=NULL,realm_id=$2 WHERE id=$1`, f.app.PublisherID, realm)
	before := botCount(t, f, `SELECT count(*) FROM owned_bot_audit WHERE app_id=$1`, f.app.ID)
	deny := func() {
		t.Helper()
		if v, e := f.repo.PublicBot(ctx, b.ID); !errors.Is(e, bots.ErrNotFound) || v.ID != "" || v.Developer != nil {
			t.Fatal("imported organization bot exposed", e)
		}
		if v, e := f.repo.PublicDeveloper(ctx, b.ID); !errors.Is(e, bots.ErrNotFound) || v.ID != "" || v.Publisher != nil {
			t.Fatal("imported organization developer exposed", e)
		}
		if v, e := f.repo.PublicChat(ctx, b.ID); !errors.Is(e, bots.ErrNotFound) || v.ID != "" {
			t.Fatal("imported organization chat exposed", e)
		}
		if v, e := f.repo.PublicMiniApp(ctx, mini.Slug); !errors.Is(e, bots.ErrNotFound) || v.ID != "" || v.Developer != nil {
			t.Fatal("imported organization miniapp exposed", e)
		}
		if v, e := f.repo.Bots(ctx, f.scope, b.ID, &bots.Patch{Nick: botText("Forbidden organization update")}, "update"); !errors.Is(e, bots.ErrNotFound) || len(v) != 0 {
			t.Fatal("imported organization bot mutated", e)
		}
		if v, e := f.repo.MiniApps(ctx, f.scope, mini.ID, &bots.MiniPatch{Manifest: botManifest(authorityUUID(t))}, "update"); !errors.Is(e, bots.ErrNotFound) || len(v) != 0 {
			t.Fatal("imported organization miniapp mutated", e)
		}
	}
	deny()
	query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.other)
	deny()
	query(t, f.db, `UPDATE owned_realms SET deleted_at=clock_timestamp() WHERE id=$1`, realm)
	deny()
	if n := botCount(t, f, `SELECT count(*) FROM owned_bot_audit WHERE app_id=$1`, f.app.ID); n != before {
		t.Fatal("denied organization mutation audit", before, n)
	}
	query(t, f.db, `UPDATE owned_publishers SET type=0,account_id=$2,realm_id=NULL WHERE id=$1`, f.app.PublisherID, f.actor)
	if _, e := f.repo.PublicBot(ctx, b.ID); e != nil {
		t.Fatal("restored direct bot", e)
	}
	if _, e := f.repo.PublicMiniApp(ctx, mini.Slug); e != nil {
		t.Fatal("restored direct miniapp", e)
	}
	if _, e := f.repo.Bots(ctx, f.scope, b.ID, &bots.Patch{Nick: botText("Healthy direct update")}, "update"); e != nil {
		t.Fatal("direct bot management changed", e)
	}
}
