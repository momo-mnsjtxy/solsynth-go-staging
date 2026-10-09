// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	f "solsynth.local/go-migration/ports/federation"
	"time"
)

//go:embed discovery_schema.sql
var DiscoverySchema string

// NewWithDiscovery adds optional server discovery and configured remote lookups.
// It performs no I/O, migrations, key provisioning, or network work.
func NewWithDiscovery(db *sql.DB, c f.Config, keys f.KeyProvider, guard TransactionGuard, features f.DiscoveryFeatures) (*Repository, error) {
	r, e := New(db, c, keys, guard)
	if e != nil {
		return nil, e
	}
	if len(features.Targets) > 128 || len(features.Targets) > 0 && keys == nil {
		return nil, f.ErrInvalid
	}
	r.discoveryTargets = map[string]f.DiscoveryTarget{}
	for _, target := range features.Targets {
		if target.Validate(c) != nil || r.discoveryTargets[target.Resource].Resource != "" {
			return nil, f.ErrInvalid
		}
		r.discoveryTargets[target.Resource] = target
	}
	if features.Server != nil {
		server := *features.Server
		if _, e = server.Actor(c); e != nil {
			return nil, e
		}
		r.server = &server
	}
	return r, nil
}

// InitializeServerIdentity is explicit offline operator initialization. Existing
// actor URI, key bytes, and reserved handle cannot be rewritten by configuration.
func (r *Repository) InitializeServerIdentity(ctx context.Context) error {
	if r == nil || r.server == nil {
		return f.ErrUnavailable
	}
	actor, e := r.server.Actor(r.config)
	if e != nil {
		return e
	}
	key, _ := f.ParsePublicPEM(actor.PublicKey.PEM)
	fingerprint, _ := f.KeyFingerprint(key)
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_server_identity(singleton,actor_uri,key_id,key_fingerprint,username,created_at) VALUES(true,$1,$2,$3,$4,$5) ON CONFLICT(singleton)DO NOTHING`, actor.ID, actor.PublicKey.ID, fingerprint, actor.Username, r.config.Clock()); e != nil {
		return e
	}
	var matches bool
	e = tx.QueryRowContext(ctx, `SELECT actor_uri=$1 AND key_id=$2 AND key_fingerprint=$3 AND username=$4 FROM owned_federation_server_identity WHERE singleton`, actor.ID, actor.PublicKey.ID, fingerprint, actor.Username).Scan(&matches)
	if e != nil {
		return e
	}
	if !matches {
		return f.ErrForbidden
	}
	return tx.Commit()
}
func (r *Repository) ServerActor(ctx context.Context) (f.ServerActor, error) {
	if r == nil || r.server == nil {
		return f.ServerActor{}, f.ErrNotFound
	}
	actor, e := r.server.Actor(r.config)
	if e != nil {
		return f.ServerActor{}, e
	}
	key, _ := f.ParsePublicPEM(actor.PublicKey.PEM)
	fingerprint, _ := f.KeyFingerprint(key)
	var matches bool
	e = r.db.QueryRowContext(ctx, `SELECT actor_uri=$1 AND key_id=$2 AND key_fingerprint=$3 AND username=$4 FROM owned_federation_server_identity WHERE singleton`, actor.ID, actor.PublicKey.ID, fingerprint, actor.Username).Scan(&matches)
	if e != nil || !matches {
		return f.ServerActor{}, f.ErrUnavailable
	}
	return actor, nil
}
func (r *Repository) DiscoveryReady(ctx context.Context) error {
	var ready bool
	e := r.db.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_server_identity')IS NOT NULL AND to_regclass('owned_federation_discoveries')IS NOT NULL AND to_regclass('owned_federation_discovery_epochs')IS NOT NULL`).Scan(&ready)
	if e != nil || !ready {
		return f.ErrUnavailable
	}
	if _, e = r.db.ExecContext(ctx, `SELECT max_attempts,expires_at,policy_stamp FROM owned_federation_discoveries LIMIT 0`); e != nil {
		return f.ErrUnavailable
	}
	if e = r.db.QueryRowContext(ctx, `SELECT count(*)=11 FROM pg_trigger WHERE NOT tgisinternal AND tgenabled='O' AND tgname IN('owned_discovery_authority_grants','owned_discovery_authority_punishments','owned_discovery_authority_group_members','owned_discovery_publisher_members','owned_discovery_federation_blocks','owned_discovery_group_grants','owned_discovery_account','owned_discovery_publisher','owned_discovery_federation','owned_discovery_session','owned_discovery_session_delete')`).Scan(&ready); e != nil || !ready {
		return f.ErrUnavailable
	}
	if r.server != nil {
		_, e = r.ServerActor(ctx)
	}
	return e
}

type discoveryWork struct {
	f.DiscoveryJob
	publisher, owner, requester, remote, binding, token, session string
	version, epoch                                               int64
	maxAttempts                                                  int
	expires                                                      time.Time
	stamp                                                        []byte
}

func (r *Repository) discoveryTarget(ctx context.Context, resource string) (f.DiscoveryTarget, f.RemoteKey, string, error) {
	target, ok := r.discoveryTargets[resource]
	if !ok {
		return target, f.RemoteKey{}, "", f.ErrForbidden
	}
	key, e := r.remote(ctx, target.Actor)
	if e != nil {
		return target, key, "", e
	}
	binding, e := target.Binding(r.config, key)
	return target, key, binding, e
}
func (r *Repository) RequestDiscovery(ctx context.Context, actor, ref, resource string) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	target, _, binding, e := r.discoveryTarget(ctx, resource)
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
	if _, _, current, err := r.discoveryTarget(ctx, resource); err != nil || current != binding {
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
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_discoveries WHERE publisher_id=$1`, p.id).Scan(&count); e != nil {
		return out, e
	}
	if count >= 128 {
		if _, e := r.readDiscovery(ctx, tx, p.id, resource, binding, p.owner); e != nil {
			return out, f.ErrForbidden
		}
	}
	id, e := newID()
	if e != nil {
		return out, e
	}
	// One retained observation per publisher/handle. Replays return that job;
	// terminal failures do not secretly reset their bounded attempt budget.
	inserted, e := tx.ExecContext(ctx, `INSERT INTO owned_federation_discoveries(id,publisher_id,owner_id,requester_id,requester_version,resource,actor_uri,binding,status,next_attempt,created_at,requester_session,requester_epoch,max_attempts,expires_at,policy_stamp) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$9,$10,$11,$12,$13,$14::jsonb) ON CONFLICT(publisher_id,resource)DO NOTHING`, id, p.id, p.owner, actor, version, resource, target.Actor, binding, r.config.Clock(), work.session, work.epoch, work.maxAttempts, work.expires, work.stamp)
	if e != nil {
		return out, e
	}
	out, e = r.readDiscovery(ctx, tx, p.id, resource, binding, p.owner)
	if e != nil {
		return out, e
	}
	if n, _ := inserted.RowsAffected(); n > 0 {
		if e = r.audit(ctx, tx, actor, p.id, "discovery.request"); e != nil {
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
	if _, _, current, err := r.discoveryTarget(ctx, resource); err != nil || current != binding {
		return out, f.ErrForbidden
	}
	return out, tx.Commit()
}
func (r *Repository) readDiscovery(ctx context.Context, q queryer, pid, resource, binding, owner string) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	var raw []byte
	e := q.QueryRowContext(ctx, `SELECT id::text,resource,status,attempts,result FROM owned_federation_discoveries WHERE publisher_id=$1 AND resource=$2 AND binding=$3 AND owner_id=$4`, pid, resource, binding, owner).Scan(&out.ID, &out.Resource, &out.State, &out.Attempts, &raw)
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
func (r *Repository) Discovery(ctx context.Context, actor, ref, resource string) (f.DiscoveryJob, error) {
	var out f.DiscoveryJob
	_, _, binding, e := r.discoveryTarget(ctx, resource)
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
	if _, _, current, err := r.discoveryTarget(ctx, resource); err != nil || current != binding {
		return out, f.ErrForbidden
	}
	target := r.discoveryTargets[resource]
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
	return r.readDiscovery(ctx, tx, p.id, resource, binding, p.owner)
}

// storedDiscoveryGuard only reconciles recorded admissions; it cannot issue a
// principal or replace the original proof required for a new request.
type storedDiscoveryGuard interface {
	StoredSessionEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64, ...string) (bool, error)
}

func (r *Repository) discoveryAllowed(ctx context.Context, tx *sql.Tx, w discoveryWork) (bool, error) {
	guard, ok := r.guard.(storedDiscoveryGuard)
	if !ok {
		return false, f.ErrUnavailable
	}
	allowed, e := guard.StoredSessionEligible(ctx, r.db, tx, w.requester, w.session, w.version, w.epoch, f.Permission)
	if e != nil || !allowed {
		return false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_federation_publishers f ON f.publisher_id=p.id JOIN owned_authority_accounts owner ON owner.id=p.account_id JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$3::uuid WHERE p.id=$1::uuid AND p.account_id=$2::uuid AND p.type=0 AND p.realm_id IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND f.enabled AND f.actor_uri=$4::text||'/activitypub/actors/'||f.identity_handle AND NOT owner.disabled AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=100 AND NOT EXISTS(SELECT 1 FROM owned_federation_blocks b WHERE b.publisher_id=p.id AND b.actor_uri=$5::text) AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=owner.id AND x.kind IN('disable_account','permission_modification') AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp())))`, w.publisher, w.owner, w.requester, r.config.Origin, w.remote).Scan(&allowed)
	if e != nil || !allowed {
		return false, e
	}
	if len(w.stamp) > 0 {
		stamp, err := discoveryStamp(ctx, tx, w.requester, w.publisher, w.session)
		if err != nil || !bytes.Equal(stamp, w.stamp) {
			return false, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT $1::timestamptz>clock_timestamp()`, w.expires).Scan(&allowed); err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}
func (r *Repository) discoveryPreflight(ctx context.Context, w discoveryWork) (bool, error) {
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var owned bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_discoveries WHERE id=$1::uuid AND status='inflight' AND lease_token=$2::uuid AND lease_until>$3::timestamptz AND expires_at>clock_timestamp())`, w.ID, w.token, r.config.Clock()).Scan(&owned); e != nil || !owned {
		return false, e
	}
	return r.discoveryAllowed(ctx, tx, w)
}

type admittedDiscoveryFetcher struct {
	repository *Repository
	work       discoveryWork
	fetch      f.Fetcher
}

func (a admittedDiscoveryFetcher) Fetch(ctx context.Context, uri, accept string) (f.FetchResponse, error) {
	_, _, binding, e := a.repository.discoveryTarget(ctx, a.work.Resource)
	if e != nil || binding != a.work.binding {
		return f.FetchResponse{}, f.ErrForbidden
	}
	allowed, e := a.repository.discoveryPreflight(ctx, a.work)
	if e != nil {
		return f.FetchResponse{}, e
	}
	if !allowed {
		return f.FetchResponse{}, f.ErrForbidden
	}
	// The read transaction has closed before any external work starts.
	return a.fetch.Fetch(ctx, uri, accept)
}

// The optional discovery migration owns this narrow history fence because the
// blog/factor migrations are optional and have different domain dependencies.
// Current scopes and policy are always evaluated by canonical authority above.
func discoveryStamp(ctx context.Context, q queryer, actor, publisher, session string) ([]byte, error) {
	var stamp []byte
	e := q.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,authorization_epoch,ARRAY[id] path FROM owned_authority_sessions WHERE id=NULLIF($3::text,'')::uuid AND account_id=$1::uuid UNION ALL SELECT s.id,s.parent_id,s.authorization_epoch,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$1::uuid WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64) SELECT jsonb_build_object('actor',COALESCE((SELECT revision FROM owned_federation_discovery_epochs WHERE kind='account' AND entity_id=$1::uuid),0),'owner',COALESCE((SELECT revision FROM owned_federation_discovery_epochs WHERE kind='account' AND entity_id=p.account_id),0),'publisher',COALESCE((SELECT revision FROM owned_federation_discovery_epochs WHERE kind='publisher' AND entity_id=p.id),0),'owner_id',p.account_id,'sessions',(SELECT jsonb_agg(jsonb_build_object('id',id,'epoch',authorization_epoch) ORDER BY id) FROM chain)) FROM owned_publishers p WHERE p.id=$2::uuid`, actor, publisher, session).Scan(&stamp)
	return stamp, e
}

// The earliest original ancestry or policy boundary is permanent. Refresh and
// a transient timed deny cannot extend or resurrect this one-shot observation.
func discoveryDeadline(ctx context.Context, tx *sql.Tx, actor, owner, session string) (time.Time, error) {
	var expires time.Time
	e := tx.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,expires_at,ARRAY[id] path FROM owned_authority_sessions WHERE id=$3::uuid AND account_id=$1::uuid UNION ALL SELECT s.id,s.parent_id,s.expires_at,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$1::uuid WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64), boundaries AS(SELECT expires_at boundary FROM chain UNION ALL SELECT clock_timestamp()+interval '24 hours' UNION ALL SELECT unnest(ARRAY[starts_at,expires_at]) FROM owned_authority_grants WHERE account_id=$1::uuid UNION ALL SELECT unnest(ARRAY[g.starts_at,g.expires_at]) FROM owned_authority_group_grants g JOIN owned_authority_group_members m ON m.group_id=g.group_id WHERE m.account_id=$1::uuid UNION ALL SELECT unnest(ARRAY[starts_at,expires_at]) FROM owned_authority_punishments WHERE account_id IN($1::uuid,$2::uuid)) SELECT LEAST((SELECT min(expires_at) FROM chain),min(boundary)) FROM boundaries WHERE boundary>clock_timestamp()`, actor, owner, session).Scan(&expires)
	return expires, e
}
func (r *Repository) claimDiscovery(ctx context.Context) (discoveryWork, error) {
	var w discoveryWork
	token, e := newID()
	if e != nil {
		return w, e
	}
	now := r.config.Clock()
	if _, e = r.db.ExecContext(ctx, `UPDATE owned_federation_discoveries SET status='dead',finished_at=$1,lease_token=NULL,lease_until=NULL WHERE status IN('pending','inflight') AND attempts>=max_attempts AND(lease_until IS NULL OR lease_until<=$1)`, now); e != nil {
		return w, e
	}
	e = r.db.QueryRowContext(ctx, `WITH candidate AS(SELECT id FROM owned_federation_discoveries WHERE attempts<max_attempts AND((status='pending' AND next_attempt<=$1)OR(status='inflight' AND lease_until<=$1))ORDER BY next_attempt,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE owned_federation_discoveries d SET status='inflight',attempts=attempts+1,lease_token=$2,lease_until=LEAST($3,expires_at) FROM candidate c WHERE d.id=c.id RETURNING d.id::text,d.publisher_id::text,d.owner_id::text,d.requester_id::text,d.requester_version,d.resource,d.actor_uri,d.binding,d.attempts,d.requester_session::text,d.requester_epoch,d.max_attempts,d.expires_at,d.policy_stamp`, now, token, now.Add(2*r.config.DeliveryTimeout)).Scan(&w.ID, &w.publisher, &w.owner, &w.requester, &w.version, &w.Resource, &w.remote, &w.binding, &w.Attempts, &w.session, &w.epoch, &w.maxAttempts, &w.expires, &w.stamp)
	w.token = token
	return w, e
}

// ProcessDiscoveries claims durably, releases SQL locks, performs bounded GETs,
// and then validates immutable pins and current local ownership before commit.
func (r *Repository) ProcessDiscoveries(ctx context.Context, fetch f.Fetcher, limit int) (int, error) {
	if r == nil || fetch == nil || len(r.discoveryTargets) == 0 {
		return 0, f.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		w, e := r.claimDiscovery(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			return n, nil
		}
		if e != nil {
			return n, e
		}
		target, key, binding, e := r.discoveryTarget(ctx, w.Resource)
		status := "cancelled"
		var result *f.DiscoveredActor
		if e == nil && binding == w.binding && target.Actor == w.remote {
			allowed, check := r.discoveryPreflight(ctx, w)
			if check != nil {
				return n, check
			}
			if allowed {
				fetchContext, cancel := context.WithDeadline(ctx, w.expires)
				observed, retry, err := f.Discover(fetchContext, r.config, target, key, admittedDiscoveryFetcher{r, w, fetch})
				cancel()
				status = "dead"
				if err == nil {
					status = "succeeded"
					result = &observed
				} else if retry && w.Attempts < w.maxAttempts {
					status = "pending"
				}
			}
		}
		if ctx.Err() != nil {
			return n, ctx.Err()
		} // uncertain in-flight work is recoverable
		if e = r.completeDiscovery(ctx, w, status, result); e != nil {
			return n, e
		}
	}
	return limit, nil
}
func (r *Repository) completeDiscovery(ctx context.Context, w discoveryWork, status string, result *f.DiscoveredActor) error {
	// KeyProvider is a preprovisioned, non-network dependency by contract.
	_, _, binding, bindingError := r.discoveryTarget(ctx, w.Resource)
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
	e = tx.QueryRowContext(ctx, `SELECT lease_token::text,lease_until FROM owned_federation_discoveries WHERE id=$1 AND status='inflight' FOR UPDATE`, w.ID).Scan(&lease, &until)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if lease != w.token || status != "cancelled" && !until.After(r.config.Clock()) {
		return nil
	}
	_, _, current, e := r.discoveryTarget(ctx, w.Resource)
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
	_, e = tx.ExecContext(ctx, `UPDATE owned_federation_discoveries SET status=$3,result=$4,next_attempt=$5,finished_at=$6,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, w.ID, w.token, status, payload, r.config.Clock().Add(retryDelay(w.Attempts)), finished)
	if e != nil {
		return e
	}
	return tx.Commit()
}
