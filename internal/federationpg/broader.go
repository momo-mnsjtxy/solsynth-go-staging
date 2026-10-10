// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"bytes"
	"context"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

func (r *Repository) RemoteProfile(ctx context.Context, id string) (f.RemoteProfile, error) {
	if !authority.ValidID(id) {
		return f.RemoteProfile{}, f.ErrNotFound
	}
	var result []byte
	var observed time.Time
	e := r.db.QueryRowContext(ctx, `SELECT result,finished_at FROM owned_federation_discoveries WHERE id=$1::uuid AND status='succeeded'`, id).Scan(&result, &observed)
	if e != nil {
		return f.RemoteProfile{}, mapped(e)
	}
	var d f.DiscoveredActor
	if json.Unmarshal(result, &d) != nil {
		return f.RemoteProfile{}, f.ErrUnavailable
	}
	return f.RemoteProfile{ID: id, Actor: d.ID, Handle: strings.TrimPrefix(d.Resource, "acct:"), Type: d.Type, Name: d.Name, Summary: d.Summary, Inbox: d.Inbox, Provenance: d.Observation, ObservedAt: observed}, nil
}
func (r *Repository) RemoteProfileByHandle(ctx context.Context, handle string) (f.RemoteProfile, error) {
	handle = strings.TrimSpace(handle)
	if len(handle) < 3 || len(handle) > 512 || strings.Count(handle, "@") != 1 {
		return f.RemoteProfile{}, f.ErrInvalid
	}
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id::text FROM owned_federation_discoveries WHERE status='succeeded' AND lower(resource)=lower('acct:'||$1) ORDER BY finished_at DESC,id DESC LIMIT 1`, handle).Scan(&id)
	if err != nil {
		return f.RemoteProfile{}, mapped(err)
	}
	return r.RemoteProfile(ctx, id)
}
func (r *Repository) SearchRemoteProfiles(ctx context.Context, q string, offset, take int) ([]f.RemoteProfile, error) {
	q = strings.TrimSpace(q)
	if len(q) < 1 || len(q) > 256 || page(offset, take) != nil {
		return nil, f.ErrInvalid
	}
	rows, e := r.db.QueryContext(ctx, `SELECT DISTINCT ON(actor_uri) id::text,result,finished_at FROM owned_federation_discoveries WHERE status='succeeded' AND(resource ILIKE '%'||$1||'%' OR result->>'name' ILIKE '%'||$1||'%') ORDER BY actor_uri,finished_at DESC,id OFFSET $2 LIMIT $3`, q, offset, take)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []f.RemoteProfile{}
	for rows.Next() {
		var id string
		var b []byte
		var at time.Time
		if e = rows.Scan(&id, &b, &at); e != nil {
			return nil, e
		}
		var d f.DiscoveredActor
		if json.Unmarshal(b, &d) != nil {
			continue
		}
		out = append(out, f.RemoteProfile{ID: id, Actor: d.ID, Handle: strings.TrimPrefix(d.Resource, "acct:"), Type: d.Type, Name: d.Name, Summary: d.Summary, Inbox: d.Inbox, Provenance: d.Observation, ObservedAt: at})
	}
	return out, rows.Err()
}
func (r *Repository) RemotePosts(ctx context.Context, actor, ref, profileID string, offset, take int) ([]f.Note, error) {
	if page(offset, take) != nil {
		return nil, f.ErrInvalid
	}
	profile, e := r.RemoteProfile(ctx, profileID)
	if e != nil {
		return nil, e
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT object FROM owned_federation_objects WHERE publisher_id=$1 AND actor_uri=$2 AND NOT deleted ORDER BY updated_at DESC,object_id DESC OFFSET $3 LIMIT $4`, p.id, profile.Actor, offset, take)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []f.Note{}
	for rows.Next() {
		var b []byte
		var n f.Note
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if json.Unmarshal(b, &n) == nil {
			out = append(out, n)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.Permission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) Availability(ctx context.Context, actor string) (f.Availability, error) {
	if r.guard == nil || !authority.ValidID(actor) {
		return f.Availability{}, f.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return f.Availability{}, e
	}
	defer tx.Rollback()
	var locked string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 AND NOT disabled FOR UPDATE`, actor).Scan(&locked); e != nil {
		return f.Availability{}, mapped(e)
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ReadPermission); e != nil {
		return f.Availability{}, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL JOIN owned_federation_publishers f ON f.publisher_id=p.id AND f.enabled WHERE p.type=0 AND p.deleted_at IS NULL ORDER BY p.id`, actor)
	if e != nil {
		return f.Availability{}, e
	}
	defer rows.Close()
	out := f.Availability{Publishers: []string{}}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return out, e
		}
		out.Publishers = append(out.Publishers, id)
	}
	out.Enabled = len(out.Publishers) > 0
	if e = rows.Err(); e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ReadPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) RelationshipView(ctx context.Context, actor, ref, remote string) (f.RelationshipView, error) {
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return f.RelationshipView{}, e
	}
	defer tx.Rollback()
	k, e := r.remoteWithQuery(ctx, tx, remote)
	if e != nil {
		return f.RelationshipView{}, e
	}
	out := f.RelationshipView{RemoteActor: k.Actor}
	rows, e := tx.QueryContext(ctx, `SELECT direction,status FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, p.id, k.Actor)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var d, s string
		if e = rows.Scan(&d, &s); e != nil {
			rows.Close()
			return out, e
		}
		if d == "out" {
			out.Following = s == "accepted"
			out.Pending = s == "pending"
		} else {
			out.FollowedBy = s == "accepted"
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_blocks WHERE publisher_id=$1 AND actor_uri=$2)`, p.id, k.Actor).Scan(&out.Blocked); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

type realmIdentity struct {
	id, slug, name, description, owner, publisher, actor, key string
	created                                                   time.Time
	enabled                                                   bool
}

func (r *Repository) realm(ctx context.Context, q queryer, slug string, enabled bool) (realmIdentity, error) {
	var x realmIdentity
	if !f.ValidName(slug) {
		return x, f.ErrNotFound
	}
	e := q.QueryRowContext(ctx, `SELECT x.id::text,x.slug,x.name,x.description,x.account_id::text,x.created_at,COALESCE(fr.enabled,false),COALESCE(fr.actor_uri,''),COALESCE(fr.key_id,''),COALESCE((SELECT p.id::text FROM owned_publishers p WHERE p.realm_id=x.id AND p.type=1 AND p.deleted_at IS NULL ORDER BY p.id LIMIT 1),'') FROM owned_realms x LEFT JOIN owned_federation_realms fr ON fr.realm_id=x.id WHERE lower(x.slug)=lower($1) AND x.is_public AND x.is_community AND x.deleted_at IS NULL`, slug).Scan(&x.id, &x.slug, &x.name, &x.description, &x.owner, &x.created, &x.enabled, &x.actor, &x.key, &x.publisher)
	if e != nil {
		return x, mapped(e)
	}
	if enabled && (!x.enabled || x.actor != r.config.Origin+"/activitypub/realms/"+url.PathEscape(x.slug) || x.key != x.actor+"#main-key" || x.publisher == "") {
		return x, f.ErrNotFound
	}
	return x, nil
}
func (r *Repository) realmKey(ctx context.Context, x realmIdentity) (f.PublicKey, error) {
	if r.keys == nil {
		return f.PublicKey{}, f.ErrUnavailable
	}
	signer, e := r.keys.LocalKey(ctx, x.publisher)
	if e != nil {
		return f.PublicKey{}, f.ErrUnavailable
	}
	pem, e := f.PEM(signer)
	if e != nil {
		return f.PublicKey{}, e
	}
	return f.PublicKey{ID: x.key, Owner: x.actor, PEM: pem}, nil
}
func (r *Repository) RealmActor(ctx context.Context, slug string) (f.Actor, error) {
	x, e := r.realm(ctx, r.db, slug, true)
	if e != nil {
		return f.Actor{}, e
	}
	k, e := r.realmKey(ctx, x)
	if e != nil {
		return f.Actor{}, e
	}
	return f.Actor{Context: []string{f.Context, "https://w3id.org/security/v1"}, ID: x.actor, Type: "Group", Username: x.slug, Name: x.name, Summary: html.EscapeString(x.description), Inbox: x.actor + "/inbox", Outbox: x.actor + "/outbox", Followers: x.actor + "/followers", Published: x.created, PublicKey: k, URL: r.config.Origin + "/realms/" + url.PathEscape(x.slug), Discoverable: true}, nil
}
func (r *Repository) RealmCollection(ctx context.Context, slug, kind string, offset, take int) (f.Collection, error) {
	if page(offset, take) != nil {
		return f.Collection{}, f.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return f.Collection{}, e
	}
	defer tx.Rollback()
	x, e := r.realm(ctx, tx, slug, true)
	if e != nil {
		return f.Collection{}, e
	}
	id := x.actor + "/" + kind
	var total int64
	items := []any{}
	switch kind {
	case "followers":
		e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_realm_relationships WHERE realm_id=$1 AND status='accepted'`, x.id).Scan(&total)
	case "outbox":
		items, total, e = r.realmOutbox(ctx, tx, x, offset, take)
	default:
		return f.Collection{}, f.ErrNotFound
	}
	if e != nil {
		return f.Collection{}, e
	}
	if e = tx.Commit(); e != nil {
		return f.Collection{}, e
	}
	if kind == "followers" {
		return f.Collection{Context: f.Context, ID: id, Type: "OrderedCollection", Total: total}, nil
	}
	return pageCollection(id, total, offset, take, items), nil
}
func (r *Repository) realmOutbox(ctx context.Context, q queryer, x realmIdentity, offset, take int) ([]any, int64, error) {
	var announced int64
	if e := q.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_realm_outbox WHERE realm_id=$1 AND withdrawn_at IS NULL`, x.id).Scan(&announced); e != nil {
		return nil, 0, e
	}
	type timed struct {
		at    time.Time
		id    string
		value any
	}
	all := []timed{}
	limit := offset + take
	rows, e := q.QueryContext(ctx, `SELECT activity,created_at,activity_id FROM owned_federation_realm_outbox WHERE realm_id=$1 AND withdrawn_at IS NULL ORDER BY created_at DESC,activity_id DESC LIMIT $2`, x.id, limit)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var body []byte
		var at time.Time
		var id string
		if e = rows.Scan(&body, &at, &id); e != nil {
			rows.Close()
			return nil, 0, e
		}
		var a f.Activity
		if json.Unmarshal(body, &a) != nil {
			rows.Close()
			return nil, 0, f.ErrUnavailable
		}
		all = append(all, timed{at, id, a})
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, 0, e
	}
	rows.Close()
	sort.Slice(all, func(i, j int) bool {
		if all[i].at.Equal(all[j].at) {
			return all[i].id > all[j].id
		}
		return all[i].at.After(all[j].at)
	})
	end := min(offset+take, len(all))
	if offset > end {
		offset = end
	}
	items := make([]any, 0, end-offset)
	for _, v := range all[offset:end] {
		items = append(items, v.value)
	}
	return items, announced, nil
}
func (r *Repository) SetRealmFederation(ctx context.Context, actor, realmID string, enabled bool) error {
	if !authority.ValidID(actor) || !authority.ValidID(realmID) || r.guard == nil {
		return f.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var slug, owner string
	var community, public bool
	e = tx.QueryRowContext(ctx, `SELECT slug,account_id::text,is_community,is_public FROM owned_realms WHERE id=$1 AND deleted_at IS NULL`, realmID).Scan(&slug, &owner, &community, &public)
	if e != nil {
		return mapped(e)
	}
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, actor, owner)
	if e != nil {
		return e
	}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var lockedSlug, lockedOwner string
	var lockedCommunity, lockedPublic bool
	if e = tx.QueryRowContext(ctx, `SELECT slug,account_id::text,is_community,is_public FROM owned_realms WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, realmID).Scan(&lockedSlug, &lockedOwner, &lockedCommunity, &lockedPublic); e != nil {
		return mapped(e)
	}
	if lockedOwner != owner {
		return f.ErrForbidden
	}
	slug, community, public = lockedSlug, lockedCommunity, lockedPublic
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.RealmPermission); e != nil {
		return e
	}
	var role int
	e = tx.QueryRowContext(ctx, `SELECT role FROM owned_realm_members WHERE realm_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND leave_at IS NULL FOR UPDATE`, realmID, actor).Scan(&role)
	if e != nil || role < 100 || !community || !public {
		return f.ErrForbidden
	}
	var pid string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE realm_id=$1 AND type=1 AND deleted_at IS NULL ORDER BY id LIMIT 1 FOR UPDATE`, realmID).Scan(&pid); e != nil {
		return mapped(e)
	}
	uri := r.config.Origin + "/activitypub/realms/" + url.PathEscape(slug)
	if enabled {
		if _, e = r.keys.LocalKey(ctx, pid); e != nil {
			return f.ErrUnavailable
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_realms(realm_id,enabled,actor_uri,key_id,updated_at)VALUES($1,$2,$3,$4,$5)ON CONFLICT(realm_id)DO UPDATE SET enabled=EXCLUDED.enabled,updated_at=EXCLUDED.updated_at`, realmID, enabled, uri, uri+"#main-key", r.config.Clock())
	if e != nil {
		return e
	}
	if !enabled {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$2,lease_token=NULL,lease_until=NULL WHERE realm_id=$1 AND status IN('pending','inflight') AND dispatch_started_at IS NULL`, realmID, r.config.Clock()); e != nil {
			return e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.RealmPermission); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_admin_audit(account_id,action,target,occurred_at)VALUES($1,'realm.settings',$2,$3)`, actor, realmID, r.config.Clock())
	if e != nil {
		return e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.RealmPermission); e != nil {
		return e
	}
	return tx.Commit()
}

func moderationMatch(rule f.ModerationRule, actor, content string) bool {
	candidate := content
	if rule.Kind == "actor" {
		candidate = actor
	} else if rule.Kind == "domain" {
		u, e := url.Parse(actor)
		if e != nil {
			return false
		}
		candidate = strings.ToLower(u.Hostname())
	}
	if rule.Regex {
		re, e := regexp.Compile(rule.Pattern)
		return e == nil && re.MatchString(candidate)
	}
	if rule.Kind == "domain" {
		if !validDNSName(rule.Pattern) {
			return false
		}
		pattern := rule.Pattern
		return candidate == pattern || strings.HasSuffix(candidate, "."+pattern)
	}
	return strings.Contains(strings.ToLower(candidate), strings.ToLower(rule.Pattern))
}

func validDNSName(name string) bool {
	if name == "" || len(name) > 253 || name != strings.ToLower(name) || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c < 'a' || c > 'z' {
				if c < '0' || c > '9' {
					if c != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}
func (r *Repository) moderationWith(ctx context.Context, q queryer, actor, content string) (f.ModerationDecision, error) {
	rows, e := q.QueryContext(ctx, `SELECT id::text,name,description,kind,action,pattern,is_regex,enabled,priority,expires_at,created_at,updated_at FROM owned_federation_moderation_rules WHERE enabled AND(expires_at IS NULL OR expires_at>$1)ORDER BY priority DESC,id`, r.config.Clock())
	if e != nil {
		return f.ModerationDecision{}, e
	}
	defer rows.Close()
	for rows.Next() {
		var x f.ModerationRule
		if e = rows.Scan(&x.ID, &x.Name, &x.Description, &x.Kind, &x.Action, &x.Pattern, &x.Regex, &x.Enabled, &x.Priority, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt); e != nil {
			return f.ModerationDecision{}, e
		}
		if moderationMatch(x, actor, content) {
			return f.ModerationDecision{Allowed: x.Action == "allow" || x.Action == "media_reject", Action: x.Action, RuleID: x.ID}, nil
		}
	}
	return f.ModerationDecision{Allowed: true}, rows.Err()
}
func (r *Repository) CheckModeration(ctx context.Context, account, actor, content string) (f.ModerationDecision, error) {
	if len(actor) > 2048 || len(content) > 262144 {
		return f.ModerationDecision{}, f.ErrInvalid
	}
	tx, e := r.adminTx(ctx, account, f.ModerationPermission)
	if e != nil {
		return f.ModerationDecision{}, e
	}
	defer tx.Rollback()
	out, e := r.moderationWith(ctx, tx, actor, content)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, account, f.ModerationPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) adminTx(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if r.guard == nil || !authority.ValidID(actor) {
		return nil, f.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	var id string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1 AND NOT disabled FOR UPDATE`, actor).Scan(&id); e != nil {
		tx.Rollback()
		return nil, mapped(e)
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func scanRule(row interface{ Scan(...any) error }) (x f.ModerationRule, e error) {
	e = row.Scan(&x.ID, &x.Name, &x.Description, &x.Kind, &x.Action, &x.Pattern, &x.Regex, &x.Enabled, &x.Priority, &x.ExpiresAt, &x.CreatedAt, &x.UpdatedAt)
	return
}

const ruleFields = `id::text,name,description,kind,action,pattern,is_regex,enabled,priority,expires_at,created_at,updated_at`

func (r *Repository) ModerationRules(ctx context.Context, actor string, offset, take int) ([]f.ModerationRule, error) {
	if page(offset, take) != nil {
		return nil, f.ErrInvalid
	}
	tx, e := r.adminTx(ctx, actor, f.ModerationPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+ruleFields+` FROM owned_federation_moderation_rules ORDER BY priority,id OFFSET $1 LIMIT $2`, offset, take)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []f.ModerationRule{}
	for rows.Next() {
		x, e := scanRule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) ModerationRule(ctx context.Context, actor, id string) (f.ModerationRule, error) {
	if !authority.ValidID(id) {
		return f.ModerationRule{}, f.ErrNotFound
	}
	tx, e := r.adminTx(ctx, actor, f.ModerationPermission)
	if e != nil {
		return f.ModerationRule{}, e
	}
	defer tx.Rollback()
	x, e := scanRule(tx.QueryRowContext(ctx, `SELECT `+ruleFields+` FROM owned_federation_moderation_rules WHERE id=$1`, id))
	if e != nil {
		return x, mapped(e)
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return x, e
	}
	return x, tx.Commit()
}
func validRule(x f.ModerationRule) error {
	if len(strings.TrimSpace(x.Name)) < 1 || len(x.Name) > 256 || len(x.Description) > 4096 || len(x.Pattern) < 1 || len(x.Pattern) > 2048 || x.Priority < -100000 || x.Priority > 100000 || !f.Has([]string{"domain", "actor", "keyword"}, x.Kind) || !f.Has([]string{"allow", "silence", "suspend", "reject", "media_reject"}, x.Action) {
		return f.ErrInvalid
	}
	if x.Regex {
		if len(x.Pattern) > 512 {
			return f.ErrInvalid
		}
		if _, e := regexp.Compile(x.Pattern); e != nil {
			return f.ErrInvalid
		}
	}
	if x.Kind == "domain" && x.Regex {
		return f.ErrInvalid
	}
	if x.Kind == "domain" && !validDNSName(x.Pattern) {
		return f.ErrInvalid
	}
	return nil
}
func (r *Repository) PutModerationRule(ctx context.Context, actor string, x f.ModerationRule) (f.ModerationRule, error) {
	if e := validRule(x); e != nil {
		return x, e
	}
	tx, e := r.adminTx(ctx, actor, f.ModerationPermission)
	if e != nil {
		return x, e
	}
	defer tx.Rollback()
	now := r.config.Clock()
	if x.ID == "" {
		x.ID, e = newID()
		if e != nil {
			return x, e
		}
		x.CreatedAt = now
	} else if !authority.ValidID(x.ID) {
		return x, f.ErrInvalid
	}
	x.UpdatedAt = now
	x, e = scanRule(tx.QueryRowContext(ctx, `INSERT INTO owned_federation_moderation_rules(id,name,description,kind,action,pattern,is_regex,enabled,priority,expires_at,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)ON CONFLICT(id)DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,kind=EXCLUDED.kind,action=EXCLUDED.action,pattern=EXCLUDED.pattern,is_regex=EXCLUDED.is_regex,enabled=EXCLUDED.enabled,priority=EXCLUDED.priority,expires_at=EXCLUDED.expires_at,updated_at=EXCLUDED.updated_at RETURNING `+ruleFields, x.ID, x.Name, x.Description, x.Kind, x.Action, x.Pattern, x.Regex, x.Enabled, x.Priority, x.ExpiresAt, now))
	if e != nil {
		return x, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return x, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_admin_audit(account_id,action,target,occurred_at)VALUES($1,'moderation.put',$2,$3)`, actor, x.ID, now)
	if e != nil {
		return x, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return x, e
	}
	return x, tx.Commit()
}
func (r *Repository) DeleteModerationRule(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return f.ErrNotFound
	}
	tx, e := r.adminTx(ctx, actor, f.ModerationPermission)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `DELETE FROM owned_federation_moderation_rules WHERE id=$1`, id)
	if e != nil {
		return e
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return f.ErrNotFound
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_admin_audit(account_id,action,target,occurred_at)VALUES($1,'moderation.delete',$2,$3)`, actor, id, r.config.Clock())
	if e != nil {
		return e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.ModerationPermission); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) keyInfo(ctx context.Context, q queryer, id string) (f.KeyInfo, error) {
	var x f.KeyInfo
	var handle string
	e := q.QueryRowContext(ctx, `SELECT p.id::text,CASE WHEN p.type=0 THEN f.actor_uri ELSE fr.actor_uri END,CASE WHEN p.type=0 THEN f.key_id ELSE fr.key_id END,CASE WHEN p.type=0 THEN f.identity_handle ELSE p.name END FROM owned_publishers p LEFT JOIN owned_authority_accounts a ON a.id=p.account_id LEFT JOIN owned_realms realm ON realm.id=p.realm_id LEFT JOIN owned_authority_accounts owner ON owner.id=realm.account_id LEFT JOIN owned_federation_publishers f ON f.publisher_id=p.id LEFT JOIN owned_federation_realms fr ON fr.realm_id=p.realm_id WHERE p.id=$1 AND p.deleted_at IS NULL AND ((p.type=0 AND a.id IS NOT NULL AND NOT a.disabled AND f.enabled AND f.actor_uri IS NOT NULL AND f.key_id=f.actor_uri||'#main-key')OR(p.type=1 AND realm.is_public AND realm.is_community AND realm.deleted_at IS NULL AND NOT owner.disabled AND fr.enabled AND fr.actor_uri IS NOT NULL AND fr.key_id=fr.actor_uri||'#main-key'))`, id).Scan(&x.ActorID, &x.ActorURI, &x.KeyID, &handle)
	if e != nil {
		return x, mapped(e)
	}
	signer, e := r.keys.LocalKey(ctx, id)
	if e == nil {
		pem, pe := f.PEM(signer)
		if pe == nil {
			public, parseErr := f.ParsePublicPEM(pem)
			if parseErr == nil {
				x.Fingerprint, parseErr = f.KeyFingerprint(public)
			}
			x.HasKey = parseErr == nil
		}
	}
	return x, nil
}
func (r *Repository) KeyInfo(ctx context.Context, actor, id string) (f.KeyInfo, error) {
	if !authority.ValidID(id) {
		return f.KeyInfo{}, f.ErrNotFound
	}
	tx, e := r.adminTx(ctx, actor, f.KeyAdminPermission)
	if e != nil {
		return f.KeyInfo{}, e
	}
	defer tx.Rollback()
	x, e := r.keyInfo(ctx, tx, id)
	if e != nil {
		return x, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return x, e
	}
	return x, tx.Commit()
}
func (r *Repository) KeyAudit(ctx context.Context, actor string, offset, take int) ([]f.AdminAudit, error) {
	if page(offset, take) != nil {
		return nil, f.ErrInvalid
	}
	tx, e := r.adminTx(ctx, actor, f.KeyAdminPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,account_id::text,action,target,occurred_at FROM owned_federation_admin_audit WHERE action LIKE 'key.%' ORDER BY id DESC OFFSET $1 LIMIT $2`, offset, take)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []f.AdminAudit{}
	for rows.Next() {
		var x f.AdminAudit
		if e = rows.Scan(&x.ID, &x.AccountID, &x.Action, &x.Target, &x.OccurredAt); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) KeyStatistics(ctx context.Context, actor string) (f.KeyStatistics, error) {
	tx, e := r.adminTx(ctx, actor, f.KeyAdminPermission)
	if e != nil {
		return f.KeyStatistics{}, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id FROM(SELECT publisher_id::text id FROM owned_federation_publishers WHERE actor_uri IS NOT NULL UNION SELECT p.id::text FROM owned_federation_realms fr JOIN owned_publishers p ON p.realm_id=fr.realm_id AND p.type=1 AND p.deleted_at IS NULL WHERE fr.enabled)actors ORDER BY id`)
	if e != nil {
		return f.KeyStatistics{}, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return f.KeyStatistics{}, e
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := f.KeyStatistics{LocalActors: int64(len(ids))}
	for _, id := range ids {
		if _, e = r.keys.LocalKey(ctx, id); e == nil {
			out.AvailableKeys++
		} else {
			out.MissingKeys++
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

type keyCommandWork struct {
	f.KeyCommand
	requester, actorURI, keyID, token, session string
	version, epoch                             int64
	attempts                                   int
	lease                                      time.Time
}

func scanKeyCommand(row interface{ Scan(...any) error }) (x f.KeyCommand, e error) {
	var finished sql.NullTime
	e = row.Scan(&x.ID, &x.ActorID, &x.Action, &x.Status, &x.ExpectedFingerprint, &x.ResultFingerprint, &x.CreatedAt, &finished)
	if finished.Valid {
		x.FinishedAt = &finished.Time
	}
	return
}

func (r *Repository) SubmitKeyCommand(ctx context.Context, actor, id, action, operation string) (f.KeyCommand, error) {
	if !authority.ValidID(id) || !authority.ValidID(operation) || (action != "ensure" && action != "rotate") {
		return f.KeyCommand{}, f.ErrInvalid
	}
	if _, ok := r.keys.(f.IdempotentKeyAdministrator); !ok {
		return f.KeyCommand{}, f.ErrUnavailable
	}
	tx, e := r.adminTx(ctx, actor, f.KeyAdminPermission)
	if e != nil {
		return f.KeyCommand{}, e
	}
	defer tx.Rollback()
	target, e := r.keyInfo(ctx, tx, id)
	if e != nil {
		return f.KeyCommand{}, e
	}
	proof, e := r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission)
	if e != nil {
		return f.KeyCommand{}, e
	}
	if action == "rotate" && !target.HasKey {
		return f.KeyCommand{}, f.ErrUnavailable
	}
	now := r.config.Clock()
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_key_commands(id,requester_id,target_id,target_actor_uri,target_key_id,action,expected_fingerprint,requester_version,requester_session,requester_epoch,status,next_attempt,created_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending',$11,$11)ON CONFLICT(id)DO NOTHING`, operation, actor, id, target.ActorURI, target.KeyID, action, target.Fingerprint, proof.Account.Version, proof.Session.ID, proof.Session.AuthorizationEpoch, now)
	if e != nil {
		return f.KeyCommand{}, e
	}
	out, e := scanKeyCommand(tx.QueryRowContext(ctx, `SELECT id::text,target_id::text,action,status,expected_fingerprint,COALESCE(result_fingerprint,''),created_at,finished_at FROM owned_federation_key_commands WHERE id=$1 AND requester_id=$2 AND target_id=$3 AND action=$4 AND target_actor_uri=$5 AND target_key_id=$6`, operation, actor, id, action, target.ActorURI, target.KeyID))
	if e != nil {
		return out, f.ErrReplay
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

func (r *Repository) claimKeyCommand(ctx context.Context) (keyCommandWork, error) {
	var w keyCommandWork
	token, e := newID()
	if e != nil {
		return w, e
	}
	_, e = r.db.ExecContext(ctx, `WITH expired AS(SELECT id FROM owned_federation_key_commands WHERE status IN('pending','inflight') AND attempts>=LEAST(12,$1) AND(lease_until IS NULL OR lease_until<=clock_timestamp()) FOR UPDATE SKIP LOCKED LIMIT 100)UPDATE owned_federation_key_commands c SET status='dead',finished_at=clock_timestamp(),lease_token=NULL,lease_until=NULL FROM expired WHERE c.id=expired.id`, r.config.MaxAttempts)
	if e != nil {
		return w, e
	}
	e = r.db.QueryRowContext(ctx, `WITH candidate AS(SELECT id FROM owned_federation_key_commands WHERE(status='pending' AND next_attempt<=$1)OR(status='inflight' AND lease_until<=clock_timestamp())ORDER BY next_attempt,id FOR UPDATE SKIP LOCKED LIMIT 1)UPDATE owned_federation_key_commands c SET status='inflight',attempts=attempts+1,lease_token=$2,lease_until=clock_timestamp()+($3::bigint*interval '1 microsecond')FROM candidate x WHERE c.id=x.id RETURNING c.id::text,c.target_id::text,c.action,c.status,c.expected_fingerprint,COALESCE(c.result_fingerprint,''),c.created_at,c.finished_at,c.requester_id::text,c.target_actor_uri,c.target_key_id,c.requester_session::text,c.requester_version,c.requester_epoch,c.attempts,c.lease_until`, r.config.Clock(), token, (2*r.config.DeliveryTimeout).Microseconds()).Scan(&w.ID, &w.ActorID, &w.Action, &w.Status, &w.ExpectedFingerprint, &w.ResultFingerprint, &w.CreatedAt, new(sql.NullTime), &w.requester, &w.actorURI, &w.keyID, &w.session, &w.version, &w.epoch, &w.attempts, &w.lease)
	w.token = token
	return w, e
}
func (r *Repository) keyCommandAllowed(ctx context.Context, tx *sql.Tx, w keyCommandWork) (bool, error) {
	guard, ok := r.guard.(storedDiscoveryGuard)
	if !ok {
		return false, f.ErrUnavailable
	}
	eligible, e := guard.StoredSessionEligible(ctx, r.db, tx, w.requester, w.session, w.version, w.epoch, f.KeyAdminPermission)
	if e != nil || !eligible {
		return eligible, e
	}
	info, e := r.keyInfo(ctx, tx, w.ActorID)
	if e != nil {
		return false, nil
	}
	if info.ActorURI != w.actorURI || info.KeyID != w.keyID {
		return false, nil
	}
	// The custody provider performs the expected-fingerprint CAS. Do not reject
	// a retry merely because the first external call succeeded before its SQL
	// acknowledgement; replaying the same operation ID must return that result.
	return true, nil
}
func (r *Repository) finishKeyCommand(ctx context.Context, w keyCommandWork, result string, externalFingerprint string) error {
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var token, recorded string
	e = tx.QueryRowContext(ctx, `SELECT lease_token::text,COALESCE(result_fingerprint,'') FROM owned_federation_key_commands WHERE id=$1 AND status='inflight' AND attempts=$2 AND lease_until>clock_timestamp() FOR UPDATE`, w.ID, w.attempts).Scan(&token, &recorded)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if token != w.token {
		return nil
	}
	if recorded != "" {
		externalFingerprint, result = recorded, "succeeded"
	}
	guard, ok := r.guard.(storedDiscoveryGuard)
	if !ok {
		return f.ErrUnavailable
	}
	allowed, e := guard.StoredSessionEligible(ctx, r.db, tx, w.requester, w.session, w.version, w.epoch, f.KeyAdminPermission)
	if e != nil {
		return e
	}
	info, targetErr := r.keyInfo(ctx, tx, w.ActorID)
	if targetErr != nil || info.ActorURI != w.actorURI || info.KeyID != w.keyID {
		allowed = false
	}
	if !allowed && externalFingerprint == "" {
		result = "cancelled"
	}
	if result == "succeeded" {
		if len(externalFingerprint) != 64 || w.Action == "rotate" && externalFingerprint == w.ExpectedFingerprint {
			return f.ErrUnavailable
		}
		signer, keyErr := r.keys.LocalKey(ctx, w.ActorID)
		if keyErr != nil {
			return f.ErrUnavailable
		}
		public, ok := signer.Public().(*rsa.PublicKey)
		if !ok {
			return f.ErrUnavailable
		}
		actual, fingerprintErr := f.KeyFingerprint(public)
		if fingerprintErr != nil || actual != externalFingerprint {
			return f.ErrUnavailable
		}
		auditTarget := w.ActorID + ":" + w.ExpectedFingerprint + "->" + externalFingerprint
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_admin_audit(account_id,action,target,occurred_at)VALUES($1,$2,$3,$4)`, w.requester, "key."+w.Action, auditTarget, r.config.Clock()); e != nil {
			return e
		}
	}
	var finished any
	if result != "pending" {
		finished = r.config.Clock()
	}
	_, e = tx.ExecContext(ctx, `UPDATE owned_federation_key_commands SET status=$3,result_fingerprint=$4,next_attempt=$5,finished_at=$6,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2`, w.ID, w.token, result, nullString(externalFingerprint), r.config.Clock().Add(retryDelay(w.attempts)), finished)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) recordKeyCommandResult(ctx context.Context, w keyCommandWork, fingerprint string) error {
	if len(fingerprint) != 64 || w.Action == "rotate" && fingerprint == w.ExpectedFingerprint {
		return f.ErrUnavailable
	}
	res, e := r.db.ExecContext(ctx, `UPDATE owned_federation_key_commands SET result_fingerprint=$3 WHERE id=$1 AND lease_token=$2 AND status='inflight' AND attempts=$4 AND lease_until>clock_timestamp() AND(result_fingerprint IS NULL OR result_fingerprint=$3)`, w.ID, w.token, fingerprint, w.attempts)
	if e != nil {
		return e
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return f.ErrUnavailable
	}
	return nil
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (r *Repository) ProcessKeyCommands(ctx context.Context, limit int) (int, error) {
	admin, ok := r.keys.(f.IdempotentKeyAdministrator)
	if !ok {
		return 0, f.ErrUnavailable
	}
	if limit < 1 || limit > 100 {
		return 0, f.ErrInvalid
	}
	for n := 0; n < limit; n++ {
		w, e := r.claimKeyCommand(ctx)
		if errors.Is(e, sql.ErrNoRows) {
			return n, nil
		}
		if e != nil {
			return n, e
		}
		tx, e := r.db.BeginTx(ctx, nil)
		if e != nil {
			return n, e
		}
		allowed, check := r.keyCommandAllowed(ctx, tx, w)
		tx.Rollback()
		if check != nil {
			return n, check
		}
		if !allowed {
			if e = r.finishKeyCommand(ctx, w, "cancelled", ""); e != nil {
				return n, e
			}
			continue
		}
		if w.ResultFingerprint != "" {
			if e = r.finishKeyCommand(ctx, w, "succeeded", w.ResultFingerprint); e != nil {
				return n, e
			}
			continue
		}
		fingerprint, callErr := admin.ExecuteLocalKeyCommand(ctx, w.ID, w.ActorID, w.Action, w.ExpectedFingerprint)
		status := "succeeded"
		if callErr != nil {
			status = "pending"
		} else if e = r.recordKeyCommandResult(ctx, w, fingerprint); e != nil {
			return n, e
		}
		if e = r.finishKeyCommand(ctx, w, status, fingerprint); e != nil {
			return n, e
		}
	}
	return limit, nil
}
func (r *Repository) EnsureKey(context.Context, string, string) (f.KeyInfo, error) {
	return f.KeyInfo{}, f.ErrInvalid
}
func (r *Repository) RotateKey(context.Context, string, string) (f.KeyInfo, error) {
	return f.KeyInfo{}, f.ErrInvalid
}

func (r *Repository) Featured(ctx context.Context, name string, offset, take int) (f.Collection, error) {
	if page(offset, take) != nil {
		return f.Collection{}, f.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return f.Collection{}, e
	}
	defer tx.Rollback()
	p, e := r.publisherName(ctx, tx, name, true)
	if e != nil {
		return f.Collection{}, e
	}
	var total int64
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_featured_posts f JOIN owned_text_posts p ON p.id=f.post_id WHERE f.publisher_id=$1 AND p.visibility=0 AND p.drafted_at IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND p.published_at<=$2`, p.id, r.config.Clock()).Scan(&total)
	if e != nil {
		return f.Collection{}, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text FROM owned_federation_featured_posts f JOIN owned_text_posts p ON p.id=f.post_id WHERE f.publisher_id=$1 AND p.visibility=0 AND p.drafted_at IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND p.published_at<=$2 ORDER BY f.position,p.id OFFSET $3 LIMIT $4`, p.id, r.config.Clock(), offset, take)
	if e != nil {
		return f.Collection{}, e
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return f.Collection{}, e
		}
		items = append(items, r.objectID(id))
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return f.Collection{}, e
	}
	if e = tx.Commit(); e != nil {
		return f.Collection{}, e
	}
	return pageCollection(r.actorID(p)+"/featured", total, offset, take, items), nil
}
func (r *Repository) SetFeatured(ctx context.Context, actor, ref string, ids []string) error {
	if len(ids) > 100 {
		return f.ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !authority.ValidID(id) || seen[id] {
			return f.ErrInvalid
		}
		seen[id] = true
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	lockedIDs := append([]string(nil), ids...)
	sort.Strings(lockedIDs)
	for _, id := range lockedIDs {
		var ok bool
		if e = tx.QueryRowContext(ctx, `SELECT visibility=0 AND drafted_at IS NULL AND deleted_at IS NULL AND shadowbanned_at IS NULL AND published_at<=$3 FROM owned_text_posts WHERE id=$1 AND publisher_id=$2 FOR SHARE`, id, p.id, r.config.Clock()).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return f.ErrForbidden
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_featured_posts WHERE publisher_id=$1`, p.id); e != nil {
		return e
	}
	for i, id := range ids {
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_featured_posts(publisher_id,post_id,position,created_at)VALUES($1,$2,$3,$4)`, p.id, id, i, r.config.Clock()); e != nil {
			return e
		}
	}
	if e = r.audit(ctx, tx, actor, p.id, "featured.set"); e != nil {
		return e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.Permission); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) validateAttachments(actor string, attachments []f.Attachment) error {
	if len(attachments) > 16 {
		return f.ErrInvalid
	}
	origin, e := f.Origin(actor)
	if e != nil {
		return e
	}
	for _, a := range attachments {
		if !f.Has([]string{"Document", "Image", "Audio", "Video"}, a.Type) || len(a.Name) > 512 || len(a.MediaType) > 128 || len(a.URL) > 2048 {
			return f.ErrInvalid
		}
		u, e := url.Parse(a.URL)
		if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || "https://"+u.Host != origin || !r.config.Allows(a.URL) {
			return f.ErrForbidden
		}
		if a.MediaType != "" && !strings.HasPrefix(a.MediaType, "image/") && !strings.HasPrefix(a.MediaType, "audio/") && !strings.HasPrefix(a.MediaType, "video/") && !strings.HasPrefix(a.MediaType, "application/") {
			return f.ErrInvalid
		}
	}
	return nil
}

func (r *Repository) receiveMove(ctx context.Context, tx *sql.Tx, p publisher, old f.RemoteKey, a f.Activity, oldDigest []byte) error {
	object, e := f.ObjectID(a.Object)
	if e != nil || object != old.Actor {
		return f.ErrForbidden
	}
	target, e := f.ObjectID(a.Target)
	if e != nil || target == old.Actor {
		return f.ErrInvalid
	}
	next, e := r.remoteWithQuery(ctx, tx, target)
	if e != nil || next.Actor != target || !r.config.ValidRemote(next) || old.Incarnation == "" || next.Incarnation == "" {
		return f.ErrForbidden
	}
	if e = r.ensurePeer(ctx, tx, next); e != nil {
		return e
	}
	oldFingerprint, e := f.KeyFingerprint(old.PublicKey)
	if e != nil {
		return f.ErrSignature
	}
	newFingerprint, e := f.KeyFingerprint(next.PublicKey)
	if e != nil {
		return f.ErrSignature
	}
	// A Move is applied only after this publisher has durably observed the new
	// actor document, under its current configured pin, naming the old actor as
	// alsoKnownAs. The signed old-side Move and this reciprocal observation form
	// the immutable bilateral proof.
	var discoveryID, binding, assertedKeyID, assertedFingerprint string
	var aliases []byte
	e = tx.QueryRowContext(ctx, `SELECT discovery_id::text,remote_binding,key_id,key_fingerprint,aliases FROM owned_federation_actor_assertions WHERE publisher_id=$1 AND actor_uri=$2`, p.id, next.Actor).Scan(&discoveryID, &binding, &assertedKeyID, &assertedFingerprint, &aliases)
	if e != nil {
		return f.ErrForbidden
	}
	var observedAliases []string
	if json.Unmarshal(aliases, &observedAliases) != nil || assertedKeyID != next.KeyID || assertedFingerprint != newFingerprint || !f.Has(observedAliases, old.Actor) {
		return f.ErrForbidden
	}
	currentBinding, e := deliveryBinding(next)
	if e != nil || currentBinding != binding {
		return f.ErrForbidden
	}
	decision, e := r.moderationWith(ctx, tx, next.Actor, "")
	if e != nil || !decision.Allowed {
		if e != nil {
			return e
		}
		return f.ErrForbidden
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_moves(old_actor_uri,new_actor_uri,activity_id,old_digest,old_incarnation,old_fingerprint,new_incarnation,new_fingerprint,discovery_id,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)ON CONFLICT(old_actor_uri)DO NOTHING`, old.Actor, next.Actor, a.ID, oldDigest, old.Incarnation, oldFingerprint, next.Incarnation, newFingerprint, discoveryID, r.config.Clock())
	if e != nil {
		return e
	}
	var prior, priorActivity, priorOldIncarnation, priorOldFingerprint, priorNewIncarnation, priorNewFingerprint string
	var priorDigest []byte
	e = tx.QueryRowContext(ctx, `SELECT new_actor_uri,activity_id,old_digest,old_incarnation,old_fingerprint,new_incarnation,new_fingerprint FROM owned_federation_moves WHERE old_actor_uri=$1 FOR SHARE`, old.Actor).Scan(&prior, &priorActivity, &priorDigest, &priorOldIncarnation, &priorOldFingerprint, &priorNewIncarnation, &priorNewFingerprint)
	if e != nil || prior != next.Actor || priorActivity != a.ID || !bytes.Equal(priorDigest, oldDigest) || priorOldIncarnation != old.Incarnation || priorOldFingerprint != oldFingerprint || priorNewIncarnation != next.Incarnation || priorNewFingerprint != newFingerprint {
		return f.ErrReplay
	}
	var applied bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_move_applications WHERE old_actor_uri=$1 AND publisher_id=$2)`, old.Actor, p.id).Scan(&applied); e != nil {
		return e
	}
	if applied {
		return nil
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_blocks(publisher_id,actor_uri,created_at)SELECT publisher_id,$2,created_at FROM owned_federation_blocks WHERE publisher_id=$1 AND actor_uri=$3 ON CONFLICT DO NOTHING`, p.id, next.Actor, old.Actor); e != nil {
		return e
	}
	rows, e := tx.QueryContext(ctx, `SELECT direction,status,follow_id,updated_at FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 ORDER BY direction FOR UPDATE`, p.id, old.Actor)
	if e != nil {
		return e
	}
	type relation struct {
		d, s, id string
		at       time.Time
	}
	relations := []relation{}
	for rows.Next() {
		var x relation
		if e = rows.Scan(&x.d, &x.s, &x.id, &x.at); e != nil {
			rows.Close()
			return e
		}
		relations = append(relations, x)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2 AND direction='out'`, p.id, old.Actor); e != nil {
		return e
	}
	for _, x := range relations {
		if x.d == "in" {
			_, e = tx.ExecContext(ctx, `UPDATE owned_federation_relationships SET status='undone',updated_at=$3 WHERE publisher_id=$1 AND actor_uri=$2 AND direction='in'`, p.id, old.Actor, r.config.Clock())
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_relationships(publisher_id,actor_uri,direction,status,follow_id,updated_at)VALUES($1,$2,'out',$3,$4,$5)ON CONFLICT(publisher_id,actor_uri,direction)DO NOTHING`, p.id, next.Actor, x.s, x.id, x.at)
		}
		if e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_quote_authorizations SET revoked_at=COALESCE(revoked_at,$3)WHERE publisher_id=$1 AND actor_uri=$2`, p.id, old.Actor, r.config.Clock()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',finished_at=$3,lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND actor_uri=$2 AND status IN('pending','inflight')`, p.id, old.Actor, r.config.Clock()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_objects WHERE publisher_id=$1 AND actor_uri=$2`, p.id, old.Actor); e != nil {
		return e
	}
	currentOld, oldErr := r.remoteWithQuery(ctx, tx, old.Actor)
	currentNew, newErr := r.remoteWithQuery(ctx, tx, next.Actor)
	if oldErr != nil || newErr != nil || !sameQuoteKey(currentOld, old) || !sameQuoteKey(currentNew, next) {
		return f.ErrSignature
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_move_applications(old_actor_uri,publisher_id,applied_at)VALUES($1,$2,$3)`, old.Actor, p.id, r.config.Clock())
	return e
}
