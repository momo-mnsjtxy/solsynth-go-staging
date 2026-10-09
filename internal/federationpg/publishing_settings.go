// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"database/sql"
	"errors"

	f "solsynth.local/go-migration/ports/federation"
)

// The caller already holds the canonical publisher lock. This observes a local
// registered actor and never enables federation or creates keys/delivery intent.
func (r *Repository) PublishingActorEnabled(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	if r == nil || tx == nil {
		return false, nil
	}
	var held string
	e := tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_federation_publishers WHERE publisher_id=$1 FOR SHARE`, id).Scan(&held)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	// Use the same local-actor eligibility and immutable origin/handle anchor
	// as actor rendering. A matching key suffix alone does not identify a local
	// actor. This path deliberately neither loads keys nor provisions anything.
	p, e := r.publisher(ctx, tx, id, true)
	if errors.Is(e, f.ErrNotFound) || errors.Is(e, f.ErrUnavailable) {
		return false, nil
	}
	return e == nil && p.registered, e
}
