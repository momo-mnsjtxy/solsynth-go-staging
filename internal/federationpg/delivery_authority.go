// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

//go:embed delivery_schema.sql
var DeliverySchema string

// deliveryIntent records which original, authenticated local operation permits
// reconciliation. It is not a credential: every observation evaluates current
// canonical authority, ancestry, policy and membership again.
type deliveryIntent struct {
	Requester string          `json:"requester"`
	Owner     string          `json:"owner"`
	Session   string          `json:"session"`
	Version   int64           `json:"version"`
	Epoch     int64           `json:"epoch"`
	Stamp     json.RawMessage `json:"stamp"`
	Expires   time.Time       `json:"expires"`
}

func localFingerprint(signer crypto.Signer) (string, error) {
	pem, e := f.PEM(signer)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(pem))
	return hex.EncodeToString(sum[:]), nil
}
func deliveryBinding(k f.RemoteKey) (string, error) {
	fingerprint, e := f.KeyFingerprint(k.PublicKey)
	if e != nil {
		return "", e
	}
	raw, e := json.Marshal([]string{k.Actor, k.Inbox, k.KeyID, fingerprint, k.Incarnation})
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (r *Repository) newDeliveryIntent(ctx context.Context, tx *sql.Tx, p publisher) (deliveryIntent, error) {
	var i deliveryIntent
	if !authority.ValidID(p.requester) || r.guard == nil {
		return i, f.ErrForbidden
	}
	proof, e := r.guard.Authorize(ctx, r.db, tx, p.requester, f.Permission)
	if e != nil {
		return i, e
	}
	i = deliveryIntent{Requester: p.requester, Owner: p.owner, Session: proof.Session.ID, Version: proof.Account.Version, Epoch: proof.Session.AuthorizationEpoch}
	if i.Stamp, e = deliveryStamp(ctx, tx, i.Requester, p.id, i.Session); e != nil {
		return i, e
	}
	i.Expires, e = discoveryDeadline(ctx, tx, i.Requester, i.Owner, i.Session)
	return i, e
}
func (r *Repository) enqueueIntent(ctx context.Context, tx *sql.Tx, p publisher) (deliveryIntent, error) {
	if p.requester != "" {
		return r.newDeliveryIntent(ctx, tx, p)
	}
	var i deliveryIntent
	var raw []byte
	if e := tx.QueryRowContext(ctx, `SELECT delivery_intent FROM owned_federation_publishers WHERE publisher_id=$1`, p.id).Scan(&raw); e != nil {
		return i, e
	}
	if json.Unmarshal(raw, &i) != nil {
		return i, f.ErrForbidden
	}
	return i, nil
}
func (r *Repository) deliveryAllowed(ctx context.Context, tx *sql.Tx, p publisher, i deliveryIntent) (bool, error) {
	guard, ok := r.guard.(storedDiscoveryGuard)
	if !ok {
		return false, f.ErrUnavailable
	}
	if !authority.ValidID(i.Requester) || !authority.ValidID(i.Owner) || !authority.ValidID(i.Session) || i.Owner != p.owner || i.Version < 1 || i.Epoch < 1 || len(i.Stamp) == 0 {
		return false, nil
	}
	allowed, e := guard.StoredSessionEligible(ctx, r.db, tx, i.Requester, i.Session, i.Version, i.Epoch, f.Permission)
	if e != nil || !allowed {
		return false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers p JOIN owned_authority_accounts a ON a.id=p.account_id JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$2 WHERE p.id=$1 AND p.account_id=$3 AND p.type=0 AND p.realm_id IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND NOT a.disabled AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=100 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments x WHERE x.account_id=a.id AND x.kind IN('disable_account','permission_modification') AND(x.starts_at IS NULL OR x.starts_at<=clock_timestamp())AND(x.expires_at IS NULL OR x.expires_at>clock_timestamp()))) AND $4::timestamptz>clock_timestamp()`, p.id, i.Requester, i.Owner, i.Expires).Scan(&allowed)
	if e != nil || !allowed {
		return false, e
	}
	stamp, e := deliveryStamp(ctx, tx, i.Requester, p.id, i.Session)
	return e == nil && bytes.Equal(stamp, i.Stamp), e
}

// lockDeliveryReservations precedes every public-key row lock, including those
// taken by quote validation. Acquiring SHARE after a key's FOR SHARE row lock
// deadlocks with a revoker holding ROW EXCLUSIVE while waiting for that row.
func lockDeliveryReservations(ctx context.Context, tx *sql.Tx) (bool, error) {
	var ledger bool
	if e := tx.QueryRowContext(ctx, `SELECT to_regclass('owned_federation_public_keys') IS NOT NULL`).Scan(&ledger); e != nil {
		return false, e
	}
	if ledger {
		if _, e := tx.ExecContext(ctx, `LOCK TABLE owned_federation_public_keys IN SHARE MODE`); e != nil {
			return false, e
		}
	}
	return ledger, nil
}

// deliveryRemoteFence also excludes a static pin newly colliding with a durable
// reservation. The short table lock closes the absent-row insertion race only
// through observation commit. It is always released before external transport.
func (r *Repository) deliveryRemoteFence(ctx context.Context, tx *sql.Tx, k f.RemoteKey) error {
	ledger, e := lockDeliveryReservations(ctx, tx)
	if e != nil {
		return e
	}
	if !ledger {
		if k.Incarnation != "" {
			return f.ErrForbidden
		}
		return nil
	}
	if k.Incarnation != "" {
		return r.publicKeyFence(ctx, tx, k)
	}
	fingerprint, e := f.KeyFingerprint(k.PublicKey)
	if e != nil {
		return e
	}
	var collision bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_public_keys WHERE actor_uri=$1 OR key_id=$2 OR fingerprint=$3)`, k.Actor, k.KeyID, fingerprint).Scan(&collision)
	if e != nil {
		return e
	}
	if collision {
		return f.ErrForbidden
	}
	return nil
}

func deliveryDenial(e error) bool {
	return errors.Is(e, f.ErrNotFound) || errors.Is(e, f.ErrForbidden)
}

func deliveryStamp(ctx context.Context, q queryer, actor, publisher, session string) ([]byte, error) {
	var stamp []byte
	e := q.QueryRowContext(ctx, `WITH RECURSIVE chain AS(SELECT id,parent_id,authorization_epoch,ARRAY[id] path FROM owned_authority_sessions WHERE id=NULLIF($3::text,'')::uuid AND account_id=$1::uuid UNION ALL SELECT s.id,s.parent_id,s.authorization_epoch,c.path||s.id FROM chain c JOIN owned_authority_sessions s ON s.id=c.parent_id AND s.account_id=$1::uuid WHERE NOT s.id=ANY(c.path) AND cardinality(c.path)<64) SELECT jsonb_build_object('actor',COALESCE((SELECT revision FROM owned_federation_delivery_epochs WHERE kind='account' AND entity_id=$1::uuid),0),'owner',COALESCE((SELECT revision FROM owned_federation_delivery_epochs WHERE kind='account' AND entity_id=p.account_id),0),'publisher',COALESCE((SELECT revision FROM owned_federation_delivery_epochs WHERE kind='publisher' AND entity_id=p.id),0),'owner_id',p.account_id,'sessions',(SELECT jsonb_agg(jsonb_build_object('id',id,'epoch',authorization_epoch) ORDER BY id) FROM chain)) FROM owned_publishers p WHERE p.id=$2::uuid`, actor, publisher, session).Scan(&stamp)
	return stamp, e
}
