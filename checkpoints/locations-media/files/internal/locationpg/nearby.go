// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"sort"
	"time"
)

func (r *Repository) recheck(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if r.guard == nil {
		return nil
	}
	var e error
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	return e
}
func tokenDigest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func (r *Repository) nearbyRate(ctx context.Context, tx *sql.Tx, actor, action string, now time.Time) error {
	var last time.Time
	e := tx.QueryRowContext(ctx, `SELECT last_at FROM owned_nearby_limits WHERE account_id=$1 AND action=$2 FOR UPDATE`, actor, action).Scan(&last)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil && now.Sub(last) < 5*time.Second {
		return locations.ErrRateLimit
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_nearby_limits(account_id,action,last_at) VALUES($1,$2,$3) ON CONFLICT(account_id,action) DO UPDATE SET last_at=EXCLUDED.last_at`, actor, action, now)
	return e
}
func (r *Repository) IssuePresenceTokens(ctx context.Context, actor string, v locations.PresenceRequest) (locations.PresenceTokens, error) {
	out := locations.PresenceTokens{ServiceUUID: locations.NearbyServiceUUID, SlotDurationSec: locations.NearbySlotSeconds, Tokens: []locations.PresenceToken{}}
	if e := locations.ValidatePresence(v); e != nil {
		return out, e
	}
	tx, p, e := r.begin(ctx, actor, "nearby.presence.manage", true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// Presence cannot outlive the authenticated device session, even if copied.
	if !authority.ValidID(p.Session.ID) {
		return out, locations.ErrUnauthenticated
	}
	var sessionExpiry time.Time
	if e = tx.QueryRowContext(ctx, `SELECT expires_at FROM owned_authority_sessions WHERE id=$1 AND account_id=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR SHARE`, p.Session.ID, actor).Scan(&sessionExpiry); e != nil {
		return out, mapped(e)
	}
	now := r.now().UTC()
	if e = r.nearbyRate(ctx, tx, actor, "issue", now); e != nil {
		return out, e
	}
	discoverable, friendOnly := true, true
	if v.Discoverable != nil {
		discoverable = *v.Discoverable
	}
	if v.FriendOnly != nil {
		friendOnly = *v.FriendOnly
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_nearby_tokens WHERE account_id=$1 AND (device_id=$2 OR valid_to<=$3)`, actor, v.DeviceID, now); e != nil {
		return out, e
	}
	if !discoverable {
		if e = r.recheck(ctx, tx, actor, "nearby.presence.manage"); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	var devices int
	if e = tx.QueryRowContext(ctx, `SELECT count(DISTINCT device_id) FROM owned_nearby_tokens WHERE account_id=$1 AND valid_to>$2`, actor, now).Scan(&devices); e != nil {
		return out, e
	}
	if devices >= 10 {
		return out, locations.ErrConflict
	}
	count := v.PrefetchSlots
	if count == 0 {
		count = 3
	}
	slot := now.Unix() / locations.NearbySlotSeconds
	for i := 0; i < count; i++ {
		start := time.Unix((slot+int64(i))*locations.NearbySlotSeconds, 0).UTC()
		end := start.Add(locations.NearbySlotSeconds * time.Second)
		if end.After(sessionExpiry) {
			end = sessionExpiry
		}
		if !end.After(start) {
			break
		}
		b := make([]byte, 16)
		if _, e = rand.Read(b); e != nil {
			return out, e
		}
		token := hex.EncodeToString(b)
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_nearby_tokens(token_hash,account_id,session_id,device_id,slot,valid_from,valid_to,friend_only,capabilities) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, tokenDigest(token), actor, p.Session.ID, v.DeviceID, slot+int64(i), start, end, friendOnly, v.Capabilities); e != nil {
			return out, e
		}
		out.Tokens = append(out.Tokens, locations.PresenceToken{Slot: slot + int64(i), Token: token, ValidFrom: start, ValidTo: end})
	}
	if e = r.recheck(ctx, tx, actor, "nearby.presence.manage"); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) ResolveNearby(ctx context.Context, actor string, v []locations.Observation) ([]locations.NearbyPeer, error) {
	now := r.now().UTC()
	if e := locations.ValidateObservations(v, now); e != nil {
		return nil, e
	}
	tx, _, e := r.begin(ctx, actor, "nearby.resolve", true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = r.nearbyRate(ctx, tx, actor, "resolve", now); e != nil {
		return nil, e
	}
	peers := map[string]locations.NearbyPeer{}
	for _, o := range v {
		var owner string
		var friendOnly bool
		var validFrom, validTo time.Time
		e = tx.QueryRowContext(ctx, `SELECT t.account_id::text,t.friend_only,t.valid_from,t.valid_to FROM owned_nearby_tokens t JOIN owned_authority_accounts a ON a.id=t.account_id JOIN owned_authority_sessions s ON s.id=t.session_id AND s.account_id=t.account_id WHERE t.token_hash=$1 AND t.slot=$2 AND t.valid_from<=clock_timestamp() AND t.valid_to>clock_timestamp() AND NOT a.disabled AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()`, tokenDigest(o.Token), o.Slot).Scan(&owner, &friendOnly, &validFrom, &validTo)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if owner == actor {
			continue
		}
		blocked, friend, e := socialState(ctx, tx, actor, owner)
		if e != nil {
			return nil, e
		}
		if blocked || friendOnly && !friend {
			continue
		}
		a, e := account(ctx, tx, owner)
		if errors.Is(e, locations.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		seen := now
		if o.LastSeenAt != nil {
			seen = *o.LastSeenAt
		}
		if seen.Before(validFrom) || !seen.Before(validTo) {
			continue
		}
		visibility := "discoverable"
		if friendOnly {
			visibility = "friend_only"
		}
		peer := locations.NearbyPeer{UserID: owner, DisplayName: a.Nick, IsFriend: friend, CanInvite: !friend, Visibility: visibility, LastSeenAt: seen}
		if peer.DisplayName == "" {
			peer.DisplayName = a.Name
		}
		if old, ok := peers[owner]; !ok || seen.After(old.LastSeenAt) {
			peers[owner] = peer
		}
	}
	// All privacy and liveness predicates are refreshed after every potentially
	// blocking lookup. A token never grants location, device, or session details.
	out := []locations.NearbyPeer{}
	for id, peer := range peers {
		blocked, friend, e := socialState(ctx, tx, actor, id)
		if e != nil {
			return nil, e
		}
		if blocked || peer.Visibility == "friend_only" && !friend {
			continue
		}
		live := false
		for _, o := range v {
			var ok bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_nearby_tokens t JOIN owned_authority_accounts a ON a.id=t.account_id JOIN owned_authority_sessions s ON s.id=t.session_id WHERE t.token_hash=$1 AND t.slot=$2 AND t.account_id=$3 AND t.valid_from<=clock_timestamp() AND t.valid_to>clock_timestamp() AND NOT a.disabled AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND (NOT t.friend_only OR $4))`, tokenDigest(o.Token), o.Slot, id, friend).Scan(&ok)
			if e != nil {
				return nil, e
			}
			live = live || ok
		}
		if live {
			peer.IsFriend = friend
			peer.CanInvite = !friend
			out = append(out, peer)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	if e = r.recheck(ctx, tx, actor, "nearby.resolve"); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
