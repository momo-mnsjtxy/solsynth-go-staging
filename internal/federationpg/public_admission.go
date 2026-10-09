// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	f "solsynth.local/go-migration/ports/federation"
	"time"
)

func (r *Repository) RequestPublicAdmission(ctx context.Context, actor, ref string, target f.DiscoveryTarget) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	resource := target.Resource
	_, binding, e := r.publicTarget(ctx, r.db, target, false)
	if e != nil {
		return out, e
	}
	initial, e := r.publisher(ctx, r.db, ref, true)
	if e != nil {
		return out, e
	}
	initialStamp, e := discoveryStamp(ctx, r.db, actor, initial.id, "")
	if e != nil {
		return out, e
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	currentStamp, stampError := discoveryStamp(ctx, tx, actor, p.id, "")
	if stampError != nil {
		return out, stampError
	}
	if !p.enabled || p.owner != initial.owner || !r.identityMatches(p) || !bytes.Equal(initialStamp, currentStamp) {
		return out, f.ErrForbidden
	}
	if _, current, err := r.publicTarget(ctx, tx, target, true); err != nil || current != binding {
		return out, f.ErrForbidden
	}
	var version int64
	if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1`, actor).Scan(&version); e != nil {
		return out, e
	}
	principal, e := r.guard.Authorize(ctx, r.db, tx, actor, f.Permission)
	if e != nil {
		return out, e
	}
	work := discoveryWork{publisher: p.id, owner: p.owner, requester: actor, remote: target.Actor, version: version, session: principal.Session.ID, epoch: principal.Session.AuthorizationEpoch, maxAttempts: r.config.MaxAttempts}
	if work.stamp, e = discoveryStamp(ctx, tx, actor, p.id, work.session); e != nil {
		return out, e
	}
	if work.expires, e = discoveryDeadline(ctx, tx, actor, p.owner, work.session); e != nil {
		return out, e
	}
	if allowed, e := r.discoveryAllowed(ctx, tx, work); e != nil || !allowed {
		if e != nil {
			return out, e
		}
		return out, f.ErrForbidden
	}
	if prior, err := r.readPublicAdmission(ctx, tx, p.id, resource, binding, p.owner); err == nil {
		return prior, nil
	} else if !errors.Is(err, f.ErrNotFound) {
		return out, err
	}
	if allowed, err := r.firstContact(ctx, tx, target, f.RemoteKey{Actor: target.Actor, KeyID: target.Actor + "#main-key"}); err != nil || !allowed {
		if err != nil {
			return out, err
		}
		return out, f.ErrForbidden
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_public_admission_jobs WHERE publisher_id=$1`, p.id).Scan(&count); e != nil {
		return out, e
	}
	if count >= 128 {
		if _, e := r.readPublicAdmission(ctx, tx, p.id, resource, binding, p.owner); e != nil {
			return out, f.ErrForbidden
		}
	}
	id, e := newID()
	if e != nil {
		return out, e
	}
	// One retained observation per publisher/handle. Replays return that job;
	// terminal failures do not secretly reset their bounded attempt budget.
	inserted, e := tx.ExecContext(ctx, `INSERT INTO owned_federation_public_admission_jobs(id,publisher_id,owner_id,requester_id,requester_version,resource,actor_uri,binding,status,next_attempt,created_at,requester_session,requester_epoch,max_attempts,expires_at,policy_stamp) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$9,$10,$11,$12,$13,$14::jsonb) ON CONFLICT(publisher_id,resource)DO NOTHING`, id, p.id, p.owner, actor, version, resource, target.Actor, binding, r.config.Clock(), work.session, work.epoch, work.maxAttempts, work.expires, work.stamp)
	if e != nil {
		return out, e
	}
	out, e = r.readPublicAdmission(ctx, tx, p.id, resource, binding, p.owner)
	if e != nil {
		return out, e
	}
	if n, _ := inserted.RowsAffected(); n > 0 {
		if e = r.audit(ctx, tx, actor, p.id, "public_admission.request"); e != nil {
			return out, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.Permission); e != nil {
		return out, e
	}
	if allowed, err := r.discoveryAllowed(ctx, tx, work); err != nil || !allowed {
		if err != nil {
			return out, err
		}
		return out, f.ErrForbidden
	}
	if _, current, err := r.publicTarget(ctx, tx, target, true); err != nil || current != binding {
		return out, f.ErrForbidden
	}
	return out, tx.Commit()
}
func (r *Repository) readPublicAdmission(ctx context.Context, q queryer, pid, resource, binding, owner string) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	var raw []byte
	e := q.QueryRowContext(ctx, `SELECT id::text,resource,status,attempts,result FROM owned_federation_public_admission_jobs WHERE publisher_id=$1 AND resource=$2 AND binding=$3 AND owner_id=$4`, pid, resource, binding, owner).Scan(&out.ID, &out.Resource, &out.State, &out.Attempts, &raw)
	if e != nil {
		return out, mapped(e)
	}
	if raw != nil {
		out.Result = &f.DiscoveredActor{}
		if json.Unmarshal(raw, out.Result) != nil {
			return f.DiscoveryJob{}, f.ErrUnavailable
		}
	}
	return out, nil
}
func (r *Repository) PublicAdmission(ctx context.Context, actor, ref string, target f.DiscoveryTarget) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	resource := target.Resource
	_, binding, e := r.publicTarget(ctx, r.db, target, false)
	if e != nil {
		return out, e
	}
	initial, e := r.publisher(ctx, r.db, ref, true)
	if e != nil {
		return out, e
	}
	initialStamp, e := discoveryStamp(ctx, r.db, actor, initial.id, "")
	if e != nil {
		return out, e
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	currentStamp, stampError := discoveryStamp(ctx, tx, actor, p.id, "")
	if stampError != nil {
		return out, stampError
	}
	if !p.enabled || p.owner != initial.owner || !r.identityMatches(p) || !bytes.Equal(initialStamp, currentStamp) {
		return out, f.ErrForbidden
	}
	if _, current, err := r.publicTarget(ctx, tx, target, true); err != nil || current != binding {
		return out, f.ErrForbidden
	}

	if blocked, e := r.blocked(ctx, tx, p.id, target.Actor); e != nil || blocked {
		if e != nil {
			return out, e
		}
		return out, f.ErrForbidden
	}
	principal, e := r.guard.Authorize(ctx, r.db, tx, actor, f.Permission)
	if e != nil {
		return out, e
	}
	if allowed, e := r.discoveryAllowed(ctx, tx, discoveryWork{publisher: p.id, owner: p.owner, requester: actor, remote: target.Actor, version: principal.Account.Version, session: principal.Session.ID, epoch: principal.Session.AuthorizationEpoch}); e != nil || !allowed {
		if e != nil {
			return out, e
		}
		return out, f.ErrForbidden
	}
	return r.readPublicAdmission(ctx, tx, p.id, resource, binding, p.owner)
}

func (r *Repository) publicAdmissionPreflight(ctx context.Context, w discoveryWork) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var owned bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_public_admission_jobs WHERE id=$1::uuid AND status='inflight' AND lease_token=$2::uuid AND lease_until>$3::timestamptz AND expires_at>clock_timestamp())`, w.ID, w.token, r.config.Clock()).Scan(&owned); e != nil || !owned {
		return false, e
	}
	return r.discoveryAllowed(ctx, tx, w)
}

func (r *Repository) claimPublicAdmission(ctx context.Context) (discoveryWork, error) {
	var w discoveryWork
	token, e := newID()
	if e != nil {
		return w, e
	}
	now := r.config.Clock()
	if _, e = r.db.ExecContext(ctx, `UPDATE owned_federation_public_admission_jobs SET status='dead',finished_at=$1,lease_token=NULL,lease_until=NULL WHERE status IN('pending','inflight') AND attempts>=max_attempts AND(lease_until IS NULL OR lease_until<=$1)`, now); e != nil {
		return w, e
	}
	e = r.db.QueryRowContext(ctx, `WITH candidate AS(SELECT id FROM owned_federation_public_admission_jobs WHERE attempts<max_attempts AND((status='pending' AND next_attempt<=$1)OR(status='inflight' AND lease_until<=$1))ORDER BY next_attempt,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE owned_federation_public_admission_jobs d SET status='inflight',attempts=attempts+1,lease_token=$2,lease_until=LEAST($3,expires_at) FROM candidate c WHERE d.id=c.id RETURNING d.id::text,d.publisher_id::text,d.owner_id::text,d.requester_id::text,d.requester_version,d.resource,d.actor_uri,d.binding,d.attempts,d.requester_session::text,d.requester_epoch,d.max_attempts,d.expires_at,d.policy_stamp`, now, token, now.Add(2*r.config.DeliveryTimeout)).Scan(&w.ID, &w.publisher, &w.owner, &w.requester, &w.version, &w.Resource, &w.remote, &w.binding, &w.Attempts, &w.session, &w.epoch, &w.maxAttempts, &w.expires, &w.stamp)
	w.token = token
	return w, e
}

type publicAdmissionFetcher struct {
	repository *Repository
	work       discoveryWork
	fetch      f.Fetcher
}

func (a publicAdmissionFetcher) Fetch(ctx context.Context, uri, accept string) (f.FetchResponse, error) {
	t := f.DiscoveryTarget{Resource: a.work.Resource, Actor: a.work.remote}
	_, binding, e := a.repository.publicTarget(ctx, a.repository.db, t, false)
	if e != nil || binding != a.work.binding {
		return f.FetchResponse{}, f.ErrForbidden
	}
	allowed, e := a.repository.publicAdmissionPreflight(ctx, a.work)
	if e != nil {
		return f.FetchResponse{}, e
	}
	if !allowed {
		return f.FetchResponse{}, f.ErrForbidden
	}
	return a.fetch.Fetch(ctx, uri, accept)
}
func (r *Repository) ProcessPublicAdmissions(ctx context.Context, fetch f.Fetcher, limit int) (int, error) {
	if r == nil || fetch == nil || len(r.publicPolicies) == 0 {
		return 0, f.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		w, e := r.claimPublicAdmission(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			return n, nil
		}
		if e != nil {
			return n, e
		}
		target := f.DiscoveryTarget{Resource: w.Resource, Actor: w.remote}
		policy, binding, e := r.publicTarget(ctx, r.db, target, false)
		status := "cancelled"
		var result *f.DiscoveredActor
		var key f.RemoteKey
		if e == nil && binding == w.binding {
			allowed, err := r.publicAdmissionPreflight(ctx, w)
			if err != nil {
				return n, err
			}
			if allowed {
				fetchContext, cancel := context.WithDeadline(ctx, w.expires)
				found, candidate, retry, err := f.DiscoverFirstContact(fetchContext, r.config, policy, target, publicAdmissionFetcher{r, w, fetch})
				cancel()
				status = "dead"
				if err == nil {
					status = "succeeded"
					result = &found
					key = candidate
				} else if retry && w.Attempts < w.maxAttempts {
					status = "pending"
				}
			}
		}
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if e = r.completePublicAdmission(ctx, w, status, result, key); e != nil {
			return n, e
		}
	}
	return limit, nil
}
func (r *Repository) completePublicAdmission(ctx context.Context, w discoveryWork, status string, result *f.DiscoveredActor, candidate f.RemoteKey) error {
	// KeyProvider is a preprovisioned, non-network dependency by contract.
	target := f.DiscoveryTarget{Resource: w.Resource, Actor: w.remote}
	_, binding, bindingError := r.publicTarget(ctx, r.db, target, false)
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, w.owner, w.requester)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var locked string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`, w.publisher).Scan(&locked)
	if e != nil {
		return e
	}
	// All policy reads occur in a fresh statement after canonical row lock waits.
	allowed, e := r.discoveryAllowed(ctx, tx, w)
	if e != nil {
		return e
	}
	if !allowed || bindingError != nil || binding != w.binding {
		status = "cancelled"
		result = nil
	}
	var lease string
	var until time.Time
	e = tx.QueryRowContext(ctx, `SELECT lease_token::text,lease_until FROM owned_federation_public_admission_jobs WHERE id=$1 AND status='inflight' FOR UPDATE`, w.ID).Scan(&lease, &until)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if lease != w.token || status != "cancelled" && !until.After(r.config.Clock()) {
		return nil
	}
	policy, current, e := r.publicTarget(ctx, tx, target, true)
	if e != nil || current != w.binding {
		status = "cancelled"
		result = nil
	}
	if allowed, e = r.discoveryAllowed(ctx, tx, w); e != nil {
		return e
	} else if !allowed {
		status = "cancelled"
		result = nil
	}
	if status == "succeeded" {
		// Serialize identity admission under a shared per-origin transaction lock;
		// unique constraints also retain cross-origin key/fingerprint reservations.
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('owned_public_admission:'||$1,0))`, policy.Origin); e != nil {
			return e
		}
		allowed, err := r.discoveryAllowed(ctx, tx, w)
		if err != nil {
			return err
		}
		fresh, err := r.firstContact(ctx, tx, target, candidate)
		if err != nil {
			return err
		}
		_, current, err := r.publicTarget(ctx, tx, target, true)
		var leaseLive bool
		if e = tx.QueryRowContext(ctx, `SELECT lease_until>$3 AND expires_at>clock_timestamp() FROM owned_federation_public_admission_jobs WHERE id=$1 AND lease_token=$2`, w.ID, w.token, r.config.Clock()).Scan(&leaseLive); e != nil {
			return e
		}
		if !allowed || !leaseLive || err != nil || current != w.binding {
			status = "cancelled"
			result = nil
		} else if !fresh {
			status = "dead"
			result = nil
		} else {
			if _, e = tx.ExecContext(ctx, `SAVEPOINT public_admission_grant`); e != nil {
				return e
			}
			candidate.Incarnation = w.ID
			pem, err := f.PublicPEM(candidate.PublicKey)
			if err != nil {
				return err
			}
			inserted, err := tx.ExecContext(ctx, `INSERT INTO owned_federation_public_keys(actor_uri,resource,inbox_uri,key_id,fingerprint,public_pem,incarnation,origin,policy_incarnation,admitted_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)ON CONFLICT DO NOTHING`, candidate.Actor, w.Resource, candidate.Inbox, candidate.KeyID, result.KeyFingerprint, pem, w.ID, policy.Origin, policy.Incarnation, r.config.Clock())
			if err != nil {
				return err
			}
			if n, _ := inserted.RowsAffected(); n == 0 {
				status = "dead"
				result = nil
			} else {
				if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_public_audit(origin,actor_uri,job_id,action,occurred_at)VALUES($1,$2,$3,'key.admit',$4)`, policy.Origin, candidate.Actor, w.ID, r.config.Clock()); e != nil {
					return e
				}
			}
		}
	}
	if status == "succeeded" {
		// Unique-key and audit inserts can wait. No pre-wait authority/deadline
		// observation is allowed to authorize a grant after those waits complete.
		allowed, err := r.discoveryAllowed(ctx, tx, w)
		if err != nil {
			return err
		}
		var live bool
		if e = tx.QueryRowContext(ctx, `SELECT lease_until>$3 AND expires_at>clock_timestamp() FROM owned_federation_public_admission_jobs WHERE id=$1 AND lease_token=$2`, w.ID, w.token, r.config.Clock()).Scan(&live); e != nil {
			return e
		}
		if !allowed || !live {
			if _, e = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT public_admission_grant`); e != nil {
				return e
			}
			status = "cancelled"
			result = nil
		}
	}

	var payload any
	var finished any
	if result != nil {
		b, e := json.Marshal(result)
		if e != nil {
			return e
		}
		payload = b
	}
	if status != "pending" {
		finished = r.config.Clock()
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_federation_public_admission_jobs SET status=$3,result=$4,next_attempt=$5,finished_at=$6,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, w.ID, w.token, status, payload, r.config.Clock().Add(retryDelay(w.Attempts)), finished)
	if e != nil {
		return e
	}
	return tx.Commit()
}
