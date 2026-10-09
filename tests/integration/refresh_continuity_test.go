// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/messager/calls"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

func continuitySession(t *testing.T, db *sql.DB, actor, session string) authority.Session {
	t.Helper()
	var out authority.Session
	e := authoritypg.New(db).ReadAccount(context.Background(), actor, func(tx authority.Transaction) error { var e error; out, e = tx.Session(session); return e })
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPGRefreshContinuityMigrationPreservesStaleCaptures(t *testing.T) {
	db := database(t)
	tx, e := db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	// A connection-local legacy table exercises the real migration without
	// altering the harness's canonical authority schema or another fixture.
	if _, e = tx.Exec(`CREATE TEMP TABLE owned_authority_sessions(id integer PRIMARY KEY,epoch bigint NOT NULL); INSERT INTO owned_authority_sessions VALUES(1,7); CREATE TEMP TABLE continuity_captures(kind text,captured_epoch bigint); INSERT INTO continuity_captures VALUES('current',7),('stale',6)`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(authoritypg.ContinuitySchema); e != nil {
		t.Fatal(e)
	}
	var current, stale bool
	if e = tx.QueryRow(`SELECT (SELECT s.authorization_epoch=c.captured_epoch FROM continuity_captures c WHERE kind='current'),(SELECT s.authorization_epoch=c.captured_epoch FROM continuity_captures c WHERE kind='stale') FROM owned_authority_sessions s WHERE id=1`).Scan(&current, &stale); e != nil || !current || stale {
		t.Fatal("backfill revived stale capture", current, stale, e)
	}
	if _, e = tx.Exec(`UPDATE owned_authority_sessions SET epoch=8 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(authoritypg.ContinuitySchema); e != nil {
		t.Fatal(e)
	}
	var epoch, authorization int64
	if e = tx.QueryRow(`SELECT epoch,authorization_epoch FROM owned_authority_sessions WHERE id=1`).Scan(&epoch, &authorization); e != nil || epoch != 8 || authorization != 7 {
		t.Fatal("migration rerun rewrote durable generation", epoch, authorization, e)
	}
	for _, invalid := range []int64{0, 9} {
		if _, e = tx.Exec(`SAVEPOINT generation_constraint`); e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(`UPDATE owned_authority_sessions SET authorization_epoch=$1::bigint WHERE id=1`, invalid)
		var violation *pgconn.PgError
		if !errors.As(e, &violation) || violation.Code != "23514" || violation.ConstraintName != "owned_authority_sessions_authorization_epoch_check" {
			t.Fatal("invalid generation did not hit the exact database constraint", invalid, e)
		}
		if _, e = tx.Exec(`ROLLBACK TO SAVEPOINT generation_constraint`); e != nil {
			t.Fatal(e)
		}
	}
	// A separate single-connection pool makes its local temporary legacy table
	// visible to the same real readiness query, without changing shared tables.
	shadow := database(t)
	shadow.SetMaxOpenConns(1)
	if _, e = shadow.Exec(`CREATE TEMP TABLE owned_authority_sessions(id integer PRIMARY KEY,epoch bigint NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(shadow).ContinuityReady(context.Background()); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("missing migration appeared ready", e)
	}
	if _, e = shadow.Exec(authoritypg.ContinuitySchema); e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(shadow).ContinuityReady(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestPGRefreshContinuityConcurrentRotationAndScopeRollback(t *testing.T) {
	f := newCallFixture(t)
	pair := pgIssue(t, f.issuer, f.owner)
	p, e := f.verifier.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	const n = 12
	results := make(chan authority.Pair, n)
	failures := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.issuer.Refresh(context.Background(), pair.RefreshToken, 0, "")
			results <- v
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
	var rotated authority.Pair
	for v := range results {
		if rotated.Token == "" {
			rotated = v
		}
		if rotated != v {
			t.Fatal("concurrent refresh diverged")
		}
	}
	se := continuitySession(t, f.db, f.owner, p.Session.ID)
	if se.Epoch != 2 || se.AuthorizationEpoch != 1 {
		t.Fatal("rotation changed authorization", se)
	}
	if _, e = f.verifier.Authenticate(context.Background(), pair.Token); !errors.Is(e, authority.ErrStale) {
		t.Fatal("old token accepted", e)
	}
	store := authoritypg.New(f.db)
	fault := errors.New("synthetic rollback")
	e = store.WithinAccount(context.Background(), f.owner, func(tx authority.Transaction) error {
		next, x := tx.Session(se.ID)
		if x != nil {
			return x
		}
		next.Scopes = []string{"posts.read"}
		if x = tx.SaveSession(next); x != nil {
			return x
		}
		return fault
	})
	if !errors.Is(e, fault) {
		t.Fatal(e)
	}
	if got := continuitySession(t, f.db, f.owner, se.ID); got.Epoch != 2 || got.AuthorizationEpoch != 1 {
		t.Fatal("rollback leaked generation", got)
	}
	for _, scopes := range [][]string{{"posts.read"}, {"*"}} {
		old := continuitySession(t, f.db, f.owner, se.ID)
		next := old
		next.Scopes = scopes
		want, x := authority.PrepareSessionWrite(&old, next)
		if x != nil {
			t.Fatal(x)
		}
		if e = store.WithinAccount(context.Background(), f.owner, func(tx authority.Transaction) error { return tx.SaveSession(next) }); e != nil {
			t.Fatal(e)
		}
		got := continuitySession(t, f.db, f.owner, se.ID)
		if got.Epoch != want.Epoch || got.AuthorizationEpoch != want.AuthorizationEpoch {
			t.Fatal("SQL/memory transition mismatch", got, want)
		}
	}
	tx, e := f.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	ok, e := f.guard.StoredAuthenticationEligible(context.Background(), f.db, tx, f.owner, se.ID, p.Account.Version, 1)
	if e != nil || ok {
		t.Fatal("scope restoration revived capture", ok, e)
	}
}

func continuityApp(t *testing.T, db *sql.DB, cfg authority.Config, issuer *authority.Service, endpoint string) (*application.Backend, *streamChatProvider) {
	t.Helper()
	p := &streamChatProvider{streamProvider: &streamProvider{endpoint: endpoint}}
	visibility := int32(0)
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
	chat := chatStreamConfig()
	app, e := application.NewPostgres(db, nil, application.Config{Authority: cfg, SessionService: issuer, PublicVisibility: &visibility, MessagerPolicy: &policy, Livestreams: &application.LivestreamConfig{Provider: p, Policy: streamConfig(), Chat: &chat}})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.Ready(context.Background()); e != nil {
		t.Fatal(e)
	}
	return app, p
}
func continuityBroadcast(t *testing.T, db *sql.DB, app *application.Backend, actor, token string) live.Stream {
	t.Helper()
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at)VALUES($1::uuid,2,clock_timestamp()-interval '1 second')`, actor)
	pub := backendDecode[accounts.Publisher](t, backendJSONCall(t, app, "POST", "/sphere/publishers/individual", token, map[string]string{"name": "continuity-" + actor, "nick": "Synthetic continuity"}, 200))
	s := backendDecode[live.Stream](t, backendJSONCall(t, app, "POST", "/sphere/livestreams?pub="+pub.Name, token, map[string]string{"title": "Synthetic continuous stream"}, 200))
	backendJSONCall(t, app, "POST", "/api/livestreams/"+s.ID+"/start", token, map[string]bool{"no_ingress": true}, 200)
	return s
}
func continuityPhase(t *testing.T, db *sql.DB, app *application.Backend, id, want string) {
	t.Helper()
	query(t, db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid`, id)
	if _, e := app.ProcessExternalDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	var phase string
	if e := db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1::uuid`, id).Scan(&phase); e != nil || phase != want {
		t.Fatal("stream phase", phase, want, e)
	}
}

func TestPGBackendRefreshContinuesBroadcastChatThenLogoutRevokes(t *testing.T) {
	f := newBackendFixture(t)
	owner := pgIssue(t, f.issuer, f.actor)
	viewer := pgIssue(t, f.issuer, f.other)
	app, provider := continuityApp(t, f.db, f.config, f.issuer, "wss://synthetic-continuity-"+f.actor+".invalid")
	s := continuityBroadcast(t, f.db, app, f.actor, owner.Token)
	path := "/api/livestreams/" + s.ID
	admission := backendDecode[live.TokenResponse](t, backendCall(t, app, "GET", path+"/token", viewer.Token, "", 200))
	msg := backendDecode[live.ChatMessage](t, backendJSONCall(t, app, "POST", path+"/chat", viewer.Token, map[string]string{"content": "Queued before rotation"}, 200))
	rotate := func(pair authority.Pair) authority.Pair {
		return backendDecode[authority.Pair](t, backendJSONCall(t, app, "POST", "/stargate/auth/token", "", map[string]string{"grant_type": "refresh_token", "refresh_token": pair.RefreshToken}, 200))
	}
	owner2, viewer2 := rotate(owner), rotate(viewer)
	backendCall(t, app, "GET", path+"/token", owner.Token, "", 401)
	backendCall(t, app, "GET", path+"/chat", viewer.Token, "", 401)
	continuityPhase(t, f.db, app, s.ID, "active")
	backendJSONCall(t, app, "POST", path+"/start", owner2.Token, map[string]bool{"no_ingress": true}, 200)
	packets := provider.snapshot()
	if len(packets) != 1 || len(packets[0].Recipients) != 1 || packets[0].Recipients[0] != admission.Identity {
		t.Fatal("refresh lost queued chat recipient", packets)
	}
	history := backendDecode[[]live.ChatMessage](t, backendCall(t, app, "GET", path+"/chat", viewer2.Token, "", 200))
	if len(history) != 1 || history[0].ID != msg.ID || history[0].DeliveryState != "accepted" {
		t.Fatal("chat did not survive refresh", history)
	}
	backendJSONCall(t, app, "POST", path+"/chat", viewer2.Token, map[string]string{"content": "After rotation"}, 200)
	backendCall(t, app, "DELETE", "/stargate/sessions/current", owner2.Token, "", 204)
	continuityPhase(t, f.db, app, s.ID, "done")
}

func TestPGRefreshContinuityPasswordAndFactorEndBroadcast(t *testing.T) {
	for _, kind := range []string{"password", "factor"} {
		t.Run(kind, func(t *testing.T) {
			f := newSigninFixture(t)
			account, _ := f.register(t, true)
			if e := authoritypg.New(f.db).ReplacePolicy(f.ctx, account.ID, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
				t.Fatal(e)
			}
			pair := f.login(t, account.Name, syntheticPassword)
			app, _ := continuityApp(t, f.db, authorityConfig(t, f.keys, f.now), f.issuer, "wss://synthetic-security-"+account.ID+".invalid")
			s := continuityBroadcast(t, f.db, app, account.ID, pair.Token)
			var factor signin.Factor
			if kind == "factor" {
				var e error
				factor, e = f.service.CreateFactor(f.ctx, pair.Token, 3, "")
				if e != nil {
					t.Fatal(e)
				}
			}
			rotated, e := f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, "")
			if e != nil {
				t.Fatal(e)
			}
			continuityPhase(t, f.db, app, s.ID, "active")
			if kind == "password" {
				e = f.service.ChangePassword(f.ctx, rotated.Token, syntheticPassword, "synthetic changed password only 5678")
			} else {
				_, e = f.service.EnableFactor(f.ctx, rotated.Token, factor.ID, syntheticTOTP(t, factor.CreatedResponse["uri"], f.now.Load()))
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.verifier.Authenticate(f.ctx, rotated.Token); !errors.Is(e, authority.ErrStale) {
				t.Fatal("security mutation retained token", e)
			}
			continuityPhase(t, f.db, app, s.ID, "done")
		})
	}
}

func TestPGRefreshContinuityCallAdmissionAndScopeRemoval(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	pair := pgIssue(t, f.issuer, f.owner)
	ctx := f.ctx(t, pair.Token)
	joined, e := f.repo.Join(ctx, f.owner, room.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	rotated, e := f.issuer.Refresh(context.Background(), pair.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProcessDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = f.db.QueryRow(`SELECT state FROM call_v1.members WHERE call_id=$1 AND account_id=$2`, joined.CallID, f.owner).Scan(&state); e != nil || state != "admitted" {
		t.Fatal("refresh removed call admission", state, e)
	}
	if _, e = f.repo.Join(f.ctx(t, rotated.Token), f.owner, room.ID, ""); e != nil {
		t.Fatal("rotated call rejoin failed", e)
	}
	p, e := f.verifier.Authenticate(context.Background(), rotated.Token)
	if e != nil {
		t.Fatal(e)
	}
	e = authoritypg.New(f.db).WithinAccount(context.Background(), f.owner, func(tx authority.Transaction) error {
		s, e := tx.Session(p.Session.ID)
		if e != nil {
			return e
		}
		s.Scopes = []string{"posts.read"}
		return tx.SaveSession(s)
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProcessDue(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	if e = f.db.QueryRow(`SELECT state FROM call_v1.members WHERE call_id=$1 AND account_id=$2`, joined.CallID, f.owner).Scan(&state); e != nil || state != "left" {
		t.Fatal("scope removal retained admission", state, e)
	}
}

func TestPGRefreshContinuityStreamScopePolicyAndAncestorRevocation(t *testing.T) {
	for _, kind := range []string{"session_scope", "permission", "punishment", "ancestor", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := newStreamFixture(t)
			pair := pgIssue(t, f.issuer, f.owner)
			p, e := f.verifier.Authenticate(f.ctx, pair.Token)
			if e != nil {
				t.Fatal(e)
			}
			f.ownerCtx = f.guard.Bind(f.ctx, p)
			s := streamCreate(t, f, "continuity-revocation")
			streamNative(t, f, s.ID)
			if _, e = f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, ""); e != nil {
				t.Fatal(e)
			}
			streamDue(t, f, s.ID)
			store := authoritypg.New(f.db)
			switch kind {
			case "session_scope":
				e = store.WithinAccount(f.ctx, f.owner, func(tx authority.Transaction) error {
					se, e := tx.Session(p.Session.ID)
					if e != nil {
						return e
					}
					se.Scopes = []string{"posts.read"}
					return tx.SaveSession(se)
				})
			case "permission":
				e = store.ReplacePolicy(f.ctx, f.owner, []authority.Rule{{Pattern: "posts.*"}}, nil)
			case "punishment":
				e = store.ReplacePolicy(f.ctx, f.owner, []authority.Rule{{Pattern: "*"}}, []authority.Punishment{{Kind: "permission_modification", Pattern: "livestreams.*"}})
			case "ancestor":
				parent := pgIssue(t, f.issuer, f.owner)
				pp, x := f.verifier.Authenticate(f.ctx, parent.Token)
				if x != nil {
					t.Fatal(x)
				}
				query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$2::uuid WHERE id=$1::uuid`, p.Session.ID, pp.Session.ID)
				e = f.issuer.Revoke(f.ctx, parent.Token, "current")
			case "disabled":
				e = store.WithinAccount(f.ctx, f.owner, func(tx authority.Transaction) error { a := tx.Account(); a.Disabled = true; return tx.SaveAccount(a) })
			}
			if e != nil {
				t.Fatal(e)
			}
			streamDue(t, f, s.ID)
			var phase string
			if e = f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1::uuid`, s.ID).Scan(&phase); e != nil || phase != "done" {
				t.Fatal("revoked stream retained", kind, phase, e)
			}
		})
	}
}

func TestPGRefreshContinuityCallAccountWaitRollsBack(t *testing.T) {
	f := newCallFixture(t)
	room := createChat(t, f.chatFixture, true)
	joined, e := f.repo.Join(f.ctx(t, f.token), f.owner, room.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProjectEvents(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE call_v1.calls SET updated_at=clock_timestamp()-interval '1 day' WHERE id=$1::uuid`, joined.CallID)
	var before time.Time
	if e = f.db.QueryRow(`SELECT updated_at FROM call_v1.calls WHERE id=$1::uuid`, joined.CallID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	block, e := f.db.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback()
	if _, e = block.Exec(`SELECT 1 FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, f.owner); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, e = f.repo.ProcessDue(ctx, 1); !errors.Is(e, calls.ErrConflict) {
		t.Fatal("secondary account lock did not fail retryably", e)
	}
	var after time.Time
	var state string
	if e = f.db.QueryRow(`SELECT c.updated_at,m.state FROM call_v1.calls c JOIN call_v1.members m ON m.call_id=c.id WHERE c.id=$1::uuid AND m.account_id=$2::uuid`, joined.CallID, f.owner).Scan(&after, &state); e != nil || !before.Equal(after) || state != "admitted" {
		t.Fatal("contended maintenance committed partial work", before, after, state, e)
	}
	if e = block.Rollback(); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ProcessDue(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
}

func TestPGRefreshContinuityRestartPersistence(t *testing.T) {
	db := database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-refresh-continuity.json")
	type state struct{ Actor, Session, Stream, Endpoint string }
	var saved state
	if os.Getenv("PG_TEST_PHASE") == "before" {
		f := newStreamFixture(t)
		pair := pgIssue(t, f.issuer, f.owner)
		p, e := f.verifier.Authenticate(f.ctx, pair.Token)
		if e != nil {
			t.Fatal(e)
		}
		f.ownerCtx = f.guard.Bind(f.ctx, p)
		s := streamCreate(t, f, "continuity-restart")
		streamNative(t, f, s.ID)
		if _, e = f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, ""); e != nil {
			t.Fatal(e)
		}
		saved = state{f.owner, p.Session.ID, s.ID, f.provider.Endpoint()}
		raw, e := json.Marshal(saved)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	} else if os.Getenv("PG_TEST_PHASE") == "after" {
		raw, e := os.ReadFile(path)
		if e != nil || json.Unmarshal(raw, &saved) != nil {
			t.Fatal("missing synthetic restart state", e)
		}
	} else {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	se := continuitySession(t, db, saved.Actor, saved.Session)
	if se.Epoch != 2 || se.AuthorizationEpoch != 1 {
		t.Fatal("generation lost across restart", se)
	}
	f := newStreamFixture(t)
	f.provider.endpoint = saved.Endpoint
	streamDue(t, f, saved.Stream)
	var phase string
	if e := db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1`, saved.Stream).Scan(&phase); e != nil || phase != "active" {
		t.Fatal("restart revoked valid refreshed capture", phase, e)
	}
}
