// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/board"
)

type developerProofKey struct{}
type developerTestAuthority struct{ v *authority.Verifier }

func (a developerTestAuthority) Require(ctx context.Context, p ...string) (authority.Principal, error) {
	proof, ok := ctx.Value(developerProofKey{}).(authority.Principal)
	if !ok {
		return authority.Principal{}, authority.ErrInvalid
	}
	if len(p) == 0 {
		return a.v.AuthenticatePrincipal(ctx, proof)
	}
	return a.v.AuthorizePrincipal(ctx, proof, p...)
}

type developerFixture struct {
	db                                   *sql.DB
	repo                                 *apppg.Repository
	handler                              http.Handler
	guard                                *authoritypg.TransactionGuard
	v                                    *authority.Verifier
	actor, other, token, otherToken, dev string
}

func newDeveloperFixture(t *testing.T) developerFixture {
	t.Helper()
	db := accountDB(t)
	query(t, db, apppg.Schema)
	f := developerFixture{db: db, actor: authorityUUID(t), other: authorityUUID(t)}
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	config := authorityConfig(t, authorityKeys(t), now)
	for _, actor := range []string{f.actor, f.other} {
		if e := authoritypg.SeedSynthetic(context.Background(), db, actor, "synthetic_dev_"+strings.ReplaceAll(actor, "-", ""), time.Now()); e != nil {
			t.Fatal(e)
		}
		if e := authoritypg.New(db).ReplacePolicy(context.Background(), actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	issuer, e := authority.New(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, issuer, f.actor).Token
	f.otherToken = pgIssue(t, issuer, f.other).Token
	f.v, e = authority.NewVerifier(authoritypg.New(db), config)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(authoritypg.New(db), f.v)
	f.repo = apppg.NewWithAuthority(db, f.guard)
	svc := &apps.Service{Repository: f.repo, Authority: developerTestAuthority{f.v}}
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apps.IsAppCredentialRoute(r) {
			svc.AppHandler().ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if raw := r.Header.Get("Authorization"); raw != "" {
			p, e := f.v.AuthenticateRequest(r)
			if e != nil {
				w.WriteHeader(401)
				return
			}
			ctx = f.guard.Bind(context.WithValue(ctx, developerProofKey{}, p), p)
		}
		svc.Handler().ServeHTTP(w, r.WithContext(ctx))
	})
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1,1,now())`, f.actor)
	name := "devpub-" + strings.ReplaceAll(f.actor, "-", "")
	pub, e := accountpg.NewForCompatibilityTests(db).CreatePublisher(context.Background(), f.actor, profiles.PublisherPatch{Name: &name, Nick: &name})
	if e != nil {
		t.Fatal(e)
	}
	f.dev = pub.Name
	return f
}
func devJSON(t *testing.T, v any) string {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(raw)
}
func (f developerFixture) setup(t *testing.T) (apps.Project, apps.App, string) {
	t.Helper()
	backendCall(t, f.handler, "POST", "/api/developers/"+f.dev+"/enroll", f.token, "", 200)
	p := backendDecode[apps.Project](t, backendCall(t, f.handler, "POST", "/api/private/projects?dev="+f.dev, f.token, `{"slug":"first","name":"First"}`, 201))
	query := "?dev=" + f.dev + "&proj=" + p.ID
	a := backendDecode[apps.App](t, backendCall(t, f.handler, "POST", "/api/private/apps"+query, f.token, `{"slug":"app-`+p.ID+`","name":"Synthetic app","oauth_config":{"allowed_scopes":["accounts.profile.board"]}}`, 201))
	return p, a, query
}
func TestPGDeveloperAppsLifecycle(t *testing.T) {
	f := newDeveloperFixture(t)
	_, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendCall(t, f.handler, "GET", root+q, "", "", 401)
	backendCall(t, f.handler, "GET", root+q, f.otherToken, "", 403)
	backendCall(t, f.handler, "GET", "/api/apps/"+a.Slug, "", "", 404)
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":1}`, 200)
	backendCall(t, f.handler, "GET", "/api/apps/"+strings.ToUpper(a.Slug), "", "", 200)
	product := backendDecode[apps.Product](t, backendCall(t, f.handler, "POST", root+"/products"+q, f.token, `{"identifier":"entry","display_name":"Entry","currency":"USD","price":0.123456789,"state":{"stock_mode":"manual","stock_quantity":4}}`, 201))
	if product.Price.String() != "0.123456789" {
		t.Fatal("price precision lost", product.Price)
	}
	backendCall(t, f.handler, "POST", root+"/products"+q, f.token, `{"identifier":"entry","display_name":"Entry","currency":"USD"}`, 409)
	backendCall(t, f.handler, "POST", root+"/products"+q, f.token, `{"identifier":"unsupported","display_name":"Entry","currency":"USD","recurrence":"unconfigured-provider"}`, 501)
	backendCall(t, f.handler, "PATCH", root+"/products/"+product.ID+q, f.token, `{"state":{"is_enabled":false}}`, 200)
	backendCall(t, f.handler, "GET", "/api/apps/"+a.Slug+"/products/entry", "", "", 404)
	backendCall(t, f.handler, "PATCH", root+"/products/"+product.ID+q, f.token, `{"state":{"is_enabled":true}}`, 200)
	widget := `{"key":"weather","name":"Weather","is_enabled":true,"payload_type":"object","renderer_type":"data","required_fields":["temperature"],"field_types":[{"name":"temperature","type":"number"}],"max_payload_bytes":200}`
	backendCall(t, f.handler, "POST", root+"/board"+q, f.token, widget, 200)
	backendCall(t, f.handler, "POST", root+"/board"+q, f.token, widget, 409)
	backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{"type":1}`, 501)
	secret := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{"expires_in":"01:00:00"}`, 201))
	if secret.Secret == nil || secret.ExpiresAt.Before(time.Now()) {
		t.Fatal("secret lifetime")
	}
	list := backendDecode[[]apps.Secret](t, backendCall(t, f.handler, "GET", root+"/secrets"+q, f.token, "", 200))
	if len(list) != 1 || list[0].Secret != nil {
		t.Fatal("secret leaked in list")
	}
	var stored []byte
	if e := f.db.QueryRow(`SELECT secret_hash FROM owned_app_secrets WHERE id=$1`, secret.ID).Scan(&stored); e != nil || strings.Contains(string(stored), *secret.Secret) {
		t.Fatal("stored plaintext", e)
	}
	appPath := root + "/board/widgets"
	backendCall(t, f.handler, "GET", appPath, *secret.Secret, "", 200)
	backendCall(t, f.handler, "GET", appPath, f.token, "", 401)
	backendCall(t, f.handler, "GET", root+"/board/widgets?access_token=bad", *secret.Secret, "", 401)
	req := httptest.NewRequest("GET", appPath, nil)
	req.Header.Set("Authorization", "Bearer "+*secret.Secret)
	req.Header.Set("X-App-Secret", *secret.Secret)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("mixed identity accepted", w.Code)
	}
	service := board.Service{Repository: boardpg.NewForCompatibilityTests(f.db, nil), Catalog: f.repo, Normalizer: board.DocumentedEnvelope{}, Cache: board.NewGroupCache()}
	key := "weather"
	layout, e := service.Replace(context.Background(), f.other, []board.Item{{Kind: board.CustomApp, CustomAppID: &a.ID, CustomAppWidgetKey: &key, IsEnabled: true}})
	if e != nil || len(layout) != 1 {
		t.Fatal("canonical board placement", e)
	}
	body := `{"account_id":"` + f.other + `","widget_key":"weather","payload":{"temperature":{"value":18,"label":"Temperature"}}}`
	backendCall(t, f.handler, "POST", root+"/board/payload", *secret.Secret, body, 403)
	backendCall(t, f.handler, "PUT", "/api/apps/"+a.Slug+"/consent", f.otherToken, `{"scopes":["accounts.profile.board"],"expires_in":"01:00:00"}`, 200)
	out := backendDecode[board.Item](t, backendCall(t, f.handler, "POST", root+"/board/payload", *secret.Secret, body, 200))
	if out.ID != layout[0].ID {
		t.Fatal("wrong board item")
	}
	// An app credential cannot bypass a currently disabled owner or recipient.
	// Scheduled/expired punishments remain half-open; block_login is not disable.
	before, later := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for _, subject := range []string{f.actor, f.other} {
		for _, punishment := range []authority.Punishment{{Kind: "disable_account", StartsAt: &later}, {Kind: "disable_account", ExpiresAt: &before}, {Kind: "block_login"}} {
			if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), subject, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{punishment}); e != nil {
				t.Fatal(e)
			}
			backendCall(t, f.handler, "POST", root+"/board/payload", *secret.Secret, body, 200)
		}
		if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), subject, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
			t.Fatal(e)
		}
		status := 403
		if subject == f.actor {
			status = 401
		}
		backendCall(t, f.handler, "POST", root+"/board/payload", *secret.Secret, body, status)
		if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), subject, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
			t.Fatal(e)
		}
	}
	backendCall(t, f.handler, "POST", root+"/board/payload", *secret.Secret, `{"account_id":"`+f.actor+`","widget_key":"weather","payload":{}}`, 403)
	rotate := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets/"+secret.ID+"/rotate"+q, f.token, `{}`, 200))
	backendCall(t, f.handler, "GET", appPath, *secret.Secret, "", 401)
	backendCall(t, f.handler, "POST", root+"/board/payload", *rotate.Secret, body, 200)
	backendCall(t, f.handler, "DELETE", "/api/apps/"+a.Slug+"/consent", f.otherToken, "", 204)
	backendCall(t, f.handler, "POST", root+"/board/payload", *rotate.Secret, body, 403)
	backendCall(t, f.handler, "DELETE", root+"/secrets/"+secret.ID+q, f.token, "", 204)
	backendCall(t, f.handler, "GET", appPath, *rotate.Secret, "", 401)
	// A new repository/pool uses the same canonical state, not process-local maps.
	persisted, e := apppg.New(database(t)).PublicProducts(context.Background(), a.Slug, "entry")
	if e != nil || len(persisted) != 1 || persisted[0].ID != product.ID {
		t.Fatal("persistent product", e)
	}
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":2}`, 200)
	backendCall(t, f.handler, "GET", "/api/apps/"+a.Slug, "", "", 404)
	backendCall(t, f.handler, "DELETE", root+"/products/"+product.ID+q, f.token, "", 204)
	backendCall(t, f.handler, "DELETE", root+q, f.token, "", 204)
	backendCall(t, f.handler, "GET", root+q, f.token, "", 404)
}
func TestPGDeveloperAppsIsolationAndRevocation(t *testing.T) {
	f := newDeveloperFixture(t)
	p, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":1}`, 200)
	otherApp := backendDecode[apps.App](t, backendCall(t, f.handler, "POST", "/api/private/apps"+q, f.token, `{"slug":"other-`+p.ID+`","name":"Other","status":1,"oauth_config":{"allowed_scopes":["accounts.profile.board"]}}`, 201))
	secret := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{}`, 201))
	backendCall(t, f.handler, "GET", "/api/private/apps/"+otherApp.ID+"/board/widgets", *secret.Secret, "", 401)
	backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{"expires_in":"9999h"}`, 400)
	backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{"scopes":["accounts.*"]}`, 403)
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"publisher_id":"`+f.other+`"}`, 400)
	query(t, f.db, `UPDATE owned_app_secrets SET expires_at=created_at+interval '1 microsecond' WHERE id=$1`, secret.ID)
	backendCall(t, f.handler, "GET", root+"/board/widgets", *secret.Secret, "", 401)
	proof, e := f.v.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := f.guard.Bind(context.Background(), proof)
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*", Deny: true}}, nil); e != nil {
		t.Fatal(e)
	}
	name := "must not write"
	_, e = f.repo.Apps(ctx, apps.Scope{Actor: f.actor, Developer: f.dev, Project: p.ID, App: a.ID}, &apps.AppPatch{Name: &name}, "update")
	if !errors.Is(e, authority.ErrForbidden) {
		t.Fatal("fresh transaction permission ignored", e)
	}
	var actual string
	if e = f.db.QueryRow(`SELECT name FROM owned_custom_apps WHERE id=$1`, a.ID).Scan(&actual); e != nil || actual == name {
		t.Fatal("denied mutation changed state", e)
	}
}

// The guarded runner executes this in both phases. The record contains only
// synthetic IDs and an expiring synthetic token inside its disposable root.
func TestPGDeveloperAppsRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "developer-apps-restart.json")
	if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var state struct{ ID, Slug, Secret string }
		if e = json.Unmarshal(raw, &state); e != nil {
			t.Fatal(e)
		}
		repo := apppg.New(db)
		a, e := repo.PublicApp(context.Background(), state.Slug)
		if e != nil || a.ID != state.ID {
			t.Fatal("app lost on postgres restart", e)
		}
		if _, e = repo.AppWidgets(context.Background(), state.ID, state.Secret); e != nil {
			t.Fatal("app identity lost on postgres restart", e)
		}
		return
	}
	f := newDeveloperFixture(t)
	_, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":1}`, 200)
	secret := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{"expires_in":"01:00:00"}`, 201))
	if e := os.WriteFile(path, []byte(devJSON(t, struct{ ID, Slug, Secret string }{a.ID, a.Slug, *secret.Secret})), 0600); e != nil {
		t.Fatal(e)
	}
}

func TestPGDeveloperAppsConcurrentRevocation(t *testing.T) {
	f := newDeveloperFixture(t)
	p, a, q := f.setup(t)
	root := "/api/private/apps/" + a.ID
	backendCall(t, f.handler, "PATCH", root+q, f.token, `{"status":1}`, 200)
	secret := backendDecode[apps.Secret](t, backendCall(t, f.handler, "POST", root+"/secrets"+q, f.token, `{}`, 201))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A revocation which already owns the credential row wins over a concurrent reader.
	blocker, e := f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	var pid int
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_app_secrets SET revoked_at=statement_timestamp() WHERE id=$1`, secret.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := f.repo.AppWidgets(ctx, a.ID, *secret.Secret); done <- e }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_app_secrets")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("revoked secret crossed row-lock wait", e)
	}
	// The account proof is also revalidated after an account-row lock wait.
	proof, e := f.v.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	blocker, e = f.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Rollback()
	if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, f.actor); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.ExecContext(ctx, `UPDATE owned_authority_sessions SET revoked_at=statement_timestamp(),epoch=epoch+1 WHERE id=$1`, proof.Session.ID); e != nil {
		t.Fatal(e)
	}
	guarded := f.guard.Bind(ctx, proof)
	name := "denied after wait"
	go func() {
		_, e := f.repo.Apps(guarded, apps.Scope{Actor: f.actor, Developer: f.dev, Project: p.ID, App: a.ID}, &apps.AppPatch{Name: &name}, "update")
		done <- e
	}()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: f.db, ctx: ctx}, pid, "owned_authority_accounts")
	if e = blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, authority.ErrRevoked) {
		t.Fatal("revoked owner proof crossed row-lock wait", e)
	}
	var current string
	if e = f.db.QueryRowContext(ctx, `SELECT name FROM owned_custom_apps WHERE id=$1`, a.ID).Scan(&current); e != nil || current == name {
		t.Fatal("blocked mutation committed", e)
	}
}
