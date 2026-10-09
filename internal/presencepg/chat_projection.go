// SPDX-License-Identifier: AGPL-3.0-only
package presencepg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/presence"
	"time"
)

// ChatStatusInTransaction exposes the same privacy, bilateral blocking,
// invisibility, and current lease/session projection used by account reads.
// The chat caller owns room visibility and authenticated viewer proof.
func ChatStatusInTransaction(ctx context.Context, tx *sql.Tx, viewer, owner string, now time.Time) (presence.Status, error) {
	if tx == nil || !authority.ValidID(owner) || viewer != "" && !authority.ValidID(viewer) {
		return presence.Status{}, presence.ErrInvalid
	}
	return projectStatus(ctx, tx, viewer, owner, now)
}

// ChatStatusesInTransaction observes all canonical presence inputs in one SQL
// statement after the caller's complete account/room locks and account hydration.
// The database observation clock is evaluated after any relation-lock wait, so
// an expiring lease or status cannot survive because an earlier batch member was
// hydrated before that wait. It exposes no session, activity or credential data.
func ChatStatusesInTransaction(ctx context.Context, tx *sql.Tx, viewer string, owners []string) (map[string]presence.Status, error) {
	if tx == nil || viewer != "" && !authority.ValidID(viewer) || len(owners) > 10000 {
		return nil, presence.ErrInvalid
	}
	for _, owner := range owners {
		if !authority.ValidID(owner) {
			return nil, presence.ErrInvalid
		}
	}
	rows, e := tx.QueryContext(ctx, `WITH point AS MATERIALIZED(SELECT clock_timestamp() AS now)
 SELECT a.id::text,a.created_at,
 COALESCE(pref.online_visibility,'public'),COALESCE(pref.last_seen_visibility,'friends'),COALESCE(pref.status_visibility,'public'),
 (EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.account_id=a.id AND f.related_id=NULLIF($1::text,'')::uuid AND f.status>=100)
  AND EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE f.related_id=a.id AND f.account_id=NULLIF($1::text,'')::uuid AND f.status>=100)),
 to_jsonb(custom),live.n,live.active,live.idle,seen.last_seen_at
 FROM owned_authority_accounts a CROSS JOIN point
 LEFT JOIN owned_presence_preferences pref ON pref.account_id=a.id
 LEFT JOIN LATERAL(SELECT s.* FROM owned_presence_statuses s WHERE s.account_id=a.id AND(s.cleared_at IS NULL OR s.cleared_at>point.now) ORDER BY s.created_at DESC,s.id LIMIT 1) custom ON true
 LEFT JOIN LATERAL(SELECT count(*) AS n,count(*)FILTER(WHERE p.idle_since IS NULL) AS active,max(p.idle_since) AS idle
 FROM owned_presence_sessions p JOIN owned_authority_sessions s ON s.id=p.session_id AND s.account_id=p.account_id
 WHERE p.account_id=a.id AND p.expires_at>point.now AND s.expires_at>point.now AND s.revoked_at IS NULL
 AND p.epoch=s.epoch AND p.account_version=a.version AND s.account_version=a.version) live ON true
 LEFT JOIN owned_presence_last_seen seen ON seen.account_id=a.id
 WHERE a.id=ANY($2::uuid[]) AND NOT a.disabled
 AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments u WHERE u.account_id=a.id AND u.kind='disable_account' AND(u.starts_at IS NULL OR u.starts_at<=point.now)AND(u.expires_at IS NULL OR u.expires_at>point.now))
 AND NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships f WHERE ((f.account_id=a.id AND f.related_id=NULLIF($1::text,'')::uuid)OR(f.related_id=a.id AND f.account_id=NULLIF($1::text,'')::uuid))AND f.status=-100)
 ORDER BY a.id`, viewer, owners)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]presence.Status{}
	for rows.Next() {
		var id string
		var created time.Time
		var pref presence.Preferences
		var friend bool
		var raw []byte
		var n, active int
		var idle, seen sql.NullTime
		if e = rows.Scan(&id, &created, &pref.OnlineVisibility, &pref.LastSeenVisibility, &pref.StatusVisibility, &friend, &raw, &n, &active, &idle, &seen); e != nil {
			return nil, e
		}
		label := "Offline"
		v := presence.Status{ID: id, AccountID: id, Label: &label, Meta: json.RawMessage(`null`), CreatedAt: created, UpdatedAt: created}
		allowed := func(s string) bool { return viewer == id || s == "public" || s == "friends" && friend }
		var actual presence.Status
		custom := len(raw) != 0 && string(raw) != "null"
		if custom {
			if e = json.Unmarshal(raw, &actual); e != nil {
				return nil, e
			}
			actual.IsCustomized = true
			if len(actual.Meta) == 0 {
				actual.Meta = json.RawMessage(`null`)
			}
		}
		invisible := custom && actual.Type == presence.Invisible
		if custom && allowed(pref.StatusVisibility) {
			v = actual
		}
		if invisible && viewer != id {
			v.Type = presence.Default
		}
		if allowed(pref.OnlineVisibility) && !invisible {
			v.IsOnline = n > 0
			v.IsIdle = n > 0 && active == 0
			if v.IsIdle && idle.Valid {
				v.IdleSince = &idle.Time
			}
		}
		if allowed(pref.LastSeenVisibility) && (!invisible || viewer == id) && seen.Valid {
			v.LastSeenAt = &seen.Time
		}
		if !v.IsCustomized && v.IsOnline {
			label = "Online"
		}
		out[id] = v
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return out, nil
}
