// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
	c "solsynth.local/go-migration/ports/wallet/commerce"
)

// ChatSearchPerks reuses the canonical paid-subscription projections and
// definition validation used by realm boosts. The identifier-to-level map is
// explicit owned policy; no request field or custom-app grant confers a perk.
type ChatSearchPerks struct {
	repo   *Repository
	levels map[string]int
}

var _ rooms.SearchEntitlement = (*ChatSearchPerks)(nil)

func (r *Repository) ChatSearchPerks(levels map[string]int) (*ChatSearchPerks, error) {
	if r == nil || r.db == nil || r.guard == nil || levels == nil {
		return nil, rooms.ErrUnavailable
	}
	if len(levels) > 100 {
		return nil, rooms.ErrInvalid
	}
	p := &ChatSearchPerks{repo: r, levels: map[string]int{}}
	for id, level := range levels {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 256 || level < 1 || level > 3 {
			return nil, rooms.ErrInvalid
		}
		p.levels[id] = level
	}
	return p, nil
}
func (p *ChatSearchPerks) RequireChatSearch(ctx context.Context, db *sql.DB, tx *sql.Tx, actor string, now time.Time) error {
	if p == nil || p.repo == nil || db == nil || db != p.repo.db || tx == nil || p.repo.guard == nil || !authority.ValidID(actor) {
		return rooms.ErrUnavailable
	}
	if _, e := p.repo.guard.Authenticate(ctx, db, tx, actor); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND status=1 AND begun_at<=$2::timestamptz AND ended_at>$2::timestamptz ORDER BY id LIMIT 1001`, actor, now)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) > 1000 {
		return rooms.ErrUnavailable
	}
	eligible := []c.Subscription{}
	for _, id := range ids {
		row, x := lockedSubscriptionProjection(ctx, tx, id)
		if x != nil {
			return x
		}
		sub, reason := row.validate()
		if reason != "" {
			return rooms.ErrConflict
		}
		if p.levels[sub.Identifier] < 1 || sub.AccountID != actor || sub.AppIdentifier != nil || !sub.IsActive || sub.IsTesting || sub.Status != c.SubscriptionActive || sub.BegunAt.After(now) || !sub.EndedAt.After(now) {
			continue
		}
		def, x := definition(ctx, tx, sub.Identifier)
		if errors.Is(x, c.ErrNotFound) {
			continue
		}
		if x != nil {
			return x
		}
		if def.AppIdentifier == nil && def.ProductID == "" {
			eligible = append(eligible, sub)
		}
	}
	if len(eligible) == 0 {
		return rooms.ErrSearchEntitlement
	}
	// The supplied time selects candidates before subscription/definition locks.
	// Even a final caller revalidation can discover a newly started paid cycle
	// whose definition was not locked by the first pass. Recheck every retained
	// cycle after all lock waits; an earlier eligible cycle may also expire while
	// a later candidate is blocked. Canonical rows and definitions remain locked.
	if e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	for _, sub := range eligible {
		if !sub.BegunAt.After(now) && sub.EndedAt.After(now) {
			return nil
		}
	}
	return rooms.ErrSearchEntitlement
}
