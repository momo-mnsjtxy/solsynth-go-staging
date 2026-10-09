// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"errors"
	"solsynth.local/go-migration/internal/federationpg"
	f "solsynth.local/go-migration/ports/federation"
	"sync"
	"testing"
	"time"
)

func TestPGFederationPublicAdmissionRequestWaitABA(t *testing.T) {
	_ = database(t)
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "read"}[read], func(t *testing.T) {
			x, target, _ := publicAdmissionFixture(t)
			if read {
				requestPublicAdmission(t, x, target)
			}
			ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
			defer cancel()
			hold, e := x.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer hold.Rollback()
			var pid int
			if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
				t.Fatal(e)
			}
			if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, x.actor); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if read {
					_, e := x.r.PublicAdmission(ctx, x.actor, x.publisher, target)
					done <- e
				} else {
					_, e := x.r.RequestPublicAdmission(ctx, x.actor, x.publisher, target)
					done <- e
				}
			}()
			waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_authority_accounts")
			if _, e = hold.ExecContext(ctx, `UPDATE owned_publisher_members SET role=0 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor); e != nil {
				t.Fatal(e)
			}
			if _, e = hold.ExecContext(ctx, `UPDATE owned_publisher_members SET role=200 WHERE publisher_id=$1::uuid AND account_id=$2::uuid`, x.publisher, x.actor); e != nil {
				t.Fatal(e)
			}
			if e = hold.Commit(); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, f.ErrForbidden) {
				t.Fatal("pre-wait admission restored", e)
			}
			if !read {
				var n int
				if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_admission_jobs WHERE publisher_id=$1::uuid`, x.publisher).Scan(&n); e != nil || n != 0 {
					t.Fatal("waiter left work", n, e)
				}
			}
		})
	}
}

func TestPGFederationPublicAdmissionCurrentPolicyAfterJobWait(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	job := requestPublicAdmission(t, x, target)
	ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
	defer cancel()
	hold, e := x.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	fetched, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	response := discoveryResponse(t, x, target)
	fetch := discoveryFetchFunc(func(c context.Context, u, a string) (f.FetchResponse, error) {
		v, e := response(c, u, a)
		if u == target.Actor {
			close(fetched)
			select {
			case <-release:
			case <-c.Done():
				return f.FetchResponse{}, c.Err()
			}
		}
		return v, e
	})
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessPublicAdmissions(ctx, fetch, 1); done <- e }()
	select {
	case <-fetched:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var pid int
	if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `SELECT id FROM owned_federation_public_admission_jobs WHERE id=$1::uuid FOR UPDATE`, job.ID); e != nil {
		t.Fatal(e)
	}
	unblock()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_public_admission_jobs")
	// Synthetic generation-only probe: canonical grant ABA is tested separately.
	syntheticFederationHistoryTransition(t, ctx, hold, "discovery", x.actor)
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil || publicAdmissionState(t, x.db, job.ID) != "cancelled" {
		t.Fatal("job-wait policy ABA survived", e)
	}
}

func TestPGFederationPublicAdmissionOriginalBoundsAndConfiguration(t *testing.T) {
	_ = database(t)
	t.Run("original-ancestry-deadline", func(t *testing.T) {
		x, target, _ := publicAdmissionFixture(t)
		parent := authorityUUID(t)
		query(t, x.db, `INSERT INTO owned_authority_sessions(id,account_id,parent_id,label,type,client_id,created_at,last_granted_at,category,trusted,epoch,authorization_epoch,account_version,expires_at,scopes)SELECT $2::uuid,account_id,NULL,'synthetic original admission ancestor',0,'',created_at,last_granted_at,'device',false,1,1,account_version,clock_timestamp()+interval '3 seconds',scopes FROM owned_authority_sessions WHERE account_id=$1::uuid`, x.actor, parent)
		query(t, x.db, `UPDATE owned_authority_sessions SET parent_id=$2::uuid WHERE account_id=$1::uuid AND id<>$2::uuid`, x.actor, parent)
		job := requestPublicAdmission(t, x, target)
		var expires time.Time
		if e := x.db.QueryRow(`SELECT expires_at FROM owned_federation_public_admission_jobs WHERE id=$1`, job.ID).Scan(&expires); e != nil {
			t.Fatal(e)
		}
		query(t, x.db, `UPDATE owned_authority_sessions SET epoch=epoch+1,expires_at=clock_timestamp()+interval '1 hour' WHERE account_id=$1::uuid`, x.actor)
		select {
		case <-time.After(time.Until(expires) + 25*time.Millisecond):
		case <-x.ctx.Done():
			t.Fatal(x.ctx.Err())
		}
		calls := 0
		if n, e := x.r.ProcessPublicAdmissions(x.ctx, discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) { calls++; return f.FetchResponse{}, nil }), 1); e != nil || n != 1 || calls != 0 || publicAdmissionState(t, x.db, job.ID) != "cancelled" {
			t.Fatal("extended original deadline", n, e, calls)
		}
		if _, e := x.db.Exec(`UPDATE owned_federation_public_admission_jobs SET expires_at=expires_at+interval '1 day',max_attempts=max_attempts+1 WHERE id=$1`, job.ID); e == nil {
			t.Fatal("immutable limits rewritten")
		}
	})
	t.Run("configuration", func(t *testing.T) {
		x, target, policy := publicAdmissionFixture(t)
		job := requestPublicAdmission(t, x, target)
		changed := x.config
		changed.MaxAttempts++
		r, e := federationpg.New(x.db, changed, firstContactKeys{federationKeys: federationKeys{locals: x.keys.locals}}, x.guard)
		if e != nil {
			t.Fatal(e)
		}
		r, e = r.WithPublicAdmission([]f.PublicAdmissionPolicy{policy})
		if e != nil {
			t.Fatal(e)
		}
		calls := 0
		fetch := discoveryFetchFunc(func(context.Context, string, string) (f.FetchResponse, error) {
			calls++
			return f.FetchResponse{Status: 503}, nil
		})
		if _, e = r.ProcessPublicAdmissions(x.ctx, fetch, 1); e != nil || calls != 0 || publicAdmissionState(t, x.db, job.ID) != "cancelled" {
			t.Fatal("configuration revived old work", e, calls)
		}
		if _, e = x.r.ProcessPublicAdmissions(x.ctx, fetch, 1); e != nil || calls != 0 {
			t.Fatal("restoring config revived work", e, calls)
		}
	})
}

func TestPGFederationPublicAdmissionCurrentPolicyAfterAuditWait(t *testing.T) {
	x, target, _ := publicAdmissionFixture(t)
	job := requestPublicAdmission(t, x, target)
	ctx, cancel := context.WithTimeout(x.ctx, 10*time.Second)
	defer cancel()
	hold, e := x.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer hold.Rollback()
	var pid int
	if e = hold.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	if _, e = hold.ExecContext(ctx, `LOCK TABLE owned_federation_public_audit IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := x.r.ProcessPublicAdmissions(ctx, discoveryResponse(t, x, target), 1); done <- e }()
	waitAtomicBlocked(t, atomicAuthorityFixture{db: x.db, ctx: ctx}, pid, "owned_federation_public_audit")
	syntheticFederationHistoryTransition(t, ctx, hold, "discovery", x.actor)
	if e = hold.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if publicAdmissionState(t, x.db, job.ID) != "cancelled" {
		t.Fatal("post-audit stale result committed")
	}
	var count int
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_keys WHERE actor_uri=$1`, target.Actor).Scan(&count); e != nil || count != 0 {
		t.Fatal("stale key retained", count, e)
	}
	if e = x.db.QueryRow(`SELECT count(*) FROM owned_federation_public_audit WHERE job_id=$1`, job.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("stale success audit retained", count, e)
	}
}
