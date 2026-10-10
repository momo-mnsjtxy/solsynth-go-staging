// SPDX-License-Identifier: AGPL-3.0-only
package authoritypg

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"solsynth.local/go-migration/ports/authority"
)

var _ authority.BrowserTicketRedeemer = (*Repository)(nil)

// RedeemBrowserTicket atomically consumes a live shared capability. Coordinates
// are returned only through the authority verifier's private store interface.
func (r *Repository) RedeemBrowserTicket(ctx context.Context, hash []byte) (actor, session string, version, sessionEpoch, authorizationEpoch int64, use string, issuedAt, expiresAt time.Time, err error) {
	if r == nil || r.db == nil || len(hash) != 32 {
		err = authority.ErrInvalid
		return
	}
	err = r.db.QueryRowContext(ctx, `DELETE FROM owned_authority_stream_tickets WHERE ticket_hash=$1 AND expires_at>clock_timestamp() RETURNING account_id::text,session_id::text,account_version,session_epoch,authorization_epoch,credential_use,issued_at,expires_at`, hash).Scan(&actor, &session, &version, &sessionEpoch, &authorizationEpoch, &use, &issuedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = authority.ErrExpired
	}
	return
}
