// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
	"strings"
	"time"
)

//go:embed quote_schema.sql
var QuoteSchema string
var _ f.QuoteRepository = (*Repository)(nil)

type quoteGrant struct {
	id, publisher, actor, request, object, target, key string
	fingerprint                                        []byte
	updated                                            time.Time
	audit                                              int64
	revoked                                            sql.NullTime
}

const quoteColumns = `id::text,publisher_id::text,actor_uri,request_id,object_uri,target_post_id::text,key_id,key_sha256,target_updated_at,target_audit_id,revoked_at`

func scanQuote(row interface{ Scan(...any) error }) (g quoteGrant, e error) {
	e = row.Scan(&g.id, &g.publisher, &g.actor, &g.request, &g.object, &g.target, &g.key, &g.fingerprint, &g.updated, &g.audit, &g.revoked)
	return g, mapped(e)
}
func quoteKey(k f.RemoteKey) []byte {
	if k.PublicKey == nil {
		return nil
	}
	b, e := x509.MarshalPKIXPublicKey(k.PublicKey)
	if e != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	return sum[:]
}
func sameQuoteKey(a, b f.RemoteKey) bool {
	return a.Actor == b.Actor && a.KeyID == b.KeyID && len(quoteKey(a)) == 32 && bytes.Equal(quoteKey(a), quoteKey(b))
}
func (r *Repository) quoteID(id string) string {
	return r.config.Origin + "/quote-authorizations/" + id
}
func (r *Repository) localObject(raw string) (string, error) {
	prefix := r.config.Origin + "/activitypub/objects/"
	if !strings.HasPrefix(raw, prefix) {
		return "", f.ErrForbidden
	}
	id := strings.TrimPrefix(raw, prefix)
	if !authority.ValidID(id) || raw != r.objectID(id) {
		return "", f.ErrInvalid
	}
	return id, nil
}
func (r *Repository) quoteTarget(ctx context.Context, tx *sql.Tx, p publisher, id string, lock bool) (time.Time, int64, error) {
	var updated time.Time
	var audit int64
	q := `SELECT updated_at,COALESCE((SELECT max(a.id) FROM owned_text_post_audit a WHERE a.post_id=p.id),0) FROM owned_text_posts p WHERE p.id=$1 AND p.publisher_id=$2 AND p.realm_id IS NULL AND p.visibility=0 AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND p.drafted_at IS NULL AND p.published_at<=$3 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=$4 AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp()))`
	if lock {
		// Acquire the row in its own statement. The policy/audit query below
		// then has a fresh READ COMMITTED snapshot after a waited-for update.
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 AND publisher_id=$2 FOR UPDATE`, id, p.id).Scan(&held); e != nil {
			return updated, audit, mapped(e)
		}
	}
	e := tx.QueryRowContext(ctx, q, id, p.id, r.config.Clock(), p.owner).Scan(&updated, &audit)
	return updated, audit, mapped(e)
}
func (r *Repository) quoteRemoteObject(ctx context.Context, tx *sql.Tx, p publisher, actor, object string) error {
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT o.object FROM owned_federation_objects o WHERE o.publisher_id=$1 AND o.object_id=$2 AND o.actor_uri=$3 AND NOT o.deleted AND EXISTS(SELECT 1 FROM owned_federation_relationships x WHERE x.publisher_id=o.publisher_id AND x.actor_uri=o.actor_uri AND x.direction='out' AND x.status='accepted')`, p.id, object, actor).Scan(&raw)
	if e != nil {
		return mapped(e)
	}
	var n f.Note
	if json.Unmarshal(raw, &n) != nil || n.ID != object || n.AttributedTo != actor || n.Type != "Note" || !f.Has(n.To, f.Public) && !f.Has(n.CC, f.Public) {
		return f.ErrForbidden
	}
	return nil
}

// A successful lookup that returns a different key is an observed revocation
// of this immutable grant. Persist it so restoring an older provider snapshot
// cannot revive the grant. A lookup outage alone is not a new key observation.
var errQuoteKeyChanged = errors.New("quote signing key changed")

func (r *Repository) invalidateQuoteKey(ctx context.Context, tx *sql.Tx, g quoteGrant) error {
	if _, e := tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1`, g.id); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `UPDATE owned_federation_objects SET object=object-'quote'-'quoteAuthorization' WHERE publisher_id=$1 AND object_id=$2`, g.publisher, g.object); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL,finished_at=clock_timestamp() WHERE quote_id=$1 AND activity_type='Accept' AND status IN('pending','inflight')`, g.id); e != nil {
		return e
	}
	return errors.Join(f.ErrNotFound, errQuoteKeyChanged)
}
func quotePolicyFailure(tx *sql.Tx, e error) error {
	if errors.Is(e, errQuoteKeyChanged) {
		if x := tx.Commit(); x != nil {
			return x
		}
		return f.ErrNotFound
	}
	return e
}
func (r *Repository) quoteKeyCurrent(ctx context.Context, tx *sql.Tx, g quoteGrant) error {
	k, e := r.remote(ctx, g.actor)
	if e != nil {
		return f.ErrNotFound
	}
	if k.KeyID != g.key || !bytes.Equal(quoteKey(k), g.fingerprint) {
		return r.invalidateQuoteKey(ctx, tx, g)
	}
	return nil
}

func (r *Repository) quoteCurrent(ctx context.Context, tx *sql.Tx, p publisher, g quoteGrant, lock bool) error {
	if g.revoked.Valid || g.publisher != p.id {
		return f.ErrNotFound
	}
	if e := r.quoteKeyCurrent(ctx, tx, g); e != nil {
		return e
	}
	if blocked, e := r.blocked(ctx, tx, p.id, g.actor); e != nil {
		return e
	} else if blocked {
		return f.ErrNotFound
	}
	updated, audit, e := r.quoteTarget(ctx, tx, p, g.target, lock)
	if e != nil {
		return e
	}
	if !updated.Equal(g.updated) || audit != g.audit {
		return f.ErrNotFound
	}
	if e := r.quoteRemoteObject(ctx, tx, p, g.actor, g.object); e != nil {
		return e
	}
	// Key providers may wait independently of the database; check once more
	// after all row waits and policy reads before permitting this grant.
	if e := r.quoteKeyCurrent(ctx, tx, g); e != nil {
		return e
	}
	// Scheduled owner punishments can become effective during a key lookup,
	// even while row locks exclude ordinary policy mutations.
	updated, audit, e = r.quoteTarget(ctx, tx, p, g.target, false)
	if e != nil {
		return e
	}
	if !updated.Equal(g.updated) || audit != g.audit {
		return f.ErrNotFound
	}
	return nil
}
func (r *Repository) quotePublicationCurrent(ctx context.Context, tx *sql.Tx, p publisher, a f.Activity) error {
	var target string
	if a.Type == "QuoteRequest" {
		target, _ = f.ObjectID(a.Object)
	} else if a.Type == "Create" || a.Type == "Update" {
		var n f.Note
		if json.Unmarshal(a.Object, &n) == nil && n.QuoteAuthorization != "" {
			target = n.Quote
		}
	}
	if target == "" {
		return nil
	}
	id, e := r.localObject(target)
	if e != nil {
		return e
	}
	_, _, e = r.quoteTarget(ctx, tx, p, id, false)
	return e
}
func (r *Repository) receiveQuoteRequest(ctx context.Context, tx *sql.Tx, p publisher, k f.RemoteKey, a f.Activity) error {
	if !f.Addressed(a, r.actorID(p)) || a.Result != "" {
		return f.ErrForbidden
	}
	target, e := f.ObjectID(a.Object)
	if e != nil {
		return e
	}
	id, e := r.localObject(target)
	if e != nil {
		return e
	}
	object, e := f.ObjectID(a.Instrument)
	if e != nil || !r.config.Allows(object) {
		return f.ErrInvalid
	}
	actorOrigin, _ := f.Origin(k.Actor)
	objectOrigin, _ := f.Origin(object)
	if actorOrigin != objectOrigin {
		return f.ErrForbidden
	}
	if len(a.Instrument) > 0 && a.Instrument[0] == '{' {
		var n f.Note
		if json.Unmarshal(a.Instrument, &n) != nil || n.Type != "Note" || n.AttributedTo != "" && n.AttributedTo != k.Actor {
			return f.ErrForbidden
		}
	}
	updated, audit, e := r.quoteTarget(ctx, tx, p, id, true)
	if e != nil {
		return e
	}
	if e = r.quoteRemoteObject(ctx, tx, p, k.Actor, object); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_quote_authorizations WHERE publisher_id=$1 AND object_uri=$2)`, p.id, object).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return f.ErrReplay
	}
	grant, e := newID()
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_quote_authorizations(id,publisher_id,actor_uri,request_id,object_uri,target_post_id,target_updated_at,target_audit_id,key_id,key_sha256,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, grant, p.id, k.Actor, a.ID, object, id, updated, audit, k.KeyID, quoteKey(k), r.config.Clock()); e != nil {
		return e
	}
	accept, e := r.activity(p, "Accept", a.ID, k.Actor)
	if e != nil {
		return e
	}
	accept.Result = r.quoteID(grant)
	if e = r.enqueue(ctx, tx, p, k, accept, "", nil); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET quote_id=$3 WHERE activity_id=$1 AND actor_uri=$2`, accept.ID, k.Actor, grant)
	return e
}
func (r *Repository) QuoteAuthorization(ctx context.Context, id string) (f.QuoteAuthorization, error) {
	var out f.QuoteAuthorization
	if !authority.ValidID(id) {
		return out, f.ErrNotFound
	}
	initial, e := scanQuote(r.db.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM owned_federation_quote_authorizations WHERE id=$1`, id))
	if e != nil {
		return out, e
	}
	// Resolve potentially slow provider state before taking the current database
	// snapshot; this provisional result never authorizes the returned resource.
	observed, e := r.remote(ctx, initial.actor)
	if e != nil {
		return out, f.ErrNotFound
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var owner, held string
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1`, initial.publisher).Scan(&owner); e != nil {
		return out, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, owner).Scan(&held); e != nil {
		return out, mapped(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`, initial.publisher).Scan(&held); e != nil {
		return out, mapped(e)
	}
	g, e := scanQuote(tx.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM owned_federation_quote_authorizations WHERE id=$1`, id))
	if e != nil {
		return out, e
	}
	if g.revoked.Valid {
		return out, f.ErrNotFound
	}
	if observed.KeyID != g.key || !bytes.Equal(quoteKey(observed), g.fingerprint) {
		return out, quotePolicyFailure(tx, r.invalidateQuoteKey(ctx, tx, g))
	}
	p, e := r.publisher(ctx, tx, g.publisher, true)
	if e != nil {
		return out, e
	}
	if e = r.quoteCurrent(ctx, tx, p, g, true); e != nil {
		return out, quotePolicyFailure(tx, e)
	}
	out = f.QuoteAuthorization{Context: []any{f.Context, map[string]any{"QuoteAuthorization": "https://w3id.org/fep/044f#QuoteAuthorization", "gts": "https://gotosocial.org/ns#", "interactingObject": map[string]string{"@id": "gts:interactingObject", "@type": "@id"}, "interactionTarget": map[string]string{"@id": "gts:interactionTarget", "@type": "@id"}}}, Type: "QuoteAuthorization", ID: r.quoteID(g.id), AttributedTo: r.actorID(p), InteractingObject: g.object, InteractionTarget: r.objectID(g.target)}
	if e = tx.Commit(); e != nil {
		return f.QuoteAuthorization{}, e
	}
	return out, nil
}
func (r *Repository) validateQuoteUse(ctx context.Context, tx *sql.Tx, p publisher, k f.RemoteKey, n *f.Note) error {
	if n.Quote == "" && n.QuoteAuthorization == "" {
		return nil
	}
	prefix := r.config.Origin + "/quote-authorizations/"
	if !strings.HasPrefix(n.QuoteAuthorization, prefix) {
		return f.ErrForbidden
	}
	id := strings.TrimPrefix(n.QuoteAuthorization, prefix)
	if !authority.ValidID(id) || n.QuoteAuthorization != r.quoteID(id) {
		return f.ErrForbidden
	}
	g, e := scanQuote(tx.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM owned_federation_quote_authorizations WHERE id=$1`, id))
	if e != nil {
		return e
	}
	if g.publisher != p.id || g.actor != k.Actor || g.object != n.ID || n.Quote != r.objectID(g.target) {
		return f.ErrForbidden
	}
	if g.key != k.KeyID || !bytes.Equal(g.fingerprint, quoteKey(k)) {
		return r.invalidateQuoteKey(ctx, tx, g)
	}
	if e = r.quoteCurrent(ctx, tx, p, g, true); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET consumed_at=COALESCE(consumed_at,$2) WHERE id=$1`, id, r.config.Clock())
	return e
}
func (r *Repository) RevokeQuoteAuthorization(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return f.ErrNotFound
	}
	var pid string
	if e := r.db.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_federation_quote_authorizations WHERE id=$1`, id).Scan(&pid); e != nil {
		return mapped(e)
	}
	tx, p, e := r.beginLocal(ctx, actor, pid)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	g, e := scanQuote(tx.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM owned_federation_quote_authorizations WHERE id=$1 AND publisher_id=$2`, id, p.id))
	if e != nil {
		return e
	}
	if g.revoked.Valid {
		return nil
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=$2 WHERE id=$1`, id, r.config.Clock()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_objects SET object=object-'quote'-'quoteAuthorization' WHERE publisher_id=$1 AND object_id=$2`, p.id, g.object); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',lease_token=NULL,lease_until=NULL,finished_at=$2 WHERE quote_id=$1 AND activity_type='Accept' AND status IN('pending','inflight')`, id, r.config.Clock()); e != nil {
		return e
	}
	// Revoke is local and succeeds even when a current remote key is unavailable.
	// Delivery revalidates that actor and this previously pinned destination later.
	var inbox string
	if e = tx.QueryRowContext(ctx, `SELECT inbox_uri FROM owned_federation_peers WHERE actor_uri=$1`, g.actor).Scan(&inbox); e != nil {
		return e
	}
	origin, _ := f.Origin(g.actor)
	destination, _ := f.Origin(inbox)
	// Removing a peer from the egress allowlist must not prevent local
	// withdrawal. Suppress a now-disallowed notification, not the revocation.
	if r.config.Allows(inbox) && origin == destination {
		deletion, e := r.activity(p, "Delete", map[string]string{"id": r.quoteID(id), "type": "Tombstone"}, g.actor)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_deliveries(publisher_id,actor_uri,inbox_uri,activity_id,activity_type,payload,quote_id,next_attempt,created_at)VALUES($1,$2,$3,$4,'Delete',$5,$6,$7,$7)`, p.id, g.actor, inbox, deletion.ID, []byte(marshal(deletion)), id, r.config.Clock()); e != nil {
			return e
		}
	}
	if e = r.audit(ctx, tx, actor, p.id, "quote.revoke"); e != nil {
		return e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.Permission); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) quoteDeliveryCurrent(ctx context.Context, tx *sql.Tx, p publisher, d delivery) (bool, bool, error) {
	var id sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT quote_id::text FROM owned_federation_deliveries WHERE id=$1`, d.id).Scan(&id); e != nil {
		return false, false, e
	}
	if !id.Valid {
		return false, true, nil
	}
	g, e := scanQuote(tx.QueryRowContext(ctx, `SELECT `+quoteColumns+` FROM owned_federation_quote_authorizations WHERE id=$1`, id.String))
	if e != nil {
		return true, false, e
	}
	if g.publisher != p.id || g.actor != d.remote {
		return true, false, nil
	}
	var a f.Activity
	if json.Unmarshal(d.body, &a) != nil {
		return true, false, nil
	}
	object, e := f.ObjectID(a.Object)
	if e != nil {
		return true, false, nil
	}
	if d.kind == "Delete" {
		return true, g.revoked.Valid && object == r.quoteID(g.id) && a.Result == "", nil
	}
	if d.kind != "Accept" || object != g.request || a.Result != r.quoteID(g.id) {
		return true, false, nil
	}
	e = r.quoteCurrent(ctx, tx, p, g, true)
	if errors.Is(e, f.ErrNotFound) || errors.Is(e, f.ErrForbidden) {
		return true, false, nil
	}
	return true, e == nil, e
}
