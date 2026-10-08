// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"database/sql"
	_ "embed"
	"net/url"
	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

// IdentitySchema is explicit migration input, never installed by a constructor.
//
//go:embed identity_schema.sql
var IdentitySchema string

func (r *Repository) identityFor(handle string) (string, string, error) {
	if !f.ValidName(handle) {
		return "", "", f.ErrUnavailable
	}
	actor := r.config.Origin + "/activitypub/actors/" + url.PathEscape(handle)
	if len(actor)+len("#main-key") > 2048 {
		return "", "", f.ErrUnavailable
	}
	return actor, actor + "#main-key", nil
}
func (r *Repository) identityMatches(p publisher) bool {
	actor, key, e := r.identityFor(p.identityHandle)
	return e == nil && p.actorURI == actor && p.keyID == key
}

// InitializeIdentity is a trusted, explicit operator operation. It never loads
// or generates keys, sends, changes handles, or enables federation. A different
// configured origin cannot rewrite a prior anchor.
func (r *Repository) InitializeIdentity(ctx context.Context, publisherID string) error {
	if r == nil || r.db == nil || !authority.ValidID(publisherID) {
		return f.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var owner string
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1::uuid AND type=0`, publisherID).Scan(&owner); e != nil {
		return mapped(e)
	}
	var account string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, owner).Scan(&account); e != nil {
		return mapped(e)
	}
	var currentOwner string
	if e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_publishers WHERE id=$1::uuid AND type=0 FOR UPDATE`, publisherID).Scan(&currentOwner); e != nil {
		return mapped(e)
	}
	if currentOwner != owner {
		return f.ErrUnavailable
	}
	var handle string
	var actor, key sql.NullString
	if e = tx.QueryRowContext(ctx, `SELECT identity_handle,actor_uri,key_id FROM owned_federation_publishers WHERE publisher_id=$1::uuid FOR UPDATE`, publisherID).Scan(&handle, &actor, &key); e != nil {
		return mapped(e)
	}
	wantActor, wantKey, e := r.identityFor(handle)
	if e != nil {
		return e
	}
	if actor.Valid || key.Valid {
		if !actor.Valid || !key.Valid || actor.String != wantActor || key.String != wantKey {
			return f.ErrInvalid
		}
		return tx.Commit()
	}
	var reserved bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_name_reservations WHERE publisher_id=$1::uuid AND folded_name=lower($2::text))`, publisherID, handle).Scan(&reserved); e != nil {
		return e
	}
	if !reserved {
		return f.ErrUnavailable
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_publishers SET actor_uri=$2::text,key_id=$3::text WHERE publisher_id=$1::uuid`, publisherID, wantActor, wantKey); e != nil {
		return e
	}
	return tx.Commit()
}

// InitializeIdentities is a bounded migration batch. Call explicitly until zero
// is returned. Existing anchors from a different origin reject the operation.
func (r *Repository) InitializeIdentities(ctx context.Context, limit int) (int, error) {
	if r == nil || limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	var mismatch bool
	if e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_publishers WHERE actor_uri IS NOT NULL AND actor_uri<>$1::text||'/activitypub/actors/'||identity_handle)`, r.config.Origin).Scan(&mismatch); e != nil {
		return 0, e
	}
	if mismatch {
		return 0, f.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT publisher_id::text FROM owned_federation_publishers WHERE actor_uri IS NULL ORDER BY publisher_id LIMIT $1::integer`, limit)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for i, id := range ids {
		if e = r.InitializeIdentity(ctx, id); e != nil {
			return i, e
		}
	}
	return len(ids), nil
}
