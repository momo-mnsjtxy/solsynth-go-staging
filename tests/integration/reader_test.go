// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/reader"
)

type readerFetchFunc func(context.Context, string) (reader.Link, error)

func (f readerFetchFunc) Fetch(ctx context.Context, url string) (reader.Link, error) {
	return f(ctx, url)
}

type readerFixture struct {
	atomicAuthorityFixture
	clock   *atomic.Int64
	repo    *readerpg.Repository
	policy  reader.Policy
	fetches atomic.Int32
}

func newReaderFixture(t *testing.T) *readerFixture {
	t.Helper()
	db := database(t)
	query(t, db, readerpg.Schema)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &readerFixture{clock: &atomic.Int64{}}
	f.db = db
	f.ctx = ctx
	f.actor = authorityUUID(t)
	f.clock.Store(time.Now().UTC().Unix())
	f.config = authorityConfig(t, authorityKeys(t), f.clock)
	if e := authoritypg.SeedSynthetic(ctx, db, f.actor, "synthetic_reader_"+strings.ReplaceAll(f.actor, "-", ""), f.config.Now()); e != nil {
		t.Fatal(e)
	}
	store := authoritypg.New(db)
	if e := store.ReplacePolicy(ctx, f.actor, []authority.Rule{{Pattern: "cache.scrap"}}, nil); e != nil {
		t.Fatal(e)
	}
	var e error
	f.issuer, e = authority.New(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.token = pgIssue(t, f.issuer, f.actor).Token
	f.verifier, e = authority.NewVerifier(store, f.config)
	if e != nil {
		t.Fatal(e)
	}
	f.principal, e = f.verifier.Authenticate(ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.guard = authoritypg.NewTransactionGuard(store, f.verifier)
	f.ctx = f.guard.Bind(ctx, f.principal)
	f.policy, e = reader.NewPolicy([]string{"https://preview.example"})
	if e != nil {
		t.Fatal(e)
	}
	f.repo = f.repository(t, readerFetchFunc(func(ctx context.Context, url string) (reader.Link, error) {
		f.fetches.Add(1)
		return reader.Link{URL: url, Title: "Synthetic preview"}, nil
	}), 5000)
	if e = f.repo.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	if e = f.repo.InvalidateAll(f.ctx, f.actor); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *readerFixture) repository(t *testing.T, fetcher reader.Fetcher, entries int) *readerpg.Repository {
	t.Helper()
	r, e := readerpg.New(f.db, f.guard, readerpg.Config{Policy: f.policy, Fetcher: fetcher, Now: f.config.Now, TTL: 24 * time.Hour, MaxEntries: entries})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func readerKey(url string) string { s := sha256.Sum256([]byte(url)); return hex.EncodeToString(s[:]) }
func readerState(t *testing.T, f *readerFixture) (generation int64, rows, audits int) {
	t.Helper()
	e := f.db.QueryRow(`SELECT generation,(SELECT count(*) FROM owned_reader_cache),(SELECT count(*) FROM owned_reader_cache_audit WHERE actor_id=$1) FROM owned_reader_cache_state WHERE singleton`, f.actor).Scan(&generation, &rows, &audits)
	if e != nil {
		t.Fatal(e)
	}
	return
}
func TestPGReaderCacheLifecycleAndIsolation(t *testing.T) {
	f := newReaderFixture(t)
	a := "https://preview.example/Case?q=Kept"
	b := "https://preview.example/case?q=Kept"
	for _, url := range []string{a, a + "#fragment", b} {
		v, e := f.repo.Preview(f.ctx, url)
		if e != nil || v.Title != "Synthetic preview" {
			t.Fatal(v, e)
		}
	}
	if f.fetches.Load() != 2 {
		t.Fatal("URL normalization collapsed case or ignored cache", f.fetches.Load())
	}
	if e := f.repo.Invalidate(f.ctx, f.actor, a); e != nil {
		t.Fatal(e)
	}
	if _, e := f.repo.Preview(f.ctx, b); e != nil || f.fetches.Load() != 2 {
		t.Fatal("URL purge removed unrelated preview", e)
	}
	if _, e := f.repo.Preview(f.ctx, a); e != nil || f.fetches.Load() != 3 {
		t.Fatal("URL cache survived invalidation", e)
	}
	cacheOnly := f.repository(t, nil, 5000)
	if _, e := cacheOnly.Preview(context.Background(), a); e != nil {
		t.Fatal("anonymous durable cache hit", e)
	}
	if e := f.repo.Invalidate(context.Background(), f.actor, a); !errors.Is(e, authority.ErrInvalid) {
		t.Fatal("actor string substituted for proof", e)
	}
	if e := f.repo.Invalidate(f.ctx, "", a); !errors.Is(e, reader.ErrUnauthenticated) {
		t.Fatal(e)
	}
	f.clock.Add(int64((24*time.Hour + time.Second) / time.Second))
	if _, e := cacheOnly.Preview(context.Background(), a); !errors.Is(e, reader.ErrUnavailable) {
		t.Fatal("expired cache hit without fetcher", e)
	}
	if _, e := f.repo.Preview(context.Background(), a); e != nil || f.fetches.Load() != 4 {
		t.Fatal("expired preview not refreshed", e)
	}
	if _, e := f.repo.Preview(context.Background(), "https://unconfigured.example/"); !errors.Is(e, reader.ErrBlocked) || f.fetches.Load() != 4 {
		t.Fatal("policy bypass", e)
	}
}
func TestPGReaderInvalidationFencesInflightFetch(t *testing.T) {
	_ = database(t) // Mark the top-level case skipped if the guarded runner is absent.
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "url", true: "all"}[all], func(t *testing.T) {
			f := newReaderFixture(t)
			url := "https://preview.example/inflight"
			other := "https://preview.example/retained"
			if _, e := f.repo.Preview(f.ctx, other); e != nil {
				t.Fatal(e)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			fetcher := readerFetchFunc(func(ctx context.Context, url string) (reader.Link, error) {
				if calls.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
						return reader.Link{URL: url, Title: "Stale preview"}, nil
					case <-ctx.Done():
						return reader.Link{}, ctx.Err()
					}
				}
				return reader.Link{URL: url, Title: "Fresh preview"}, nil
			})
			r := f.repository(t, fetcher, 5000)
			type result struct {
				v reader.Link
				e error
			}
			done := make(chan result, 1)
			go func() { v, e := r.Preview(f.ctx, url); done <- result{v, e} }()
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal("fetch did not start", f.ctx.Err())
			}
			check, e := f.db.BeginTx(f.ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			_, e = check.ExecContext(f.ctx, `SELECT singleton FROM owned_reader_cache_state FOR UPDATE NOWAIT`)
			_ = check.Rollback()
			if e != nil {
				t.Fatal("network work held cache lock", e)
			}
			if all {
				e = f.repo.InvalidateAll(f.ctx, f.actor)
			} else {
				e = f.repo.Invalidate(f.ctx, f.actor, url)
			}
			if e != nil {
				t.Fatal(e)
			}
			close(release)
			got := <-done
			if !errors.Is(got.e, reader.ErrConflict) || got.v.URL != "" {
				t.Fatal("in-flight fill survived purge", got)
			}
			var count int
			if e = f.db.QueryRow(`SELECT count(*) FROM owned_reader_cache WHERE url=$1`, url).Scan(&count); e != nil || count != 0 {
				t.Fatal(count, e)
			}
			v, e := r.Preview(f.ctx, url)
			if e != nil || v.Title != "Fresh preview" || calls.Load() != 2 {
				t.Fatal(v, e)
			}
		})
	}
}
func TestPGReaderCacheCapacityAndAtomicRollback(t *testing.T) {
	f := newReaderFixture(t)
	url := "https://preview.example/retained"
	if _, e := f.repo.Preview(f.ctx, url); e != nil {
		t.Fatal(e)
	}
	limited := f.repository(t, readerFetchFunc(func(ctx context.Context, url string) (reader.Link, error) {
		return reader.Link{URL: url, Title: "Synthetic bounded preview"}, nil
	}), 1)
	if _, e := limited.Preview(f.ctx, "https://preview.example/full"); !errors.Is(e, reader.ErrBusy) {
		t.Fatal("unbounded durable cache", e)
	}
	g, rows, audits := readerState(t, f)
	query(t, f.db, `ALTER TABLE owned_reader_cache_audit ADD CONSTRAINT reader_audit_failure CHECK(actor_id<>'`+f.actor+`'::uuid) NOT VALID`)
	t.Cleanup(func() {
		query(t, f.db, `ALTER TABLE owned_reader_cache_audit DROP CONSTRAINT IF EXISTS reader_audit_failure`)
	})
	e := f.repo.Invalidate(f.ctx, f.actor, url)
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "reader_audit_failure" {
		t.Fatal("wrong invalidation failure", e)
	}
	a, b, c := readerState(t, f)
	if a != g || b != rows || c != audits {
		t.Fatal("invalidation partially committed", a, b, c, g, rows, audits)
	}
	query(t, f.db, `ALTER TABLE owned_reader_cache_audit DROP CONSTRAINT reader_audit_failure`)
	now := f.config.Now()
	query(t, f.db, `UPDATE owned_reader_cache SET updated_at=$2,expires_at=$3 WHERE url=$1`, url, now.Add(-2*time.Minute), now.Add(-time.Minute))
	query(t, f.db, `ALTER TABLE owned_reader_cache ADD CONSTRAINT reader_fill_failure CHECK(url NOT LIKE '%/synthetic-fill-rollback') NOT VALID`)
	t.Cleanup(func() { query(t, f.db, `ALTER TABLE owned_reader_cache DROP CONSTRAINT IF EXISTS reader_fill_failure`) })
	_, e = limited.Preview(f.ctx, "https://preview.example/synthetic-fill-rollback")
	pgerr = nil
	if !errors.As(e, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "reader_fill_failure" {
		t.Fatal("wrong fill failure", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM owned_reader_cache WHERE url=$1`, url).Scan(&count); e != nil || count != 1 {
		t.Fatal("failed fill committed earlier expiry pruning", count, e)
	}
}
func TestPGReaderInvalidationFreshAuthorityAndPool(t *testing.T) {
	_ = database(t)
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "grant", true: "session"}[revoked], func(t *testing.T) {
			f := newReaderFixture(t)
			url := "https://preview.example/protected"
			if _, e := f.repo.Preview(f.ctx, url); e != nil {
				t.Fatal(e)
			}
			g, rows, audits := readerState(t, f)
			block, pid := atomicBlocker(t, f.atomicAuthorityFixture, "owned_authority_accounts", f.actor)
			var e error
			want := authority.ErrForbidden
			if revoked {
				_, e = block.ExecContext(f.ctx, `UPDATE owned_authority_sessions SET revoked_at=$2,epoch=epoch+1 WHERE id=$1`, f.principal.Session.ID, f.config.Now())
				want = authority.ErrRevoked
			} else {
				_, e = block.ExecContext(f.ctx, `DELETE FROM owned_authority_grants WHERE account_id=$1`, f.actor)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- f.repo.InvalidateAll(f.ctx, f.actor) }()
			waitAtomicBlocked(t, f.atomicAuthorityFixture, pid, "owned_authority_accounts")
			if e = block.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, want) {
				t.Fatal("stale proof authorized purge", e)
			}
			a, b, c := readerState(t, f)
			if a != g || b != rows || c != audits {
				t.Fatal("failed authority changed cache", a, b, c)
			}
		})
	}
	f := newReaderFixture(t)
	wrong, e := readerpg.New(database(t), f.guard, readerpg.Config{Policy: f.policy, Now: f.config.Now})
	if e != nil {
		t.Fatal(e)
	}
	if e = wrong.InvalidateAll(f.ctx, f.actor); !errors.Is(e, authority.ErrUnavailable) {
		t.Fatal("cross-pool guard accepted", e)
	}
}
func TestPGReaderRestartPersistence(t *testing.T) {
	const retained = "https://preview.example/synthetic-restart/Case?Value=Kept"
	const removed = "https://preview.example/synthetic-restart/removed"
	if os.Getenv("PG_TEST_PHASE") == "after" {
		db := database(t)
		var updated time.Time
		if e := db.QueryRow(`SELECT updated_at FROM owned_reader_cache WHERE url=$1`, retained).Scan(&updated); e != nil {
			t.Fatal(e)
		}
		policy, e := reader.NewPolicy([]string{"https://preview.example"})
		if e != nil {
			t.Fatal(e)
		}
		r, e := readerpg.New(db, authoritypg.NewTransactionGuard(authoritypg.New(db), nil), readerpg.Config{Policy: policy, Now: func() time.Time { return updated.Add(time.Minute) }})
		if e != nil {
			t.Fatal(e)
		}
		if e = r.Ready(context.Background()); e != nil {
			t.Fatal(e)
		}
		v, e := r.Preview(context.Background(), retained)
		if e != nil || v.URL != retained || v.Title != "Synthetic preview" {
			t.Fatal(v, e)
		}
		if _, e = r.Preview(context.Background(), removed); !errors.Is(e, reader.ErrUnavailable) {
			t.Fatal("invalidated cache resurrected on restart", e)
		}
		var audits int
		if e = db.QueryRow(`SELECT count(*) FROM owned_reader_cache_audit WHERE cache_key=$1 AND action='invalidate_url'`, readerKey(removed)).Scan(&audits); e != nil || audits != 1 {
			t.Fatal("durable purge audit", audits, e)
		}
		return
	}
	f := newReaderFixture(t)
	for _, url := range []string{retained, removed} {
		if _, e := f.repo.Preview(f.ctx, url); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.repo.Invalidate(f.ctx, f.actor, removed); e != nil {
		t.Fatal(e)
	}
}
