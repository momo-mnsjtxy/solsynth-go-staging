// SPDX-License-Identifier: AGPL-3.0-only
package commercepg

import (
	"context"
	"encoding/json"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	c "solsynth.local/go-migration/ports/wallet/commerce"
	"sort"
	"strings"
	"time"
)

const activationLimit = 1000

func storedGroup(s c.Subscription) string {
	if s.GroupIdentifier != "" {
		return s.GroupIdentifier
	}
	return s.Identifier
}

// planActivation only reallocates remaining paid duration. It never invents a
// missing duration, resurrects an expired grant or changes billing economics.
func planActivation(subscriptions []c.Subscription, target string, now time.Time) ([]c.Subscription, bool, error) {
	subs := append([]c.Subscription(nil), subscriptions...)
	sort.Slice(subs, func(i, j int) bool {
		if !subs[i].BegunAt.Equal(subs[j].BegunAt) {
			return subs[i].BegunAt.Before(subs[j].BegunAt)
		}
		if !subs[i].CreatedAt.Equal(subs[j].CreatedAt) {
			return subs[i].CreatedAt.Before(subs[j].CreatedAt)
		}
		return subs[i].ID < subs[j].ID
	})
	current, selected := -1, -1
	durations := map[string]time.Duration{}
	var total time.Duration
	var tail time.Time
	for i, s := range subs {
		if s.Status != c.SubscriptionActive || !s.IsActive || s.IsTesting || !s.EndedAt.After(now) || !s.EndedAt.After(s.BegunAt) {
			return nil, false, c.ErrConflict
		}
		if !tail.IsZero() && s.BegunAt.Before(tail) {
			return nil, false, c.ErrConflict
		}
		tail = s.EndedAt
		begin := s.BegunAt
		if !begin.After(now) {
			begin = now
			current = i
		}
		duration := s.EndedAt.Sub(begin)
		if duration <= 0 || !begin.Add(duration).Equal(s.EndedAt) || duration > time.Duration(1<<63-1)-total {
			return nil, false, c.ErrConflict
		}
		total += duration
		durations[s.ID] = duration
		if s.ID == target {
			selected = i
		}
	}
	if selected < 0 {
		return nil, false, c.ErrNotFound
	}
	if selected == current {
		return subs, false, nil
	}
	order := []c.Subscription{subs[selected]}
	if current >= 0 {
		order = append(order, subs[current])
	}
	for i, s := range subs {
		if i != selected && i != current {
			order = append(order, s)
		}
	}
	cursor := now
	for i := range order {
		s := &order[i]
		end := cursor.Add(durations[s.ID])
		s.BegunAt = cursor
		s.EndedAt = end
		s.UpdatedAt = now
		if s.RenewalAt != nil {
			s.RenewalAt = &end
		}
		cursor = end
	}
	return order, true, nil
}

func (r *Repository) PendingActivations(ctx context.Context, actor string, q c.PageQuery) (c.PendingActivations, error) {
	out := c.PendingActivations{Subscriptions: []c.Subscription{}}
	tx, e := r.userTx(ctx, actor, "")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	q = bounds(q)
	rows, e := tx.QueryContext(ctx, `SELECT document FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND status=1 AND document->>'is_active'='true' AND begun_at>$2::timestamptz AND document->>'is_testing' IS DISTINCT FROM 'true' ORDER BY begun_at,id LIMIT 1001`, actor, now)
	if e != nil {
		return out, e
	}
	candidates := []c.Subscription{}
	for rows.Next() {
		var raw []byte
		var s c.Subscription
		if e = rows.Scan(&raw); e == nil {
			e = json.Unmarshal(raw, &s)
		}
		if e != nil {
			rows.Close()
			return out, e
		}
		candidates = append(candidates, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(candidates) > activationLimit {
		return out, c.ErrUnavailable
	}
	eligible := []c.Subscription{}
	for _, s := range candidates {
		s, e = subscriptionAccess(ctx, tx, s, now)
		if e != nil {
			return out, e
		}
		if s.IsActive && s.BegunAt.After(now) {
			eligible = append(eligible, s)
		}
	}
	out.TotalCount = len(eligible)
	if q.Offset < len(eligible) {
		end := q.Offset + q.Take
		if end > len(eligible) {
			end = len(eligible)
		}
		out.Subscriptions = eligible[q.Offset:end]
	}
	// The retained controller uses the first record in the requested page.
	if len(out.Subscriptions) > 0 {
		at := out.Subscriptions[0].BegunAt
		out.NextActivationAt = &at
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

func (r *Repository) SwitchSubscriptionActivation(ctx context.Context, actor, group, target string) (c.GroupState, error) {
	var out c.GroupState
	if !wallet.ValidID(target) || strings.TrimSpace(group) == "" || len(group) > 512 {
		return out, c.ErrInvalid
	}
	tx, e := r.userTx(ctx, actor, "subscriptions.groups.manage")
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	now := r.now().UTC()
	rows, e := tx.QueryContext(ctx, `SELECT id::text FROM commerce_v1.subscriptions WHERE account_id=$1::uuid AND status=1 AND document->>'is_active'='true' AND ended_at>$3::timestamptz AND COALESCE(NULLIF(document->>'group_identifier',''),identifier)=$2 AND document->>'is_testing' IS DISTINCT FROM 'true' ORDER BY id LIMIT 1001`, actor, group, now)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(ids) > activationLimit {
		return out, c.ErrUnavailable
	}
	subs := []c.Subscription{}
	defs := map[string]c.Definition{}
	var groupApp *string
	first := true
	for _, id := range ids {
		row, e := lockedSubscriptionProjection(ctx, tx, id)
		if e != nil {
			return out, e
		}
		s, reason := row.validate()
		if reason != "" || s.AccountID != actor || storedGroup(s) != group {
			return out, c.ErrConflict
		}
		d, e := definition(ctx, tx, s.Identifier)
		if e != nil {
			return out, e
		}
		if first {
			groupApp = d.AppIdentifier
			first = false
		} else if !sameDefinitionApp(groupApp, d.AppIdentifier) {
			return out, c.ErrConflict
		}
		definitionGroup := d.GroupIdentifier
		if definitionGroup == "" {
			definitionGroup = d.Identifier
		}
		if definitionGroup != group {
			return out, c.ErrConflict
		}
		s, e = subscriptionAccess(ctx, tx, s, now)
		if e != nil {
			return out, e
		}
		if !s.IsActive {
			return out, c.ErrForbidden
		}
		// Moving paid time must not restore an opt-out or create provider charges.
		s.IsAutoRenewal = row.Renew && s.RenewalAt != nil && d.AllowInternalWalletRenewal && s.PaymentMethod == "solian.wallet"
		if !s.IsAutoRenewal {
			s.RenewalAt = nil
		}
		subs = append(subs, s)
		defs[s.Identifier] = d
	}
	// Refresh after app/definition/consent waits. No elapsed time is credited.
	now = r.now().UTC()
	planned, changed, e := planActivation(subs, target, now)
	if e != nil {
		return out, e
	}
	if changed {
		for _, s := range planned {
			if e = saveSubscription(ctx, tx, s); e != nil {
				return out, e
			}
		}
		payload := struct {
			GroupIdentifier string           `json:"group_identifier"`
			SubscriptionID  string           `json:"subscription_id"`
			Subscriptions   []c.Subscription `json:"subscriptions"`
		}{group, target, planned}
		if e = event(ctx, tx, "subscription.activation_switched", target, payload, now); e != nil {
			return out, e
		}
	}
	for _, s := range planned {
		if _, e = recurringConsent(ctx, tx, actor, defs[s.Identifier], s.ConsentVersion); e != nil {
			return out, e
		}
	}
	out, e = subscriptionGroup(ctx, tx, actor, group, now)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "subscriptions.groups.manage"); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

var _ c.ActivationStore = (*Repository)(nil)
