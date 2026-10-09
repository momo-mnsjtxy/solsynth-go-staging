// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
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
		if e == nil {
			rows, er := tx.QueryContext(ctx, `SELECT actor_uri FROM owned_federation_realm_relationships WHERE realm_id=$1 AND status='accepted' ORDER BY actor_uri OFFSET $2 LIMIT $3`, x.id, offset, take)
			if er != nil {
				return f.Collection{}, er
			}
			defer rows.Close()
			for rows.Next() {
				var actor string
				if er = rows.Scan(&actor); er != nil {
					return f.Collection{}, er
				}
				items = append(items, actor)
			}
			e = rows.Err()
		}
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
	return pageCollection(id, total, offset, take, items), nil
}
func (r *Repository) realmOutbox(ctx context.Context, q queryer, x realmIdentity, offset, take int) ([]any, int64, error) {
	var local, announced int64
	where := ` FROM owned_text_posts p WHERE p.realm_id=$1 AND p.visibility=0 AND p.drafted_at IS NULL AND p.deleted_at IS NULL AND p.shadowbanned_at IS NULL AND p.published_at<=$2 AND(p.publisher_id=$3 OR EXISTS(SELECT 1 FROM owned_federation_publishers f WHERE f.publisher_id=p.publisher_id AND f.enabled AND f.actor_uri IS NOT NULL))`
	if e := q.QueryRowContext(ctx, `SELECT count(*)`+where, x.id, r.config.Clock(), x.publisher).Scan(&local); e != nil {
		return nil, 0, e
	}
	if e := q.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_realm_outbox WHERE realm_id=$1`, x.id).Scan(&announced); e != nil {
		return nil, 0, e
	}
	type timed struct {
		at    time.Time
		id    string
		value any
	}
	all := []timed{}
	limit := offset + take
	rows, e := q.QueryContext(ctx, `SELECT p.id::text,p.published_at`+where+` ORDER BY p.published_at DESC,p.id DESC LIMIT $4`, x.id, r.config.Clock(), x.publisher, limit)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var pid string
		var published time.Time
		if e = rows.Scan(&pid, &published); e != nil {
			rows.Close()
			return nil, 0, e
		}
		n, _, _, ne := r.note(ctx, q, pid)
		if ne != nil {
			rows.Close()
			return nil, 0, ne
		}
		kind := "Announce"
		object := marshal(n.ID)
		if n.AttributedTo == x.actor {
			kind = "Create"
			object = marshal(n)
		}
		a := f.Activity{Context: f.Context, ID: x.actor + "/activities/post-" + pid, Type: kind, Actor: x.actor, Object: object, To: []string{f.Public}, CC: []string{x.actor + "/followers"}, Published: published}
		all = append(all, timed{published, a.ID, a})
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, 0, e
	}
	rows.Close()
	rows, e = q.QueryContext(ctx, `SELECT activity,created_at,activity_id FROM owned_federation_realm_outbox WHERE realm_id=$1 ORDER BY created_at DESC,activity_id DESC LIMIT $2`, x.id, limit)
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
	return items, local + announced, nil
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
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_realm_deliveries SET status='cancelled',finished_at=$2 WHERE realm_id=$1 AND status='pending'`, realmID, r.config.Clock()); e != nil {
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
	return strings.Contains(strings.ToLower(candidate), strings.ToLower(rule.Pattern))
}
func (r *Repository) moderationWith(ctx context.Context, q queryer, actor, content string) (f.ModerationDecision, error) {
	rows, e := q.QueryContext(ctx, `SELECT id::text,name,description,kind,action,pattern,is_regex,enabled,priority,expires_at,created_at,updated_at FROM owned_federation_moderation_rules WHERE enabled AND(expires_at IS NULL OR expires_at>$1)ORDER BY priority,id`, r.config.Clock())
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
	return tx.Commit()
}

func (r *Repository) keyInfo(ctx context.Context, q queryer, id string) (f.KeyInfo, error) {
	var x f.KeyInfo
	var handle string
	e := q.QueryRowContext(ctx, `SELECT p.id::text,COALESCE(f.actor_uri,fr.actor_uri,''),COALESCE(f.key_id,fr.key_id,''),COALESCE(f.identity_handle,p.name) FROM owned_publishers p LEFT JOIN owned_federation_publishers f ON f.publisher_id=p.id LEFT JOIN owned_federation_realms fr ON fr.realm_id=p.realm_id WHERE p.id=$1 AND p.deleted_at IS NULL`, id).Scan(&x.ActorID, &x.ActorURI, &x.KeyID, &handle)
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
func (r *Repository) keyMutation(ctx context.Context, actor, id, action string) (f.KeyInfo, error) {
	admin, ok := r.keys.(f.KeyAdministrator)
	if !ok {
		return f.KeyInfo{}, f.ErrUnavailable
	}
	if !authority.ValidID(id) {
		return f.KeyInfo{}, f.ErrNotFound
	}
	var owner string
	e := r.db.QueryRowContext(ctx, `SELECT COALESCE(p.account_id,x.account_id)::text FROM owned_publishers p LEFT JOIN owned_realms x ON x.id=p.realm_id WHERE p.id=$1 AND p.deleted_at IS NULL`, id).Scan(&owner)
	if e != nil {
		return f.KeyInfo{}, mapped(e)
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return f.KeyInfo{}, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, actor, owner)
	if e != nil {
		return f.KeyInfo{}, e
	}
	for rows.Next() {
		var locked string
		if e = rows.Scan(&locked); e != nil {
			rows.Close()
			return f.KeyInfo{}, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return f.KeyInfo{}, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return f.KeyInfo{}, e
	}
	var currentOwner string
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(p.account_id,x.account_id)::text FROM owned_publishers p LEFT JOIN owned_realms x ON x.id=p.realm_id WHERE p.id=$1 AND p.deleted_at IS NULL FOR UPDATE OF p`, id).Scan(&currentOwner); e != nil {
		return f.KeyInfo{}, mapped(e)
	}
	if currentOwner != owner {
		return f.KeyInfo{}, f.ErrForbidden
	}
	if _, e = r.keyInfo(ctx, tx, id); e != nil && !errors.Is(e, f.ErrUnavailable) {
		return f.KeyInfo{}, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return f.KeyInfo{}, e
	}
	if action == "ensure" {
		e = admin.EnsureLocalKey(ctx, id)
	} else {
		e = admin.RotateLocalKey(ctx, id)
	}
	if e != nil {
		return f.KeyInfo{}, f.ErrUnavailable
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.KeyAdminPermission); e != nil {
		return f.KeyInfo{}, e
	}
	x, e := r.keyInfo(ctx, tx, id)
	if e != nil || !x.HasKey {
		return x, f.ErrUnavailable
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_admin_audit(account_id,action,target,occurred_at)VALUES($1,$2,$3,$4)`, actor, "key."+action, id, r.config.Clock())
	if e != nil {
		return x, e
	}
	return x, tx.Commit()
}
func (r *Repository) EnsureKey(ctx context.Context, actor, id string) (f.KeyInfo, error) {
	return r.keyMutation(ctx, actor, id, "ensure")
}
func (r *Repository) RotateKey(ctx context.Context, actor, id string) (f.KeyInfo, error) {
	return r.keyMutation(ctx, actor, id, "rotate")
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
	for _, id := range ids {
		var ok bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_text_posts WHERE id=$1 AND publisher_id=$2 AND visibility=0 AND drafted_at IS NULL AND deleted_at IS NULL AND shadowbanned_at IS NULL AND published_at<=$3)`, id, p.id, r.config.Clock()).Scan(&ok); e != nil {
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

func (r *Repository) receiveMove(ctx context.Context, tx *sql.Tx, p publisher, old f.RemoteKey, a f.Activity) error {
	object, e := f.ObjectID(a.Object)
	if e != nil || object != old.Actor {
		return f.ErrForbidden
	}
	target, e := f.ObjectID(a.Target)
	if e != nil || target == old.Actor {
		return f.ErrInvalid
	}
	next, e := r.remoteWithQuery(ctx, tx, target)
	if e != nil || next.Actor != target || !r.config.ValidRemote(next) {
		return f.ErrForbidden
	}
	if e = r.ensurePeer(ctx, tx, next); e != nil {
		return e
	}
	decision, e := r.moderationWith(ctx, tx, next.Actor, "")
	if e != nil || !decision.Allowed {
		if e != nil {
			return e
		}
		return f.ErrForbidden
	}
	var prior string
	e = tx.QueryRowContext(ctx, `SELECT new_actor_uri FROM owned_federation_moves WHERE old_actor_uri=$1`, old.Actor).Scan(&prior)
	if e == nil {
		if prior == next.Actor {
			return nil
		}
		return f.ErrReplay
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
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
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_federation_relationships WHERE publisher_id=$1 AND actor_uri=$2`, p.id, old.Actor); e != nil {
		return e
	}
	for _, x := range relations {
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_relationships(publisher_id,actor_uri,direction,status,follow_id,updated_at)VALUES($1,$2,$3,$4,$5,$6)ON CONFLICT(publisher_id,actor_uri,direction)DO NOTHING`, p.id, next.Actor, x.d, x.s, x.id, x.at)
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
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_moves(old_actor_uri,new_actor_uri,activity_id,completed_at)VALUES($1,$2,$3,$4)`, old.Actor, next.Actor, a.ID, r.config.Clock())
	return e
}
