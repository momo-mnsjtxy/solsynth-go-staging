// SPDX-License-Identifier: AGPL-3.0-only
package federationpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strconv"
	"time"

	"solsynth.local/go-migration/ports/authority"
	f "solsynth.local/go-migration/ports/federation"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db     *sql.DB
	config f.Config
	keys   f.KeyProvider
	guard  TransactionGuard
}

func New(db *sql.DB, c f.Config, keys f.KeyProvider, guard TransactionGuard) (*Repository, error) {
	if db == nil {
		return nil, f.ErrUnavailable
	}
	if e := c.Validate(); e != nil {
		return nil, e
	}
	c.AllowedOrigins = append([]string{}, c.AllowedOrigins...)
	return &Repository{db: db, config: c, keys: keys, guard: guard}, nil
}

var _ f.Repository = (*Repository)(nil)

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type publisher struct {
	id, name, nick, bio, owner string
	created                    time.Time
	enabled, manual            bool
}

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return f.ErrNotFound
	}
	return e
}
func newID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func (r *Repository) actorID(p publisher) string {
	return r.config.Origin + "/activitypub/actors/" + url.PathEscape(p.name)
}
func (r *Repository) objectID(id string) string {
	return r.config.Origin + "/activitypub/objects/" + id
}
func (r *Repository) publisher(ctx context.Context, q queryer, ref string, enabled bool) (publisher, error) {
	return r.lookupPublisher(ctx, q, ref, enabled, authority.ValidID(ref))
}
func (r *Repository) publisherName(ctx context.Context, q queryer, ref string, enabled bool) (publisher, error) {
	return r.lookupPublisher(ctx, q, ref, enabled, false)
}
func (r *Repository) lookupPublisher(ctx context.Context, q queryer, ref string, enabled, byID bool) (publisher, error) {
	var p publisher
	if ref == "" || len(ref) > 256 {
		return p, f.ErrNotFound
	}
	predicate := `lower(p.name)=lower($1)`
	if byID {
		predicate = `p.id=$1::uuid`
	}
	e := q.QueryRowContext(ctx, `SELECT p.id::text,p.name,p.nick,p.bio,p.account_id::text,p.created_at,COALESCE(f.enabled,false),COALESCE(f.manual,true) FROM owned_publishers p JOIN owned_authority_accounts a ON a.id=p.account_id LEFT JOIN owned_federation_publishers f ON f.publisher_id=p.id WHERE `+predicate+` AND p.type=0 AND p.deleted_at IS NULL AND NOT a.disabled`, ref).Scan(&p.id, &p.name, &p.nick, &p.bio, &p.owner, &p.created, &p.enabled, &p.manual)
	if e != nil {
		return p, mapped(e)
	}
	if !f.ValidName(p.name) || enabled && !p.enabled {
		return p, f.ErrNotFound
	}
	return p, nil
}
func (r *Repository) localKey(ctx context.Context, p publisher) (f.PublicKey, error) {
	if r.keys == nil {
		return f.PublicKey{}, f.ErrUnavailable
	}
	signer, e := r.keys.LocalKey(ctx, p.id)
	if e != nil {
		return f.PublicKey{}, f.ErrUnavailable
	}
	pem, e := f.PEM(signer)
	if e != nil {
		return f.PublicKey{}, e
	}
	id := r.actorID(p)
	return f.PublicKey{ID: id + "#main-key", Owner: id, PEM: pem}, nil
}
func (r *Repository) Actor(ctx context.Context, name string) (f.Actor, error) {
	p, e := r.publisherName(ctx, r.db, name, true)
	if e != nil {
		return f.Actor{}, e
	}
	key, e := r.localKey(ctx, p)
	if e != nil {
		return f.Actor{}, e
	}
	id := r.actorID(p)
	return f.Actor{Context: []string{f.Context, "https://w3id.org/security/v1"}, ID: id, Type: "Person", Username: p.name, Name: p.nick, Summary: html.EscapeString(p.bio), Inbox: id + "/inbox", Outbox: id + "/outbox", Followers: id + "/followers", Following: id + "/following", Published: p.created, PublicKey: key, Manual: p.manual}, nil
}
func (r *Repository) note(ctx context.Context, q queryer, id string) (f.Note, string, time.Time, error) {
	var n f.Note
	var pid, content string
	var updated time.Time
	if !authority.ValidID(id) {
		return n, "", updated, f.ErrNotFound
	}
	e := q.QueryRowContext(ctx, `SELECT p.publisher_id::text,p.content,p.published_at,p.updated_at FROM owned_text_posts p WHERE p.id=$1 AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.visibility=0 AND p.published_at<= $2`, id, r.config.Clock()).Scan(&pid, &content, &n.Published, &updated)
	if e != nil {
		return n, "", updated, mapped(e)
	}
	pub, e := r.publisher(ctx, q, pid, true)
	if e != nil {
		return n, "", updated, e
	}
	n.Context = f.Context
	n.ID = r.objectID(id)
	n.Type = "Note"
	n.AttributedTo = r.actorID(pub)
	n.Content = html.EscapeString(content)
	n.MediaType = "text/html"
	n.To = []string{f.Public}
	n.CC = []string{n.AttributedTo + "/followers"}
	n.Updated = updated
	return n, pid, updated, nil
}
func (r *Repository) Object(ctx context.Context, id string) (f.Note, error) {
	n, _, _, e := r.note(ctx, r.db, id)
	return n, e
}
func page(offset, take int) error {
	if offset < 0 || offset > 10000 || take < 1 || take > 100 {
		return f.ErrInvalid
	}
	return nil
}
func pageCollection(id string, total int64, offset, take int, items []any) f.Collection {
	c := f.Collection{Context: f.Context, ID: id + "?offset=" + strconv.Itoa(offset) + "&take=" + strconv.Itoa(take), Type: "OrderedCollectionPage", PartOf: id, Total: total, Items: items}
	if int64(offset+len(items)) < total {
		c.Next = id + "?offset=" + strconv.Itoa(offset+take) + "&take=" + strconv.Itoa(take)
	}
	return c
}
func (r *Repository) Outbox(ctx context.Context, name string, offset, take int) (f.Collection, error) {
	if e := page(offset, take); e != nil {
		return f.Collection{}, e
	}
	p, e := r.publisherName(ctx, r.db, name, true)
	if e != nil {
		return f.Collection{}, e
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return f.Collection{}, e
	}
	defer tx.Rollback()
	var total int64
	where := ` FROM owned_text_posts WHERE publisher_id=$1 AND visibility=0 AND drafted_at IS NULL AND deleted_at IS NULL AND published_at<=$2`
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, p.id, r.config.Clock()).Scan(&total); e != nil {
		return f.Collection{}, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text`+where+` ORDER BY published_at DESC,id DESC OFFSET $3 LIMIT $4`, p.id, r.config.Clock(), offset, take)
	if e != nil {
		return f.Collection{}, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return f.Collection{}, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return f.Collection{}, e
	}
	items := []any{}
	for _, id := range ids {
		n, _, _, e := r.note(ctx, tx, id)
		if e != nil {
			return f.Collection{}, e
		}
		b, _ := json.Marshal(n)
		items = append(items, f.Activity{ID: n.ID + "/activity", Type: "Create", Actor: n.AttributedTo, Object: b, To: n.To, CC: n.CC, Published: n.Published})
	}
	if e = tx.Commit(); e != nil {
		return f.Collection{}, e
	}
	return pageCollection(r.actorID(p)+"/outbox", total, offset, take, items), nil
}
func (r *Repository) Collection(ctx context.Context, name, kind string, offset, take int) (f.Collection, error) {
	if e := page(offset, take); e != nil {
		return f.Collection{}, e
	}
	direction := "in"
	if kind == "following" {
		direction = "out"
	} else if kind != "followers" {
		return f.Collection{}, f.ErrNotFound
	}
	p, e := r.publisherName(ctx, r.db, name, true)
	if e != nil {
		return f.Collection{}, e
	}
	// Counts only: follower identities are intentionally not disclosed publicly.
	var total int64
	e = r.db.QueryRowContext(ctx, `SELECT count(*) FROM owned_federation_relationships x WHERE x.publisher_id=$1 AND x.direction=$2 AND x.status='accepted' AND NOT EXISTS(SELECT 1 FROM owned_federation_blocks b WHERE b.publisher_id=x.publisher_id AND b.actor_uri=x.actor_uri)`, p.id, direction).Scan(&total)
	return f.Collection{Context: f.Context, ID: r.actorID(p) + "/" + kind, Type: "OrderedCollection", Total: total}, e
}
func (r *Repository) beginLocal(ctx context.Context, actor, ref string) (*sql.Tx, publisher, error) {
	if r.guard == nil || !authority.ValidID(actor) {
		return nil, publisher{}, f.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, publisher{}, e
	}
	fail := func(e error) (*sql.Tx, publisher, error) { tx.Rollback(); return nil, publisher{}, e }
	// Lock all relevant account rows before publisher and membership rows. This
	// aligns fresh credential revocation and publisher-owner disable transitions.
	p, e := r.publisher(ctx, tx, ref, false)
	if e != nil {
		return fail(e)
	}
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, actor, p.owner)
	if e != nil {
		return fail(e)
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return fail(e)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return fail(e)
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, f.Permission); e != nil {
		return fail(e)
	}
	var locked string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, p.id).Scan(&locked); e != nil {
		return fail(mapped(e))
	}
	p, e = r.publisher(ctx, tx, p.id, false)
	if e != nil {
		return fail(e)
	}
	var role int
	e = tx.QueryRowContext(ctx, `SELECT role FROM owned_publisher_members WHERE publisher_id=$1 AND account_id=$2 AND joined_at IS NOT NULL AND revoked_at IS NULL FOR UPDATE`, p.id, actor).Scan(&role)
	if e != nil || role < 100 {
		return fail(f.ErrForbidden)
	}
	return tx, p, nil
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor, publisher, action string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_federation_audit(account_id,publisher_id,action,occurred_at) VALUES($1,$2,$3,$4)`, actor, publisher, action, r.config.Clock())
	return e
}
func (r *Repository) Settings(ctx context.Context, actor, ref string, s f.Settings) error {
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if s.Enabled {
		if _, e = r.localKey(ctx, p); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_publishers(publisher_id,enabled,manual,updated_at) VALUES($1,$2,$3,$4) ON CONFLICT(publisher_id) DO UPDATE SET enabled=EXCLUDED.enabled,manual=EXCLUDED.manual,updated_at=EXCLUDED.updated_at`, p.id, s.Enabled, s.Manual, r.config.Clock()); e != nil {
		return e
	}
	if !s.Enabled {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_federation_deliveries SET status='cancelled',finished_at=$2,lease_token=NULL,lease_until=NULL WHERE publisher_id=$1 AND status IN('pending','inflight')`, p.id, r.config.Clock()); e != nil {
			return e
		}
	}
	if e = r.audit(ctx, tx, actor, p.id, "settings"); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Relationships(ctx context.Context, actor, ref string, offset, take int) ([]f.Relationship, error) {
	if e := page(offset, take); e != nil {
		return nil, e
	}
	tx, p, e := r.beginLocal(ctx, actor, ref)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT actor_uri,direction,status,follow_id FROM owned_federation_relationships WHERE publisher_id=$1 ORDER BY actor_uri,direction OFFSET $2 LIMIT $3`, p.id, offset, take)
	if e != nil {
		return nil, e
	}
	out := []f.Relationship{}
	for rows.Next() {
		var x f.Relationship
		if e = rows.Scan(&x.RemoteActor, &x.Direction, &x.Status, &x.FollowID); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	return out, e
}
func marshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (r *Repository) ensurePeer(ctx context.Context, tx *sql.Tx, k f.RemoteKey) error {
	if !r.config.ValidRemote(k) {
		return f.ErrForbidden
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_federation_peers(actor_uri,inbox_uri,updated_at) VALUES($1,$2,$3) ON CONFLICT(actor_uri) DO UPDATE SET inbox_uri=EXCLUDED.inbox_uri,updated_at=EXCLUDED.updated_at`, k.Actor, k.Inbox, r.config.Clock())
	return e
}
func (r *Repository) blocked(ctx context.Context, tx *sql.Tx, pid, remote string) (bool, error) {
	var b bool
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_federation_blocks WHERE publisher_id=$1 AND actor_uri=$2)`, pid, remote).Scan(&b)
	return b, e
}
func (r *Repository) activity(p publisher, kind string, object any, to string) (f.Activity, error) {
	id, e := newID()
	if e != nil {
		return f.Activity{}, e
	}
	return f.Activity{Context: f.Context, ID: r.actorID(p) + "/activities/" + id, Actor: r.actorID(p), Type: kind, Object: marshal(object), To: []string{to}}, nil
}
func (r *Repository) enqueue(ctx context.Context, tx *sql.Tx, p publisher, k f.RemoteKey, a f.Activity, postID string, revision *time.Time) error {
	if !r.config.ValidRemote(k) {
		return f.ErrForbidden
	}
	body, e := json.Marshal(a)
	if e != nil || len(body) > f.MaxBody {
		return f.ErrInvalid
	}
	var post, audit any
	if postID != "" {
		post = postID
		var seq int64
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(max(id),0) FROM owned_text_post_audit WHERE post_id=$1`, postID).Scan(&seq); e != nil {
			return e
		}
		audit = seq
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_federation_deliveries(publisher_id,actor_uri,inbox_uri,activity_id,activity_type,payload,post_id,post_updated_at,post_audit_id,next_attempt,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10) ON CONFLICT(activity_id,actor_uri) DO NOTHING`, p.id, k.Actor, k.Inbox, a.ID, a.Type, body, post, revision, audit, r.config.Clock())
	return e
}
func (r *Repository) remote(ctx context.Context, uri string) (f.RemoteKey, error) {
	if r.keys == nil {
		return f.RemoteKey{}, f.ErrUnavailable
	}
	if !r.config.Allows(uri) {
		return f.RemoteKey{}, f.ErrForbidden
	}
	k, e := r.keys.RemoteActor(ctx, uri)
	if e != nil || k.Actor != uri || !r.config.ValidRemote(k) {
		return f.RemoteKey{}, f.ErrForbidden
	}
	return k, nil
}

// Config returns an independent copy for the HTTP signature boundary.
func (r *Repository) Config() f.Config {
	c := r.config
	c.AllowedOrigins = append([]string{}, c.AllowedOrigins...)
	return c
}
func (r *Repository) Keys() f.KeyProvider { return r.keys }
