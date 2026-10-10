// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"crypto"
	"database/sql"
	_ "embed"
	"errors"
	f "solsynth.local/go-migration/ports/federation"
	"sort"
)

//go:embed public_admission_schema.sql
var PublicAdmissionSchema string

// WithPublicAdmission enables an optional first-contact repository. It copies
// policy, performs no I/O and never initializes policy or generates credentials.
func (r *Repository) WithPublicAdmission(policies []f.PublicAdmissionPolicy) (*Repository, error) {
	if r == nil || len(policies) == 0 || len(policies) > 128 {
		return nil, f.ErrInvalid
	}
	if _, ok := r.keys.(f.FirstContactProvider); !ok {
		return nil, f.ErrUnavailable
	}
	next := *r
	next.publicPolicies = map[string]f.PublicAdmissionPolicy{}
	next.pinnedKeys = r.keys
	for _, p := range policies {
		if p.Validate(r.config) != nil || next.publicPolicies[p.Origin].Origin != "" {
			return nil, f.ErrInvalid
		}
		next.publicPolicies[p.Origin] = p
	}
	next.keys = publicKeys{&next}
	return &next, nil
}
func (r *Repository) InitializePublicAdmission(ctx context.Context) error {
	if r == nil || len(r.publicPolicies) == 0 {
		return f.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	origins := make([]string, 0, len(r.publicPolicies))
	for o := range r.publicPolicies {
		origins = append(origins, o)
	}
	sort.Strings(origins)
	for _, origin := range origins {
		p := r.publicPolicies[origin]
		result, e := tx.ExecContext(ctx, `INSERT INTO owned_federation_public_policies(origin,incarnation,created_at) VALUES($1,$2,$3) ON CONFLICT(origin)DO NOTHING`, p.Origin, p.Incarnation, r.config.Clock())
		if e != nil {
			return e
		}
		if e = r.publicPolicy(ctx, tx, p, true); e != nil {
			return e
		}
		if n, _ := result.RowsAffected(); n > 0 {
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_public_audit(origin,action,occurred_at)VALUES($1,'policy.initialize',$2)`, p.Origin, r.config.Clock()); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}
func (r *Repository) PublicAdmissionReady(ctx context.Context) error {
	if e := r.DiscoveryReady(ctx); e != nil {
		return e
	}
	for _, p := range r.publicPolicies {
		if e := r.publicPolicy(ctx, r.db, p, false); e != nil {
			return e
		}
	}
	var guards bool
	if e := r.db.QueryRowContext(ctx, `SELECT count(*)=4 FROM pg_trigger WHERE NOT tgisinternal AND tgenabled='O' AND ((tgname='owned_federation_public_permanent' AND tgrelid IN('owned_federation_public_policies'::regclass,'owned_federation_public_keys'::regclass)) OR (tgname IN('owned_federation_public_admission_identity_immutable','owned_federation_public_job_no_delete') AND tgrelid='owned_federation_public_admission_jobs'::regclass))`).Scan(&guards); e != nil || !guards {
		return f.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT k.incarnation,j.policy_stamp,a.action FROM owned_federation_public_keys k,owned_federation_public_admission_jobs j,owned_federation_public_audit a LIMIT 0`)
	return e
}
func (r *Repository) publicPolicy(ctx context.Context, q queryer, p f.PublicAdmissionPolicy, lock bool) error {
	var ok bool
	stmt := `SELECT incarnation=$2::uuid AND revoked_at IS NULL FROM owned_federation_public_policies WHERE origin=$1`
	if lock {
		stmt += ` FOR SHARE`
	}
	if e := q.QueryRowContext(ctx, stmt, p.Origin, p.Incarnation).Scan(&ok); e != nil || !ok {
		return f.ErrForbidden
	}
	return nil
}
func (r *Repository) publicTarget(ctx context.Context, q queryer, t f.DiscoveryTarget, lock bool) (f.PublicAdmissionPolicy, string, error) {
	origin, e := f.Origin(t.Actor)
	if e != nil {
		return f.PublicAdmissionPolicy{}, "", e
	}
	p, ok := r.publicPolicies[origin]
	if !ok {
		return p, "", f.ErrForbidden
	}
	binding, e := p.Binding(r.config, t)
	if e != nil {
		return p, "", e
	}
	if e = r.publicPolicy(ctx, q, p, lock); e != nil {
		return p, "", e
	}
	return p, binding, nil
}

// RevokePublicAdmission is an explicit operator operation. Revocation retains
// all unique reservations forever; this first-contact module has no restore or
// rotate operation. Passing an empty actor revokes the configured origin policy.
func (r *Repository) RevokePublicAdmission(ctx context.Context, origin, actor string) error {
	p, ok := r.publicPolicies[origin]
	if !ok {
		return f.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	action := "policy.revoke"
	var result sql.Result
	if actor == "" {
		result, e = tx.ExecContext(ctx, `UPDATE owned_federation_public_policies SET revoked_at=$3 WHERE origin=$1 AND incarnation=$2 AND revoked_at IS NULL`, origin, p.Incarnation, r.config.Clock())
	} else {
		action = "key.revoke"
		result, e = tx.ExecContext(ctx, `UPDATE owned_federation_public_keys SET revoked_at=$4 WHERE actor_uri=$1 AND origin=$2 AND policy_incarnation=$3 AND revoked_at IS NULL`, actor, origin, p.Incarnation, r.config.Clock())
	}
	if e != nil {
		return e
	}
	if n, _ := result.RowsAffected(); n > 0 {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_public_audit(origin,actor_uri,action,occurred_at)VALUES($1,NULLIF($2,''),$3,$4)`, origin, actor, action, r.config.Clock()); e != nil {
			return e
		}
	}
	return tx.Commit()
}

type publicKeys struct{ r *Repository }

func (p publicKeys) LocalKey(ctx context.Context, id string) (crypto.Signer, error) {
	return p.r.pinnedKeys.LocalKey(ctx, id)
}
func (p publicKeys) RemoteActor(ctx context.Context, id string) (f.RemoteKey, error) {
	return p.lookup(ctx, id, false)
}
func (p publicKeys) RemoteKey(ctx context.Context, id string) (f.RemoteKey, error) {
	return p.lookup(ctx, id, true)
}
func (p publicKeys) lookup(ctx context.Context, id string, byKey bool) (f.RemoteKey, error) {
	return p.lookupWithQuery(ctx, p.r.db, id, byKey)
}

// lookupWithQuery keeps every durable reservation and policy read on the
// caller's connection. Transactional callers must not acquire a second pool
// connection while holding their canonical locks. No lookup result is cached.
func (p publicKeys) lookupWithQuery(ctx context.Context, q queryer, id string, byKey bool) (f.RemoteKey, error) {
	// A durable reservation takes precedence, including a revoked tombstone. A
	// configured key cannot silently replace an admitted incarnation after restart.
	ledger := len(p.r.publicPolicies) > 0
	if !ledger {
		// Legacy installations may not have applied the optional migration. An
		// actual query error is not equivalent to the schema being absent. Never
		// cache absence: a later migration must immediately enforce reservations.
		if e := q.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_public_keys') IS NOT NULL`).Scan(&ledger); e != nil {
			return f.RemoteKey{}, e
		}
	}
	if ledger {
		k, e := p.r.publicKey(ctx, q, id, byKey, false)
		if !errors.Is(e, sql.ErrNoRows) {
			return k, e
		}
	}
	var k f.RemoteKey
	var e error
	if byKey {
		k, e = p.r.pinnedKeys.RemoteKey(ctx, id)
	} else {
		k, e = p.r.pinnedKeys.RemoteActor(ctx, id)
	}
	if e != nil {
		return f.RemoteKey{}, e
	}
	// An alternate static key ID or actor must not bypass a durable identity or
	// key-material reservation, including a revoked first-contact tombstone.
	// A failed ledger read is never permission to fall back to a static pin.
	k = k.Snapshot()
	if !p.r.config.ValidRemote(k) || byKey && k.KeyID != id || !byKey && k.Actor != id {
		return f.RemoteKey{}, f.ErrForbidden
	}
	if !ledger {
		return k, nil
	}
	fingerprint, e := f.KeyFingerprint(k.PublicKey)
	if e != nil {
		return f.RemoteKey{}, e
	}
	var reserved bool
	if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_public_keys WHERE actor_uri=$1 OR key_id=$2 OR fingerprint=$3)`, k.Actor, k.KeyID, fingerprint).Scan(&reserved); e != nil {
		return f.RemoteKey{}, e
	}
	if reserved {
		return f.RemoteKey{}, f.ErrForbidden
	}
	return k, nil
}
func (r *Repository) publicKey(ctx context.Context, q queryer, id string, byKey, lock bool) (f.RemoteKey, error) {
	var k f.RemoteKey
	var origin, incarnation, pem, fingerprint string
	var live bool
	predicate := "actor_uri=$1"
	if byKey {
		predicate = "key_id=$1"
	}
	stmt := `SELECT actor_uri,inbox_uri,key_id,public_pem,fingerprint,incarnation::text,origin,policy_incarnation::text,revoked_at IS NULL FROM owned_federation_public_keys WHERE ` + predicate
	if lock {
		stmt += ` FOR SHARE`
	}
	e := q.QueryRowContext(ctx, stmt, id).Scan(&k.Actor, &k.Inbox, &k.KeyID, &pem, &fingerprint, &k.Incarnation, &origin, &incarnation, &live)
	if e != nil {
		return k, e
	}
	p, ok := r.publicPolicies[origin]
	if !ok || !live || p.Incarnation != incarnation {
		return f.RemoteKey{}, f.ErrForbidden
	}
	if e = r.publicPolicy(ctx, q, p, lock); e != nil {
		return f.RemoteKey{}, e
	}
	k.PublicKey, e = f.ParsePublicPEM(pem)
	actual, _ := f.KeyFingerprint(k.PublicKey)
	if e != nil || actual != fingerprint || !r.config.ValidRemote(k) {
		return f.RemoteKey{}, f.ErrForbidden
	}
	return k, nil
}
func (r *Repository) publicKeyFence(ctx context.Context, tx *sql.Tx, k f.RemoteKey) error {
	if k.Incarnation == "" {
		return nil
	}
	ledger, lockErr := lockDeliveryReservations(ctx, tx)
	if lockErr != nil {
		return lockErr
	}
	if !ledger {
		static, staticErr := r.pinnedKeys.RemoteActor(ctx, k.Actor)
		if staticErr != nil || !sameQuoteKey(static.Snapshot(), k) {
			return f.ErrForbidden
		}
		return nil
	}
	current, e := r.publicKey(ctx, tx, k.Actor, false, true)
	if e == nil {
		if !sameQuoteKey(current, k) {
			return f.ErrForbidden
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return f.ErrForbidden
	}
	// Explicit static pins also carry a deployment-controlled incarnation. They
	// are valid only while the configured provider still returns the exact same
	// actor/key/incarnation; lookupWithQuery has already excluded collisions with
	// any durable admission or revocation reservation.
	static, staticErr := r.pinnedKeys.RemoteActor(ctx, k.Actor)
	if staticErr != nil || !sameQuoteKey(static.Snapshot(), k) {
		return f.ErrForbidden
	}
	return nil
}
func (r *Repository) firstContact(ctx context.Context, q queryer, t f.DiscoveryTarget, key f.RemoteKey) (bool, error) {
	provider, ok := r.pinnedKeys.(f.FirstContactProvider)
	if !ok {
		return false, f.ErrUnavailable
	}
	allowed, e := provider.FirstContact(ctx, key)
	if e != nil || !allowed {
		return false, e
	}
	var collision bool
	e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_public_keys WHERE actor_uri=$1 OR lower(resource)=lower($2) OR key_id=$3) OR EXISTS(SELECT 1 FROM owned_federation_peers WHERE actor_uri=$1) OR EXISTS(SELECT 1 FROM owned_federation_publishers WHERE actor_uri=$1 OR key_id=$3) OR EXISTS(SELECT 1 FROM owned_federation_server_identity WHERE actor_uri=$1 OR key_id=$3)`, t.Actor, t.Resource, key.KeyID).Scan(&collision)
	return !collision, e
}
