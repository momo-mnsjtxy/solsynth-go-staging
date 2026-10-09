// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

type managedOrderFixture struct {
	commerceFixture
	manager *commercepg.ManagedOrders
	scope   apps.Scope
}

func newManagedOrderFixture(t *testing.T) managedOrderFixture {
	t.Helper()
	f := managedOrderFixture{commerceFixture: newCommerceFixture(t)}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.scope.Actor, f.scope.App = f.actor, f.app
	if e := f.db.QueryRow(`SELECT a.project_id::text,p.name FROM owned_custom_apps a JOIN owned_dev_projects project ON project.id=a.project_id JOIN owned_developers d ON d.id=project.developer_id JOIN owned_publishers p ON p.id=d.publisher_id WHERE a.id=$1`, f.app).Scan(&f.scope.Project, &f.scope.Developer); e != nil {
		t.Fatal(e)
	}
	f.manager = commercepg.NewManagedOrders(f.r, apppg.NewWithAuthority(f.db, f.guard))
	return f
}

func customOrderRequest() apps.CustomOrderRequest {
	hours := 1
	return apps.CustomOrderRequest{Identifier: "ad-hoc invoice / 42", Currency: "synthetic-commerce", Amount: "3.001", ExpirationHours: &hours, Remarks: stringPointer("Synthetic custom order"), Meta: map[string]json.RawMessage{"invoice": json.RawMessage(`"42"`)}}
}

func managedOrderPath(s apps.Scope) string {
	return "/api/private/apps/" + s.App + "/orders?dev=" + s.Developer + "&proj=" + s.Project
}

func managedOrderMember(t *testing.T, f managedOrderFixture, role int, scopes []string) (string, string, context.Context) {
	t.Helper()
	actor := authorityUUID(t)
	if e := authoritypg.SeedSynthetic(f.ctx, f.db, actor, "synthetic_manager_"+strings.ReplaceAll(actor, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, actor, []authority.Rule{{Pattern: "developers.apps.update"}}, nil); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `INSERT INTO owned_publisher_members(id,publisher_id,account_id,role,joined_at,created_at,updated_at)VALUES($1,$2,$3,$4,now(),now(),now())`, authorityUUID(t), f.publisher, actor, role)
	pair, e := f.issuer.IssueSession(f.ctx, actor, authority.SessionOptions{Label: "Synthetic scoped manager", Scopes: scopes})
	if e != nil {
		t.Fatal(e)
	}
	proof, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	return actor, pair.Token, f.guard.Bind(context.Background(), proof)
}

func TestPGDeveloperStatisticsCanonicalCountsAndVisibility(t *testing.T) {
	f := newDeveloperFixture(t)
	p, a, q := f.setup(t)
	path := "/api/developers/" + f.dev + "/stats"
	assertCount := func(n int64) {
		t.Helper()
		got := backendDecode[apps.DeveloperStats](t, backendCall(t, f.handler, "GET", path, "", "", 200))
		if got.TotalCustomApps != n {
			t.Fatal(got, n)
		}
	}
	assertCount(1) // The authored public aggregate includes developing apps.
	b := backendDecode[apps.App](t, backendCall(t, f.handler, "POST", "/api/private/apps"+q, f.token, `{"slug":"second-`+a.ID+`","name":"Private app two","status":2}`, 201))
	assertCount(2)
	query(t, f.db, `UPDATE owned_custom_apps SET deleted_at=now() WHERE id=$1`, b.ID)
	assertCount(1)
	query(t, f.db, `UPDATE owned_dev_projects SET deleted_at=now() WHERE id=$1`, p.ID)
	assertCount(0)
	query(t, f.db, `UPDATE owned_dev_projects SET deleted_at=NULL WHERE id=$1`, p.ID)
	other := newDeveloperFixture(t)
	other.setup(t)
	assertCount(1)
	for _, tc := range []struct {
		disable, restore string
		id               string
	}{
		{`UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, `UPDATE owned_authority_accounts SET disabled=false WHERE id=$1`, f.actor},
		{`UPDATE owned_publishers SET deleted_at=now() WHERE id=$1`, `UPDATE owned_publishers SET deleted_at=NULL WHERE id=$1`, a.PublisherID},
		{`UPDATE owned_publishers SET shadowbanned_at=now() WHERE id=$1`, `UPDATE owned_publishers SET shadowbanned_at=NULL WHERE id=$1`, a.PublisherID},
	} {
		query(t, f.db, tc.disable, tc.id)
		backendCall(t, f.handler, "GET", path, "", "", 404)
		query(t, f.db, tc.restore, tc.id)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}}); e != nil {
		t.Fatal(e)
	}
	backendCall(t, f.handler, "GET", path, "", "", 404)
}

func TestPGDeveloperCustomOrderAuthorityAndIsolation(t *testing.T) {
	f := newManagedOrderFixture(t)
	h := backendApp(t, f.db, f.config)
	path, body := managedOrderPath(f.scope), devJSON(t, customOrderRequest())
	backendCall(t, h, "POST", path, "", body, 401)
	backendCall(t, h, "POST", path, f.credentials.ClientSecret, body, 401)
	backendCall(t, h, "GET", "/develop/developers/"+f.scope.Developer+"/stats", "", "", 200)
	viewer, viewerToken, _ := managedOrderMember(t, f, 0, []string{"developers.apps.update"})
	backendCall(t, h, "POST", path, viewerToken, body, 403)
	query(t, f.db, `UPDATE owned_publisher_members SET role=50 WHERE publisher_id=$1 AND account_id=$2`, f.publisher, viewer)
	created := backendDecode[c.Order](t, backendCall(t, h, "POST", path, viewerToken, body, 200))
	if created.Amount.String() != "3.001" || created.ProductIdentifier == nil || *created.ProductIdentifier != customOrderRequest().Identifier || len(created.Items) != 0 || created.PayeeWalletID != f.payee.ID || created.ExpiredAt.Sub(created.CreatedAt) != time.Hour {
		t.Fatal(created)
	}
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, viewer, nil, nil); e != nil {
		t.Fatal(e)
	}
	backendCall(t, h, "POST", path, viewerToken, body, 403)
	if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, viewer, []authority.Rule{{Pattern: "developers.apps.update"}}, nil); e != nil {
		t.Fatal(e)
	}
	_, narrowToken, _ := managedOrderMember(t, f, 50, []string{"developers.apps.create"})
	backendCall(t, h, "POST", path, narrowToken, body, 403)
	foreign := newManagedOrderFixture(t)
	wrong := f.scope
	wrong.Project = foreign.scope.Project
	backendCall(t, h, "POST", managedOrderPath(wrong), viewerToken, body, 404)
	wrong = f.scope
	wrong.App = foreign.app
	backendCall(t, h, "POST", managedOrderPath(wrong), viewerToken, body, 404)
	query(t, f.db, `DELETE FROM commerce_v1.merchants WHERE app_id=$1`, f.app)
	backendCall(t, h, "POST", path, viewerToken, body, 404)
	query(t, f.db, `INSERT INTO commerce_v1.merchants(app_id,wallet_id)VALUES($1,$2)`, f.app, foreign.payee.ID)
	backendCall(t, h, "POST", path, viewerToken, body, 404)
	query(t, f.db, `UPDATE commerce_v1.merchants SET wallet_id=$2 WHERE app_id=$1`, f.app, f.payee.ID)
	backendCall(t, h, "POST", path, viewerToken, `{"identifier":"x","currency":"synthetic-commerce","amount":1.0001}`, 400)
	backendCall(t, h, "POST", path, viewerToken, `{"identifier":"x","currency":"synthetic-commerce","amount":1,"payee_wallet_id":"`+foreign.payee.ID+`"}`, 400)
	query(t, f.db, `UPDATE owned_custom_apps SET status=0 WHERE id=$1`, f.app)
	backendCall(t, h, "POST", path, viewerToken, body, 403)
	query(t, f.db, `UPDATE owned_custom_apps SET status=1 WHERE id=$1`, f.app)
	query(t, f.db, `UPDATE owned_publisher_members SET revoked_at=now() WHERE publisher_id=$1 AND account_id=$2`, f.publisher, viewer)
	backendCall(t, h, "POST", path, viewerToken, body, 403)
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE app_id=$1`, f.app); n != 1 {
		t.Fatal("denial wrote orders", n)
	}
	t.Run("identifier_boundaries", testManagedOrderIdentifierBoundaries)
	t.Run("identifier_migration", testManagedOrderIdentifierMigration)
}

func testManagedOrderIdentifierBoundaries(t *testing.T) {
	f := newManagedOrderFixture(t)
	h := backendApp(t, f.db, f.config)
	if e := f.r.ManagedOrdersReady(f.ctx); e != nil {
		t.Fatal("custom order identifier migration unavailable", e)
	}
	for _, length := range []int{257, 1024} {
		q := customOrderRequest()
		q.Identifier = strings.Repeat("x", length)
		o := backendDecode[c.Order](t, backendCall(t, h, "POST", managedOrderPath(f.scope), f.token, devJSON(t, q), 200))
		var stored string
		if e := f.db.QueryRow(`SELECT product_identifier FROM commerce_v1.orders WHERE id=$1`, o.ID).Scan(&stored); e != nil || stored != q.Identifier || o.ProductIdentifier == nil || *o.ProductIdentifier != q.Identifier {
			t.Fatal("invoice identifier changed", length, stored, o, e)
		}
	}
	q := customOrderRequest()
	q.Identifier = strings.Repeat("x", 1025)
	backendCall(t, h, "POST", managedOrderPath(f.scope), f.token, devJSON(t, q), 400)
	// Widening invoice storage must not widen the older app-credential entrance.
	if _, e := f.r.CreateOrder(f.ctx, c.CreateOrder{Credentials: f.credentials, Currency: q.Currency, Amount: walletAmount(t, "1"), ProductIdentifier: stringPointer(strings.Repeat("x", 257))}, "catalog-too-long"); !errors.Is(e, c.ErrInvalid) {
		t.Fatal("catalog identifier limit widened", e)
	}
	for _, query := range []string{`SELECT count(*) FROM commerce_v1.orders WHERE app_id=$1`, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1 AND action='developers.apps.orders.create'`, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id IN(SELECT id FROM commerce_v1.orders WHERE app_id=$1) AND event_type='commerce.order.created'`} {
		if n := commerceCount(t, f.db, query, f.app); n != 2 {
			t.Fatal("boundary rejection wrote partial state", query, n)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.idempotency WHERE owner_key=$1 AND request_key='catalog-too-long'`, "app:"+f.app); n != 0 {
		t.Fatal("catalog rejection wrote idempotency", n)
	}
}

func testManagedOrderIdentifierMigration(t *testing.T) {
	db := database(t)
	schema := "synthetic_custom_identifier_" + strings.ReplaceAll(authorityUUID(t), "-", "")
	query(t, db, `CREATE SCHEMA `+schema)
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) })
	query(t, db, `CREATE TABLE `+schema+`.orders(id integer PRIMARY KEY, product_identifier varchar(256))`)
	original := strings.Repeat("x", 256)
	query(t, db, `INSERT INTO `+schema+`.orders VALUES(1,$1),(2,NULL)`, original)
	raw, e := os.ReadFile("../../internal/commercepg/custom_order_identifier.sql")
	if e != nil {
		t.Fatal(e)
	}
	migration := strings.ReplaceAll(string(raw), "commerce_v1", schema)
	failure := strings.Replace(migration, "END $$;", "RAISE EXCEPTION 'synthetic migration rollback'; END $$;", 1)
	if _, e = db.Exec(failure); e == nil {
		t.Fatal("migration rollback not exercised")
	}
	var width int
	if e = db.QueryRow(`SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=$1 AND table_name='orders' AND column_name='product_identifier'`, schema).Scan(&width); e != nil || width != 256 {
		t.Fatal("failed widening changed the schema", width, e)
	}
	query(t, db, migration)
	query(t, db, `INSERT INTO `+schema+`.orders VALUES(3,$1)`, strings.Repeat("y", 1024))
	query(t, db, migration)
	var preserved bool
	if e = db.QueryRow(`SELECT (SELECT product_identifier=$1 FROM `+schema+`.orders WHERE id=1) AND (SELECT product_identifier IS NULL FROM `+schema+`.orders WHERE id=2) AND (SELECT length(product_identifier)=1024 FROM `+schema+`.orders WHERE id=3)`, original).Scan(&preserved); e != nil || !preserved {
		t.Fatal("migration altered existing identifiers", e)
	}
	_, e = db.Exec(`INSERT INTO `+schema+`.orders VALUES(4,$1)`, strings.Repeat("z", 1025))
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "22001" {
		t.Fatal("identifier SQL bound missing", e)
	}
}

func TestPGDeveloperCustomOrderConcurrencyAtomicityAndSettlement(t *testing.T) {
	f := newManagedOrderFixture(t)
	var wg sync.WaitGroup
	results := make(chan c.Order, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := f.manager.CreateCustomOrder(f.ctx, f.scope, customOrderRequest(), "same")
			results <- o
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	var original c.Order
	for o := range results {
		if original.ID != "" && o.ID != original.ID {
			t.Fatal("parallel replay duplicated order")
		}
		original = o
	}
	for _, q := range []string{`SELECT count(*) FROM commerce_v1.orders WHERE app_id=$1`, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1 AND action='developers.apps.orders.create'`, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id IN(SELECT id FROM commerce_v1.orders WHERE app_id=$1) AND event_type='commerce.order.created'`} {
		if n := commerceCount(t, f.db, q, f.app); n != 1 {
			t.Fatal(q, n)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.order_items i JOIN commerce_v1.orders o ON o.id=i.order_id WHERE o.app_id=$1`, f.app); n != 0 {
		t.Fatal("custom order created catalog stock reservations", n)
	}
	changed := customOrderRequest()
	changed.Amount = "3.002"
	if _, e := f.manager.CreateCustomOrder(f.ctx, f.scope, changed, "same"); !errors.Is(e, c.ErrIdempotency) {
		t.Fatal(e)
	}
	query(t, f.db, `ALTER TABLE owned_apps_audit ADD CONSTRAINT synthetic_reject_custom_order CHECK(action<>'developers.apps.orders.create') NOT VALID`)
	_, rollbackErr := f.manager.CreateCustomOrder(f.ctx, f.scope, customOrderRequest(), "rollback")
	query(t, f.db, `ALTER TABLE owned_apps_audit DROP CONSTRAINT synthetic_reject_custom_order`)
	if rollbackErr == nil {
		t.Fatal("audit failure committed")
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE app_id=$1`, f.app); n != 1 {
		t.Fatal(n)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.idempotency WHERE owner_key=$1 AND request_key='rollback'`, "managed-app:"+f.app); n != 0 {
		t.Fatal(n)
	}
	// A later merchant binding cannot redirect this already captured beneficiary.
	replacement := walletCreate(t, walletpg.New(f.db), f.actor)
	query(t, f.db, `UPDATE commerce_v1.merchants SET wallet_id=$2 WHERE app_id=$1`, f.app, replacement.ID)
	replay, e := f.manager.CreateCustomOrder(f.ctx, f.scope, customOrderRequest(), "same")
	if e != nil || replay.ID != original.ID || replay.PayeeWalletID != original.PayeeWalletID {
		t.Fatal(replay, e)
	}
	if _, e = f.r.PayOrder(f.ctx, f.actor, original.ID, &f.wallet.ID); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.999", "3.001", "96.999")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "0.000", "0.000", "0.000")
	if _, e = f.r.UpdateOrder(f.ctx, original.ID, c.UpdateStatus{Credentials: f.credentials, Status: c.Finished}); e != nil {
		t.Fatal(e)
	}
	walletAssertBalance(t, walletpg.New(f.db), f.wallet.ID, "synthetic-commerce", "96.999", "0.000", "96.999")
	walletAssertBalance(t, walletpg.New(f.db), f.payee.ID, "synthetic-commerce", "3.001", "0.000", "3.001")
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, original.ID); n != 1 {
		t.Fatal(n)
	}
	expired, e := f.manager.CreateCustomOrder(f.ctx, f.scope, customOrderRequest(), "expiry")
	if e != nil {
		t.Fatal(e)
	}
	f.clock.Store(expired.ExpiredAt.Unix())
	if _, e = f.r.PayOrder(f.ctx, f.actor, expired.ID, &f.wallet.ID); !errors.Is(e, c.ErrConflict) {
		t.Fatal(e)
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM wallet_v1.transactions WHERE managed_by=$1`, expired.ID); n != 0 {
		t.Fatal(n)
	}
}

func TestPGDeveloperCustomOrderWaitRechecksOwnerAndProof(t *testing.T) {
	_ = database(t)
	for _, mutation := range []string{"owner_disabled", "owner_punished", "publisher_deleted", "app_deleted", "project_deleted", "member_revoked", "proof_expired", "audit_owner_disabled", "audit_owner_punished", "audit_proof_expired"} {
		t.Run(mutation, func(t *testing.T) {
			f := newManagedOrderFixture(t)
			actor, _, ctx := managedOrderMember(t, f, 50, []string{"developers.apps.update"})
			scope := f.scope
			scope.Actor = actor
			clock := &atomic.Int64{}
			clock.Store(f.config.Now().Unix())
			if mutation == "proof_expired" || mutation == "audit_proof_expired" {
				cfg := f.config
				cfg.Now = func() time.Time { return time.Unix(clock.Load(), 0) }
				cfg.AccessTTL = time.Second
				issuer, e := authority.New(authoritypg.New(f.db), cfg)
				if e != nil {
					t.Fatal(e)
				}
				v, e := authority.NewVerifier(authoritypg.New(f.db), cfg)
				if e != nil {
					t.Fatal(e)
				}
				pair := pgIssue(t, issuer, actor)
				proof, e := v.Authenticate(ctx, pair.Token)
				if e != nil {
					t.Fatal(e)
				}
				guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
				ctx = guard.Bind(context.Background(), proof)
				f.manager = commercepg.NewManagedOrders(f.r, apppg.NewWithAuthority(f.db, guard))
			}
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
			table, id := "owned_custom_apps", f.app
			if mutation == "publisher_deleted" {
				table, id = "owned_publishers", f.publisher
			}
			if mutation == "project_deleted" {
				table, id = "owned_dev_projects", scope.Project
			}
			if mutation == "member_revoked" {
				table = "owned_publisher_members"
				if e := f.db.QueryRow(`SELECT id::text FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2`, f.publisher, actor).Scan(&id); e != nil {
					t.Fatal(e)
				}
			}
			blocker, pid := atomicBlocker(t, locks, table, id)
			if strings.HasPrefix(mutation, "audit_") {
				if e := blocker.Rollback(); e != nil {
					t.Fatal(e)
				}
				var e error
				blocker, e = f.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				defer blocker.Rollback()
				if e = blocker.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if _, e = blocker.ExecContext(ctx, `LOCK TABLE owned_apps_audit IN ACCESS EXCLUSIVE MODE`); e != nil {
					t.Fatal(e)
				}
				table = "owned_apps_audit"
			}
			done := make(chan error, 1)
			go func() { _, e := f.manager.CreateCustomOrder(ctx, scope, customOrderRequest(), mutation); done <- e }()
			waitAtomicBlocked(t, locks, pid, table)
			var e error
			switch mutation {
			case "owner_disabled", "audit_owner_disabled":
				query(t, f.db, `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, f.actor)
			case "owner_punished", "audit_owner_punished":
				e = authoritypg.New(f.db).ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "disable_account"}})
			case "publisher_deleted":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_publishers SET deleted_at=now() WHERE id=$1`, f.publisher)
			case "app_deleted":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_custom_apps SET deleted_at=now() WHERE id=$1`, f.app)
			case "project_deleted":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_dev_projects SET deleted_at=now() WHERE id=$1`, scope.Project)
			case "member_revoked":
				_, e = blocker.ExecContext(ctx, `UPDATE owned_publisher_members SET revoked_at=now() WHERE publisher_id=$1 AND account_id=$2`, f.publisher, actor)
			case "proof_expired", "audit_proof_expired":
				clock.Add(2)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = blocker.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("stale custom-order authority committed", mutation)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.orders WHERE app_id=$1`, f.app); n != 0 {
				t.Fatal(n)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1 AND action='developers.apps.orders.create'`, f.app); n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestPGDeveloperCustomOrderSavedRestartPersistence(t *testing.T) {
	type saved struct {
		Scope         apps.Scope
		Order         c.Order
		Token         string
		Authority     restartAuthoritySnapshot
		Audit, Outbox int
	}
	const marker = "developer-custom-order.json"
	if fixtureRestartPhase(t) == "before" {
		f := newManagedOrderFixture(t)
		// Configure bounded original proof before creating the target order.
		f.config.AccessTTL, f.config.SessionTTL = time.Hour, time.Hour
		at, wall := f.config.Now(), time.Now()
		f.config.Now = func() time.Time { return at.Add(time.Since(wall)) }
		issuer, e := authority.New(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		pair := pgIssue(t, issuer, f.actor)
		v, e := authority.NewVerifier(authoritypg.New(f.db), f.config)
		if e != nil {
			t.Fatal(e)
		}
		guard := authoritypg.NewTransactionGuard(authoritypg.New(f.db), v)
		proof, e := v.Authenticate(context.Background(), pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		ctx := guard.Bind(context.Background(), proof)
		r := commercepg.New(f.db, guard, commercepg.AppAdapters(apppg.New(f.db))).WithClock(f.config.Now)
		manager := commercepg.NewManagedOrders(r, apppg.NewWithAuthority(f.db, guard))
		o, e := manager.CreateCustomOrder(ctx, f.scope, customOrderRequest(), "restart")
		if e != nil {
			t.Fatal(e)
		}
		saveRestartFixture(t, marker, saved{Scope: f.scope, Order: o, Token: pair.Token, Authority: saveAuthoritySnapshot(t, f.config), Audit: commerceCount(t, f.db, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, f.app), Outbox: commerceCount(t, f.db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1`, o.ID)})
		return
	}
	db := database(t)
	var state saved
	loadRestartFixture(t, marker, &state)
	cfg := restoreAuthoritySnapshot(t, state.Authority)
	v, e := authority.NewVerifier(authoritypg.New(db), cfg)
	if e != nil {
		t.Fatal(e)
	}
	guard := authoritypg.NewTransactionGuard(authoritypg.New(db), v)
	proof, e := v.Authenticate(context.Background(), state.Token)
	if e != nil {
		t.Fatal(e)
	}
	ctx := guard.Bind(context.Background(), proof)
	r := commercepg.New(db, guard, commercepg.AppAdapters(apppg.New(db))).WithClock(cfg.Now)
	manager := commercepg.NewManagedOrders(r, apppg.NewWithAuthority(db, guard))
	o, e := manager.CreateCustomOrder(ctx, state.Scope, customOrderRequest(), "restart")
	if e != nil || devJSON(t, o) != devJSON(t, state.Order) {
		t.Fatal("saved order/proof changed", o, e)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM owned_apps_audit WHERE app_id=$1`, state.Scope.App); n != state.Audit {
		t.Fatal(n)
	}
	if n := commerceCount(t, db, `SELECT count(*) FROM commerce_v1.outbox WHERE aggregate_id=$1`, o.ID); n != state.Outbox {
		t.Fatal(n)
	}
	h := backendApp(t, db, cfg)
	request := fixtureRawJSONRequest("POST", managedOrderPath(state.Scope), state.Token, devJSON(t, customOrderRequest()))
	request.Header.Set("Idempotency-Key", "restart")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 || backendDecode[c.Order](t, w).ID != o.ID {
		t.Fatal(w.Code, w.Body.String())
	}
}
