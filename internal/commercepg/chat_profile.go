// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/messager/rooms"
	"time"
)

// The constructor shares only mapping validation with chat search. The profile
// entitlement itself uses the current canonical paid internal subscription
// projection and fresh time checks, with level 2 required by the authored route.
func (r *Repository) ChatProfilePerks(levels map[string]int) (*ChatSearchPerks, error) {
	return r.ChatSearchPerks(levels)
}
func (p *ChatSearchPerks) RequireChatProfile(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, now time.Time) error {
	if p == nil || p.repo == nil || p.repo.db != db || db == nil || tx == nil || p.repo.guard == nil {
		return rooms.ErrUnavailable
	}
	if _, e := p.repo.guard.Authorize(ctx, db, tx, actor, "chat.members.manage"); e != nil {
		return e
	}
	level, e := p.repo.realmAccountPerk(ctx, tx, actor, p.levels, now)
	if e != nil {
		var pg interface{ SQLState() string }
		if errors.As(e, &pg) && pg.SQLState() == "55P03" {
			return rooms.ErrConflict
		}
		return e
	}
	if level < 2 {
		return rooms.ErrForbidden
	}
	return nil
}
