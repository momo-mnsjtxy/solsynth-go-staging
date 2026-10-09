// SPDX-License-Identifier: AGPL-3.0-only
package progressionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/progression"
	"strings"
	"time"
)

type canonicalEvent struct {
	key, kind, source string
	at                time.Time
	meta              map[string]string
}

// RecordCanonicalEvent is an internal hook, deliberately absent from HTTP. It
// accepts only an ID of an owned canonical record, never XP, time, event IDs or metadata.
func (r *Repository) RecordCanonicalEvent(ctx context.Context, actor, kind, sourceID string) error {
	if actor == "" {
		return authority.ErrInvalid
	}
	return r.within(ctx, actor, nil, func(tx *sql.Tx, now time.Time) error { return r.applyCanonical(ctx, tx, actor, kind, sourceID, now) })
}

// ApplyCanonicalEvent joins the caller's transaction and rechecks opaque proof.
// The caller must take its canonical actor authority lock before domain locks,
// supply a transaction from the same pool, and commit/rollback all effects together.
func (r *Repository) ApplyCanonicalEvent(ctx context.Context, tx *sql.Tx, actor, kind, sourceID string) error {
	if r == nil || r.db == nil || r.guard == nil || tx == nil {
		return authority.ErrUnavailable
	}
	if actor == "" {
		return authority.ErrInvalid
	}
	if _, e := r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return e
	}
	var now time.Time
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	return r.applyCanonical(ctx, tx, actor, kind, sourceID, now.UTC())
}
func sourceEvent(ctx context.Context, tx *sql.Tx, actor, kind, source string, now time.Time) (v canonicalEvent, e error) {
	v = canonicalEvent{kind: kind, source: source, meta: map[string]string{}}
	if !authority.ValidID(source) {
		return v, progression.ErrInvalid
	}
	var subject string
	switch kind {
	case "post.create":
		e = tx.QueryRowContext(ctx, `SELECT p.id::text,p.published_at FROM owned_text_posts p WHERE p.id=$1 AND p.deleted_at IS NULL AND p.published_at IS NOT NULL AND EXISTS(SELECT 1 FROM owned_text_post_audit a WHERE a.post_id=p.id AND a.actor_id=$2 AND a.action='create')`, source, actor).Scan(&subject, &v.at)
	case "post.react":
		e = tx.QueryRowContext(ctx, `SELECT r.post_id::text,r.created_at FROM owned_post_reactions r JOIN owned_text_posts p ON p.id=r.post_id WHERE r.id=$1 AND r.account_id=$2 AND p.deleted_at IS NULL AND p.published_at IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "post.bookmark":
		e = tx.QueryRowContext(ctx, `SELECT r.post_id::text,r.created_at FROM owned_post_bookmarks r JOIN owned_text_posts p ON p.id=r.post_id WHERE r.id=$1 AND r.account_id=$2 AND p.deleted_at IS NULL AND p.published_at IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "post.boost":
		e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.occurred_at FROM owned_post_progression_sources s WHERE s.id=$1 AND s.kind='post.boost' AND s.account_id=$2 AND s.published_at<=s.occurred_at AND s.action_publisher_id IS NOT NULL AND s.target_publisher_id IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "post.create_topical":
		e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.occurred_at FROM owned_post_progression_sources s WHERE s.id=$1 AND s.kind='post.create_topical' AND s.account_id=$2 AND s.action_publisher_id IS NULL AND s.published_at=s.occurred_at AND s.tag_id IS NOT NULL AND s.category_id IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "post.featured":
		e = tx.QueryRowContext(ctx, `SELECT s.id::text,s.occurred_at FROM owned_post_progression_sources s WHERE s.id=$1 AND s.kind='post.featured' AND s.account_id=$2 AND s.action_publisher_id IS NULL AND s.featured_rank BETWEEN 1 AND 5 AND s.published_at<=((s.featured_day+1)::timestamp AT TIME ZONE 'UTC')`, source, actor).Scan(&subject, &v.at)
	case "stellar.support_month":
		e = tx.QueryRowContext(ctx, `SELECT m.id::text,m.occurred_at FROM commerce_v1.stellar_support_months m JOIN commerce_v1.orders o ON o.id=m.order_id JOIN commerce_v1.subscriptions s ON s.id=m.subscription_id JOIN wallet_v1.transactions t ON t.id=m.transaction_id WHERE m.id=$1 AND m.account_id=$2 AND o.subscription_id=m.subscription_id AND o.actor_id=m.account_id AND s.account_id=m.account_id AND o.status=3 AND o.app_id IS NULL AND o.gift_id IS NULL AND o.document->>'app_identifier'='internal' AND o.product_identifier=s.identifier AND s.document->>'app_identifier' IS NULL AND lower(s.document->>'group_identifier')='solian.stellar' AND s.document->>'payment_method'='solian.wallet' AND COALESCE((s.document->>'is_testing')::boolean,false)=false AND COALESCE((s.document->>'is_free_trial')::boolean,false)=false AND t.id=o.transaction_id AND t.managed_by=o.id AND t.purpose='commerce' AND t.status=2 AND t.confirmed_at IS NOT NULL AND t.deleted_at IS NULL AND t.actor_id=o.actor_id AND t.payer_wallet_id=o.payer_wallet_id AND t.payee_wallet_id=o.payee_wallet_id AND t.currency=o.currency AND t.amount=o.amount AND m.payer_wallet_id=t.payer_wallet_id AND m.payee_wallet_id=t.payee_wallet_id AND m.currency=t.currency AND m.amount=t.amount AND m.confirmed_at=t.confirmed_at AND m.occurred_at=m.confirmed_at AND m.cycle_days=s.cycle_days AND m.credited_months=GREATEST(1,floor((m.cycle_days+2)::numeric/30)::integer) AND m.month_index BETWEEN 1 AND m.credited_months`, source, actor).Scan(&subject, &v.at)
	case "chat.use":
		e = tx.QueryRowContext(ctx, `SELECT m.id::text,m.created_at FROM chat_messages m JOIN chat_members s ON s.id=m.sender_id JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.id=$1 AND s.account_id=$2 AND m.type='text' AND m.deleted_at IS NULL AND r.deleted_at IS NULL AND r.owned_owner_id IS NOT NULL AND m.owned_request_hash IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "chatroom.join":
		e = tx.QueryRowContext(ctx, `SELECT m.chat_room_id::text,m.joined_at FROM chat_members m JOIN chat_rooms r ON r.id=m.chat_room_id WHERE m.id=$1 AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND m.deleted_at IS NULL AND NOT m.owned_banned AND r.deleted_at IS NULL AND r.owned_owner_id IS NOT NULL`, source, actor).Scan(&subject, &v.at)
	case "realm.join":
		e = tx.QueryRowContext(ctx, `SELECT m.realm_id::text,m.joined_at FROM owned_realm_members m JOIN owned_realms r ON r.id=m.realm_id WHERE m.id=$1 AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.leave_at IS NULL AND r.deleted_at IS NULL`, source, actor).Scan(&subject, &v.at)
	case "publisher.create":
		e = tx.QueryRowContext(ctx, `SELECT p.id::text,p.created_at FROM owned_publishers p WHERE p.id=$1 AND p.deleted_at IS NULL AND EXISTS(SELECT 1 FROM owned_accounts_audit a WHERE a.actor_id=$2 AND a.target_id=p.id AND a.action='publishers.create')`, source, actor).Scan(&subject, &v.at)
	case "relationship.friend_established":
		e = tx.QueryRowContext(ctx, `SELECT r.related_id::text,r.created_at FROM owned_social_effective_relationships r WHERE r.id=$1 AND r.account_id=$2 AND r.status>=100 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships reverse WHERE reverse.account_id=r.related_id AND reverse.related_id=r.account_id AND reverse.status>=100)`, source, actor).Scan(&subject, &v.at)
	case "account.active":
		e = tx.QueryRowContext(ctx, `SELECT account_id::text,occurred_at FROM owned_presence_activity_days WHERE id=$1 AND account_id=$2`, source, actor).Scan(&subject, &v.at)
	default:
		return v, progression.ErrUnsupported
	}
	if e != nil {
		return v, mapped(e)
	}
	if v.at.After(now) {
		return v, progression.ErrInvalid
	}
	if kind == "account.active" {
		loc, e := zone(ctx, tx, actor)
		if e != nil {
			return v, e
		}
		subject = v.at.In(loc).Format("2006-01-02")
	}
	v.key = kind + ":" + subject
	return v, nil
}
func (r *Repository) applyCanonical(ctx context.Context, tx *sql.Tx, actor, kind, sourceID string, now time.Time) error {
	v, e := sourceEvent(ctx, tx, actor, kind, sourceID, now)
	if e != nil {
		return e
	}
	res, e := tx.ExecContext(ctx, `INSERT INTO owned_progression_events(account_id,event_key,kind,source_id,occurred_at,created_at)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(account_id,event_key)DO NOTHING`, actor, v.key, v.kind, v.source, v.at, now)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil || n == 0 {
		return e
	}
	loc, e := zone(ctx, tx, actor)
	if e != nil {
		return e
	}
	for _, kind := range []string{"achievement", "quest"} {
		ds, e := definitions(ctx, tx, kind)
		if e != nil {
			return e
		}
		for _, d := range ds {
			if !available(d, v.at) || !matches(d, v, loc) {
				continue
			}
			key := ""
			if kind == "quest" {
				schedule := "daily"
				if d.Schedule != nil {
					schedule = d.Schedule.Repeatability
				}
				key, _ = Period(schedule, v.at, loc)
			}
			var p progress
			e = tx.QueryRowContext(ctx, `SELECT progress_count,current_streak,best_streak,last_progress_at,completed_at FROM owned_progression_progress WHERE account_id=$1 AND kind=$2 AND identifier=$3 AND period_key=$4 FOR UPDATE`, actor, kind, d.Identifier, key).Scan(&p.count, &p.streak, &p.best, &p.last, &p.completed)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if p.completed != nil {
				continue
			}
			advance(&p, d, v.at, loc, kind == "quest")
			if p.count >= d.TargetCount {
				completed := now
				p.completed = &completed
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_progress(account_id,kind,identifier,period_key,progress_count,current_streak,best_streak,last_progress_at,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)ON CONFLICT(account_id,kind,identifier,period_key)DO UPDATE SET progress_count=EXCLUDED.progress_count,current_streak=EXCLUDED.current_streak,best_streak=EXCLUDED.best_streak,last_progress_at=EXCLUDED.last_progress_at,completed_at=EXCLUDED.completed_at`, actor, kind, d.Identifier, key, p.count, p.streak, p.best, p.last, p.completed); e != nil {
				return e
			}
			if p.completed != nil {
				if e = complete(ctx, tx, actor, kind, key, v, d, now); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func matches(d progression.Definition, v canonicalEvent, loc *time.Location) bool {
	found := len(d.Trigger.Actions) == 0
	for _, a := range d.Trigger.Actions {
		if strings.EqualFold(a, v.kind) {
			found = true
		}
	}
	if !found {
		return false
	}
	for k, expected := range d.Trigger.MetaEquals {
		if !strings.EqualFold(v.meta[k], expected) {
			return false
		}
	}
	// The pinned catalog's night-owl title specifies a local-time predicate absent
	// from its generic trigger. Enforce it instead of awarding every post a badge.
	if d.IsSeedManaged && d.Identifier == "hidden-night-owl" && v.at.In(loc).Hour() >= 4 {
		return false
	}
	return true
}
func advance(p *progress, d progression.Definition, at time.Time, loc *time.Location, quest bool) {
	mode := d.Trigger.Mode
	if quest {
		mode = "count"
	}
	if p.last != nil && (mode == "streak" || mode == "daily_count") && at.Before(*p.last) {
		return
	}
	day := at.In(loc).Format("2006-01-02")
	lastDay := ""
	if p.last != nil {
		lastDay = p.last.In(loc).Format("2006-01-02")
	}
	switch mode {
	case "streak":
		if lastDay == day {
			return
		}
		old := p.streak
		p.streak = 1
		if p.last != nil && p.last.In(loc).AddDate(0, 0, 1).Format("2006-01-02") == day {
			p.streak = old + 1
		}
	case "daily_count":
		if lastDay != day {
			p.count = 0
		}
		p.count++
	default:
		p.count++
	}
	if mode == "streak" {
		if p.best < p.streak {
			p.best = p.streak
		}
		p.count = p.streak
	}
	if p.count > d.TargetCount {
		p.count = d.TargetCount
	}
	if p.last == nil || at.After(*p.last) {
		t := at
		p.last = &t
	}
}
func complete(ctx context.Context, tx *sql.Tx, actor, kind, key string, v canonicalEvent, d progression.Definition, now time.Time) error {
	id, e := newID()
	if e != nil {
		return e
	}
	token := kind + ":" + d.Identifier + ":" + actor + ":" + key
	raw, e := json.Marshal(d.Reward)
	if e != nil {
		return e
	}
	var badgeAt, experienceAt any
	if d.Reward.Badge != nil {
		if e = grantBadge(ctx, tx, actor, *d.Reward.Badge, now); e != nil {
			return e
		}
		badgeAt = now
	}
	if d.Reward.Experience > 0 {
		if e = insertXP(ctx, tx, actor, "progression", token, token, d.Reward.Experience, now); e != nil {
			return e
		}
		experienceAt = now
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_grants(id,account_id,kind,identifier,title,reward_token,source_event_id,period_key,reward,badge_granted_at,experience_granted_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`, id, actor, kind, d.Identifier, d.Title, token, v.key, key, raw, badgeAt, experienceAt, now); e != nil {
		return e
	}
	outbox, e := newID()
	if e != nil {
		return e
	}
	delivered := d.Reward
	delivered.SourcePoints = 0
	delivered.SourcePointsCurrency = nil
	payload, e := json.Marshal(progression.Completion{Kind: kind, Identifier: d.Identifier, Title: d.Title, PeriodKey: key, Reward: delivered})
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_progression_outbox(id,account_id,grant_id,packet_type,payload,created_at)VALUES($1,$2,$3,'progression.completed',$4,$5)`, outbox, actor, id, payload, now)
	return e
}
