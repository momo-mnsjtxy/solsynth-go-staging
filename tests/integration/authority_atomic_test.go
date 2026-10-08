// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	profiles "solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

type atomicAuthorityFixture struct {
	db                      *sql.DB
	ctx                     context.Context
	actor, publisher, token string
	principal               authority.Principal
	config                  authority.Config
	guard                   *authoritypg.TransactionGuard
	verifier                *authority.Verifier
	issuer                  *authority.Service
	posts                   *postpg.Repository
	wallets                 *walletpg.Repository
	wallet                  wallet.Wallet
}

func newAtomicAuthorityFixture(t *testing.T) atomicAuthorityFixture {
	t.Helper()
	// database(t) verifies the marked, guarded synthetic cluster before any I/O.
	db := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	f := atomicAuthorityFixture{db: db, ctx: ctx, actor: authorityUUID(t)}
	now := &atomic.Int64{}
	now.Store(time.Now().UTC().Unix())
	f.config = authorityConfig(t, authorityKeys(t), now)
	if err := authoritypg.SeedSynthetic(ctx, db, f.actor, "synthetic_atomic_"+strings.ReplaceAll(f.actor, "-", ""), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	store := authoritypg.New(db)
	if err := store.ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "posts.*"}, {Pattern: "wallets.*"}}, nil); err != nil {
		t.Fatal(err)
	}
	var err error
	// An independent SQL pool is the authority writer; serialization must come
	// from PostgreSQL, not a process mutex or the domain's connection pool.
	f.issuer, err = authority.New(authoritypg.New(database(t)), f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	v, err := authority.NewVerifier(store, f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.principal, err = v.Authenticate(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	f.verifier = v
	f.guard = authoritypg.NewTransactionGuard(store, v)
	f.ctx = f.guard.Bind(ctx, f.principal)
	f.posts = postpg.NewWithAuthority(db, f.guard)
	f.wallets = walletpg.NewWithAuthority(db, f.guard)
	query(t, db, `INSERT INTO owned_publisher_quota(account_id,total,starts_at) VALUES($1,2,now())`, f.actor)
	name := "atomic_" + strings.ReplaceAll(f.actor, "-", "")
	pub, err := accountpg.NewForCompatibilityTests(db).CreatePublisher(ctx, f.actor, profiles.PublisherPatch{Name: &name, Nick: &name})
	if err != nil {
		t.Fatal(err)
	}
	f.publisher = pub.ID
	f.wallet, err = walletpg.New(db).CreateWallet(ctx, wallet.Owner{AccountID: f.actor}, "Synthetic atomic wallet")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func atomicBlocker(t *testing.T, f atomicAuthorityFixture, table, id string) (*sql.Tx, int) {
	t.Helper()
	tx, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid int
	if err = tx.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	// table is one of three hardcoded owned fixture tables supplied below.
	if _, err = tx.ExecContext(f.ctx, `SELECT id FROM `+table+` WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func waitAtomicBlocked(t *testing.T, f atomicAuthorityFixture, blocker int, table string) int {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		err := f.db.QueryRowContext(f.ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND position($2 in query)>0 LIMIT 1`, blocker, table).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("expected SQL lock wait was not observed", f.ctx.Err())
		case <-ticker.C:
		}
	}
}

func atomicDomainWrite(f atomicAuthorityFixture, domain string) error {
	if domain == "post" {
		body := "Atomic authority post"
		_, err := f.posts.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: &body})
		return err
	}
	_, err := f.wallets.SetPublicID(f.ctx, f.wallet.ID, f.wallet.Owner(), true)
	return err
}

func atomicDomainCount(t *testing.T, f atomicAuthorityFixture, domain string) int {
	t.Helper()
	var count int
	var err error
	if domain == "post" {
		err = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_text_post_audit WHERE actor_id=$1`, f.actor).Scan(&count)
	} else {
		err = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM wallet_v1.outbox WHERE aggregate_id=$1 AND event_type='wallet.public_id.changed'`, f.wallet.ID).Scan(&count)
	}
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func assertAtomicDomainState(t *testing.T, f atomicAuthorityFixture, domain string, expected int) {
	t.Helper()
	if got := atomicDomainCount(t, f, domain); got != expected {
		t.Fatal("unexpected audit/outbox effects", got, "want", expected)
	}
	var actual int
	var err error
	if domain == "post" {
		err = f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM owned_text_posts WHERE publisher_id=$1`, f.publisher).Scan(&actual)
	} else {
		err = f.db.QueryRowContext(f.ctx, `SELECT CASE WHEN public_id IS NULL THEN 0 ELSE 1 END FROM wallet_v1.wallets WHERE id=$1`, f.wallet.ID).Scan(&actual)
	}
	if err != nil || actual != expected {
		t.Fatal("unexpected durable domain state", actual, "want", expected, err)
	}
}

func TestPGAuthorityAtomicWrites(t *testing.T) {
	_ = database(t) // Mark the whole case skipped when disposable PG is absent.
	for _, domain := range []string{"post", "wallet"} {
		t.Run(domain+"_write_before_revocation", func(t *testing.T) {
			f := newAtomicAuthorityFixture(t)
			table, id := "owned_publishers", f.publisher
			if domain == "wallet" {
				table, id = "wallet_v1.wallets", f.wallet.ID
			}
			blocker, blockerPID := atomicBlocker(t, f, table, id)
			writeDone := make(chan error, 1)
			go func() { writeDone <- atomicDomainWrite(f, domain) }()
			writerPID := waitAtomicBlocked(t, f, blockerPID, table)
			revokeDone := make(chan error, 1)
			go func() { revokeDone <- f.issuer.Revoke(f.ctx, f.token, "current") }()
			waitAtomicBlocked(t, f, writerPID, "owned_authority_accounts")
			// Another account still progresses while this authority lock is held.
			other := authorityUUID(t)
			if err := authoritypg.SeedSynthetic(f.ctx, f.db, other, "synthetic_isolated_"+strings.ReplaceAll(other, "-", ""), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.issuer.IssueSession(f.ctx, other, authority.SessionOptions{Label: "Synthetic independent account", Scopes: []string{"posts.*"}}); err != nil {
				t.Fatal("unrelated authority blocked", err)
			}
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-writeDone; err != nil {
				t.Fatal("write that acquired authority first", err)
			}
			if err := <-revokeDone; err != nil {
				t.Fatal("revocation after commit", err)
			}
			assertAtomicDomainState(t, f, domain, 1)
			want := error(authority.ErrRevoked)
			if domain == "wallet" {
				want = wallet.ErrUnauthorized
			}
			if err := atomicDomainWrite(f, domain); !errors.Is(err, want) {
				t.Fatal("revoked proof permitted next write", err)
			}
			assertAtomicDomainState(t, f, domain, 1)
		})
		mutations := []struct {
			name, sql          string
			args               func(atomicAuthorityFixture) []any
			postErr, walletErr error
		}{
			{"session", `UPDATE owned_authority_sessions SET revoked_at=now(),epoch=epoch+1 WHERE id=$1`, func(f atomicAuthorityFixture) []any { return []any{f.principal.Session.ID} }, authority.ErrRevoked, wallet.ErrUnauthorized},
			{"rotation", `UPDATE owned_authority_sessions SET epoch=epoch+1 WHERE id=$1`, func(f atomicAuthorityFixture) []any { return []any{f.principal.Session.ID} }, authority.ErrStale, wallet.ErrUnauthorized},
			{"disabled", `UPDATE owned_authority_accounts SET disabled=true WHERE id=$1`, func(f atomicAuthorityFixture) []any { return []any{f.actor} }, authority.ErrDisabled, wallet.ErrForbidden},
			{"version", `UPDATE owned_authority_accounts SET version=version+1 WHERE id=$1`, func(f atomicAuthorityFixture) []any { return []any{f.actor} }, authority.ErrStale, wallet.ErrUnauthorized},
			{"grant", `DELETE FROM owned_authority_grants WHERE account_id=$1`, func(f atomicAuthorityFixture) []any { return []any{f.actor} }, authority.ErrForbidden, wallet.ErrForbidden},
			{"punishment", `INSERT INTO owned_authority_punishments(account_id,kind,pattern) VALUES($1,'permission_modification','*')`, func(f atomicAuthorityFixture) []any { return []any{f.actor} }, authority.ErrForbidden, wallet.ErrForbidden},
		}
		for _, mutation := range mutations {
			t.Run(domain+"_"+mutation.name+"_before_write", func(t *testing.T) {
				f := newAtomicAuthorityFixture(t)
				blocker, pid := atomicBlocker(t, f, "owned_authority_accounts", f.actor)
				if _, err := blocker.ExecContext(f.ctx, mutation.sql, mutation.args(f)...); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- atomicDomainWrite(f, domain) }()
				waitAtomicBlocked(t, f, pid, "owned_authority_accounts")
				if err := blocker.Commit(); err != nil {
					t.Fatal(err)
				}
				want := mutation.postErr
				if domain == "wallet" {
					want = mutation.walletErr
				}
				if err := <-done; !errors.Is(err, want) {
					t.Fatal("stale preflight proof authorized write", err)
				}
				assertAtomicDomainState(t, f, domain, 0)
			})
		}
	}
	t.Run("all_wallet_request_mutators_require_proof", func(t *testing.T) {
		f := newAtomicAuthorityFixture(t)
		amount, err := wallet.ParseAmount("1")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.issuer.Revoke(f.ctx, f.token, "current"); err != nil {
			t.Fatal(err)
		}
		checks := []func() error{
			func() error { _, e := f.wallets.CreateWallet(f.ctx, f.wallet.Owner(), "denied"); return e },
			func() error { _, e := f.wallets.SetDefault(f.ctx, f.wallet.ID, f.actor); return e },
			func() error { _, e := f.wallets.SetPublicID(f.ctx, f.wallet.ID, f.wallet.Owner(), true); return e },
			func() error {
				_, e := f.wallets.Transfer(f.ctx, wallet.TransferCommand{ActorID: f.actor, PayerWalletID: f.wallet.ID, PayeeWalletID: authorityUUID(t), PayerOwner: f.wallet.Owner(), Currency: "USD", Amount: amount, Fingerprint: strings.Repeat("a", 64)})
				return e
			},
			func() error {
				_, e := f.wallets.Transition(f.ctx, authorityUUID(t), f.wallet.Owner(), wallet.Confirm)
				return e
			},
		}
		for i, check := range checks {
			if err := check(); !errors.Is(err, wallet.ErrUnauthorized) {
				t.Fatalf("mutator %d: %v", i, err)
			}
		}
	})
	t.Run("pool_isolation_and_explicit_nonatomic_opt_in", func(t *testing.T) {
		f := newAtomicAuthorityFixture(t)
		visibility := int32(0)
		policy := messagerpg.IndependentPolicy{NoneEncryptionMode: 77, NotifyAll: 10, NotifyMentions: 20, NotifyNone: 30}
		separate := database(t)
		config := application.Config{Authority: f.config, PublicVisibility: &visibility, MessagerPolicy: &policy}
		same, err := application.NewPostgres(f.db, nil, config)
		if err != nil {
			t.Fatal(err)
		}
		backendCall(t, same, http.MethodPost, "/wallet/wallets", f.token, `{"name":"same-pool atomic fixture"}`, 200)
		backendCall(t, same, http.MethodPost, "/sphere/posts?pub="+f.publisher, f.token, `{"content":"same-pool atomic fixture"}`, 200)
		app, err := application.NewPostgres(f.db, separate, config)
		if err != nil {
			t.Fatal(err)
		}
		backendCall(t, app, http.MethodGet, "/wallet/wallets/all", f.token, "", 200)
		backendCall(t, app, http.MethodPost, "/wallet/wallets", f.token, `{"name":"separate denied"}`, 503)
		backendCall(t, app, http.MethodPost, "/sphere/posts?pub="+f.publisher, f.token, `{"content":"separate denied"}`, 503)
		config.AllowNonAtomicAuthorityWrites = true
		app, err = application.NewPostgres(f.db, separate, config)
		if err != nil {
			t.Fatal(err)
		}
		backendCall(t, app, http.MethodPost, "/wallet/wallets", f.token, `{"name":"explicit non-atomic fixture"}`, 200)
		backendCall(t, app, http.MethodPost, "/sphere/posts?pub="+f.publisher, f.token, `{"content":"explicit non-atomic fixture"}`, 200)
		// Missing proof, changed projection, actor substitution and wrong pool
		// all fail before any domain mutation even for trusted in-process callers.
		for _, ctx := range []context.Context{context.Background(), f.guard.Bind(f.ctx, authority.Principal{Account: f.principal.Account, Session: f.principal.Session})} {
			if _, err := f.wallets.SetPublicID(ctx, f.wallet.ID, f.wallet.Owner(), true); !errors.Is(err, wallet.ErrUnauthorized) {
				t.Fatal("unverified proof", err)
			}
		}
		wrong := postpg.NewWithAuthority(separate, f.guard)
		if _, err := wrong.Create(f.ctx, f.actor, f.publisher, posts.Patch{Content: postString("wrong pool")}); !errors.Is(err, authority.ErrUnavailable) {
			t.Fatal("wrong pool", err)
		}
		if _, err := f.posts.Create(f.ctx, authorityUUID(t), f.publisher, posts.Patch{Content: postString("wrong actor")}); !errors.Is(err, authority.ErrInvalid) {
			t.Fatal("actor substitution", err)
		}
		tx, err := f.db.BeginTx(f.ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = f.guard.Authorize(f.ctx, f.db, tx, f.actor, "posts.create"); !errors.Is(err, authority.ErrUnavailable) {
			t.Fatal("stale snapshot isolation accepted", err)
		}
	})
}
