// SPDX-License-Identifier: AGPL-3.0-only
package friendeventpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
)

type StatusProjector interface {
	FriendStatusEventInTransaction(context.Context, *sql.Tx, string, Source) (json.RawMessage, bool, error)
}

func (r *Repository) WithStatus(p StatusProjector) *Repository { r.status = p; return r }

// Each canonical presence change advances a durable generation even if hidden.
// An invisible/private interval therefore fences old activity/status packets
// permanently, including a hide-and-restore before a queued packet is replayed.
func CaptureStatus(ctx context.Context, tx *sql.Tx, p authority.Principal) error {
	revision, e := advancePresence(ctx, tx, p)
	if e != nil {
		return e
	}
	return capture(ctx, tx, Source{Owner: p.Account.ID, Session: p.Session.ID, Kind: "status", Version: p.Account.Version, Epoch: p.Session.AuthorizationEpoch, Revision: revision}, 1)
}

func advancePresence(ctx context.Context, tx *sql.Tx, p authority.Principal) (int64, error) {
	var revision int64
	e := tx.QueryRowContext(ctx, `INSERT INTO owned_friend_presence_versions(account_id,revision)VALUES($1::uuid,1)ON CONFLICT(account_id)DO UPDATE SET revision=owned_friend_presence_versions.revision+1 RETURNING revision`, p.Account.ID).Scan(&revision)
	return revision, e
}
func FencePresence(ctx context.Context, tx *sql.Tx, p authority.Principal) error {
	_, e := advancePresence(ctx, tx, p)
	return e
}
func CaptureConnection(ctx context.Context, tx *sql.Tx, p authority.Principal) error {
	revision, e := advancePresence(ctx, tx, p)
	if e != nil {
		return e
	}
	var visible bool
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT online_visibility<>'private' FROM owned_presence_preferences WHERE account_id=$1::uuid),true)`, p.Account.ID).Scan(&visible)
	if e != nil || !visible {
		return e
	}
	return capture(ctx, tx, Source{Owner: p.Account.ID, Session: p.Session.ID, Kind: "status", Version: p.Account.Version, Epoch: p.Session.AuthorizationEpoch, Revision: revision}, 1)
}
