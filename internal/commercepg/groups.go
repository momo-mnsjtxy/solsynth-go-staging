// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"database/sql"
	"encoding/json"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"time"
)

func (r *Repository) SubscriptionGroup(ctx context.Context, actor, group string) (c.GroupState, error) {
	out := c.GroupState{GroupIdentifier: group, Catalog: c.GroupCatalog{GroupIdentifier: group, Items: []c.Definition{}}, Subscriptions: []c.GroupItem{}}
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.definitions WHERE enabled AND COALESCE(NULLIF(document->>'group_identifier',''),identifier)=$1 ORDER BY identifier`, group)
	if e != nil {
		return out, e
	}
	defs := map[string]*c.Definition{}
	for rows.Next() {
		var b []byte
		var d c.Definition
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &d)
		}
		if e != nil {
			rows.Close()
			return out, e
		}
		defs[d.Identifier] = &d
		out.Catalog.Items = append(out.Catalog.Items, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(defs) == 0 {
		return out, c.ErrNotFound
	}
	out.Catalog.DisplayName = out.Catalog.Items[0].DisplayName
	rows, e = tx.QueryContext(ctx, `SELECT s.document FROM commerce_v1.subscriptions s JOIN commerce_v1.definitions d ON d.identifier=s.identifier WHERE s.account_id=$1 AND COALESCE(NULLIF(d.document->>'group_identifier',''),d.identifier)=$2 ORDER BY s.begun_at,s.id`, actor, group)
	if e != nil {
		return out, e
	}
	now := r.now().UTC()
	for rows.Next() {
		var b []byte
		var s c.Subscription
		if e = rows.Scan(&b); e == nil {
			e = json.Unmarshal(b, &s)
		}
		if e != nil {
			rows.Close()
			return out, e
		}
		item := c.GroupItem{Subscription: s, Definition: defs[s.Identifier]}
		out.Subscriptions = append(out.Subscriptions, item)
		if s.Status == c.SubscriptionActive && s.IsActive && !s.IsTesting && s.EndedAt.After(now) {
			if !s.BegunAt.After(now) {
				out.Current = &item
			} else if out.Next == nil {
				out.Next = &item
			}
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}

// The account authority row serializes paid grants in a group. Purchased time
// queues after its existing tail, and sandbox entitlements stay separate.
func groupStart(ctx context.Context, tx *sql.Tx, actor, group, identifier, exclude string, begin time.Time, testing bool) (time.Time, error) {
	if group == "" {
		group = identifier
	}
	var tail sql.NullTime
	e := tx.QueryRowContext(ctx, `SELECT max(s.ended_at) FROM commerce_v1.subscriptions s JOIN commerce_v1.definitions d ON d.identifier=s.identifier WHERE s.account_id=$1 AND s.status=1 AND ($2='' OR s.id::text<>$2) AND COALESCE(NULLIF(d.document->>'group_identifier',''),d.identifier)=$3 AND COALESCE((s.document->>'is_testing')::boolean,false)=$4`, actor, exclude, group, testing).Scan(&tail)
	if e != nil {
		return begin, e
	}
	if tail.Valid && tail.Time.After(begin) {
		begin = tail.Time
	}
	return begin, nil
}
