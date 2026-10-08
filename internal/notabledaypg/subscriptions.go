// SPDX-License-Identifier: AGPL-3.0-only
package notabledaypg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
	"sort"
	"time"
)

func sortStrings(v []string) { sort.Strings(v) }
func (r *Repository) subscriptions(ctx context.Context, tx *sql.Tx, actor string, c notabledays.Command, now time.Time) (out notabledays.Result, e error) {
	if c.Operation == "subscription.list" || c.Operation == "subscription.subscribers" {
		field, where := "s.target_account_id", "s.subscriber_id"
		if c.Operation == "subscription.subscribers" {
			field, where = "s.subscriber_id", "s.target_account_id"
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+field+`::text FROM owned_calendar_subscriptions s JOIN owned_authority_accounts a ON a.id=`+field+` WHERE `+where+`=$1::uuid AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_social_relationships b WHERE b.status=-100 AND((b.account_id=$1::uuid AND b.related_id=`+field+`)OR(b.related_id=$1::uuid AND b.account_id=`+field+`))) ORDER BY s.created_at,s.id LIMIT 1001`, actor)
		if e != nil {
			return out, e
		}
		defer rows.Close()
		v := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return out, e
			}
			v = append(v, id)
			if len(v) > 1000 {
				return out, notabledays.ErrLimit
			}
		}
		out.Value = v
		return out, rows.Err()
	}
	if !authority.ValidID(c.TargetID) || c.TargetID == actor {
		return out, notabledays.ErrInvalid
	}
	var existing notabledays.Subscription
	e = tx.QueryRowContext(ctx, `SELECT id::text,subscriber_id::text,target_account_id::text,created_at FROM owned_calendar_subscriptions WHERE subscriber_id=$1::uuid AND target_account_id=$2::uuid FOR UPDATE`, actor, c.TargetID).Scan(&existing.ID, &existing.SubscriberID, &existing.TargetAccountID, &existing.CreatedAt)
	if c.Operation == "subscription.remove" {
		if e != nil {
			return out, mapped(e)
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_calendar_subscriptions WHERE id=$1::uuid`, existing.ID)
		out.Changed = e == nil
		out.ResourceID = existing.ID
		return out, e
	}
	if _, _, err := resolveAccount(ctx, tx, actor, c.TargetID); err != nil {
		return out, err
	}
	if e == nil {
		out.Value = existing
		return out, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	var n int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_calendar_subscriptions WHERE subscriber_id=$1::uuid`, actor).Scan(&n); e != nil {
		return out, e
	}
	if n >= 100 {
		return out, notabledays.ErrLimit
	}
	existing = notabledays.Subscription{SubscriberID: actor, TargetAccountID: c.TargetID, CreatedAt: now}
	existing.ID, e = id()
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_calendar_subscriptions(id,subscriber_id,target_account_id,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::timestamptz)`, existing.ID, actor, c.TargetID, now)
	out.Value = existing
	out.Changed = e == nil
	out.ResourceID = existing.ID
	return out, e
}
