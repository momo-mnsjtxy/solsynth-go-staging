// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

//go:embed schema.sql
var Schema string

type Config struct {
	TokenTTL, MaxRunDuration, IdleGrace time.Duration
	MaxStreamsPerPublisher              int
}
type Repository struct {
	db         *sql.DB
	guard      *authoritypg.TransactionGuard
	publishers live.PublisherProvider
	media      live.MediaProvider
	social     live.SocialProvider
	provider   live.Provider
	cfg        Config
	chat       *ChatConfig
}

var _ live.Repository = (*Repository)(nil)

func New(db *sql.DB, g *authoritypg.TransactionGuard, publishers live.PublisherProvider, media live.MediaProvider, social live.SocialProvider, provider live.Provider, c Config) (*Repository, error) {
	if db == nil || g == nil || publishers == nil || social == nil || c.TokenTTL < 15*time.Second || c.TokenTTL > 5*time.Minute || c.MaxRunDuration < c.TokenTTL || c.MaxRunDuration > 24*time.Hour || c.IdleGrace < 5*time.Minute || c.IdleGrace > time.Hour || c.MaxStreamsPerPublisher < 1 || c.MaxStreamsPerPublisher > 10000 {
		return nil, live.ErrInvalid
	}
	if provider != nil && (!provider.StrictRevocation() || provider.Name() == "" || provider.Endpoint() == "") {
		return nil, live.ErrUnconfigured
	}
	return &Repository{db: db, guard: g, publishers: publishers, media: media, social: social, provider: provider, cfg: c}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if e := r.configured(); e != nil {
		return e
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,publisher_id,operation_state FROM owned_livestreams LIMIT 0;SELECT generation,phase,ingress_id,owner_version,permission FROM owned_livestream_runs LIMIT 0;SELECT state,identity,permission FROM owned_livestream_admissions LIMIT 0;SELECT ingress_id,deleted_at FROM owned_livestream_ingresses LIMIT 0;SELECT request_hash FROM owned_livestream_create_keys LIMIT 0;SELECT action FROM owned_livestream_audit LIMIT 0`)
	if e == nil && r.chat != nil {
		return r.ChatReady(ctx)
	}
	return e
}
func (r *Repository) configured() error {
	if r == nil || r.db == nil || r.provider == nil {
		return live.ErrUnconfigured
	}
	return nil
}
func uid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func now(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var t time.Time
	e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&t)
	return t.UTC(), e
}
func hash(v any) []byte { raw, _ := json.Marshal(v); h := sha256.Sum256(raw); return h[:] }
func authError(e error) error {
	if errors.Is(e, accounts.ErrNotFound) {
		return live.ErrNotFound
	}
	if errors.Is(e, accounts.ErrForbidden) {
		return live.ErrForbidden
	}
	if errors.Is(e, accounts.ErrAmbiguous) || errors.Is(e, accounts.ErrConflict) {
		return live.ErrConflict
	}
	return e
}
func (r *Repository) begin(ctx context.Context) (*sql.Tx, error) {
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
func (r *Repository) authorize(ctx context.Context, tx *sql.Tx, actor, permission string) (authority.Principal, error) {
	if permission == "" {
		return r.guard.Authenticate(ctx, r.db, tx, actor)
	}
	return r.guard.Authorize(ctx, r.db, tx, actor, permission)
}
func (r *Repository) commit(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if _, e := r.authorize(ctx, tx, actor, permission); e != nil {
		return e
	}
	return tx.Commit()
}
func audit(ctx context.Context, tx *sql.Tx, id, actor, action string) error {
	var a any
	if actor != "" {
		a = actor
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_livestream_audit(stream_id,actor_id,action,occurred_at)VALUES($1,$2,$3,clock_timestamp())`, id, a, action)
	return e
}

const fields = `s.id::text,s.publisher_id::text,s.title,s.description,s.slug,s.type,s.visibility,s.status,s.room_name,s.thumbnail_id::text,s.metadata,s.generation,s.operation_state,s.started_at,s.ended_at,s.total_duration_seconds,s.viewer_count,s.peak_viewer_count,s.created_at,s.updated_at,s.deleted_at`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (v live.Stream, e error) {
	var raw []byte
	e = s.Scan(&v.ID, &v.PublisherID, &v.Title, &v.Description, &v.Slug, &v.Type, &v.Visibility, &v.Status, &v.RoomName, &v.ThumbnailID, &raw, &v.Generation, &v.OperationState, &v.StartedAt, &v.EndedAt, &v.TotalDurationSeconds, &v.ViewerCount, &v.PeakViewerCount, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return v, live.ErrNotFound
	}
	if e == nil && len(raw) > 0 {
		e = json.Unmarshal(raw, &v.Metadata)
	}
	v.ResourceIdentifier = "livestream:" + v.ID
	return
}
func get(ctx context.Context, tx *sql.Tx, id string, lock, deleted bool) (live.Stream, error) {
	q := `SELECT ` + fields + ` FROM owned_livestreams s WHERE s.id=$1`
	if !deleted {
		q += ` AND s.deleted_at IS NULL`
	}
	if lock {
		q += ` FOR UPDATE OF s`
	}
	return scan(tx.QueryRowContext(ctx, q, id))
}

type scope struct {
	publisher, owner, realm, starter string
	generation                       int64
}

func observe(ctx context.Context, tx *sql.Tx, pid, sid string) (s scope, e error) {
	if sid != "" {
		e = tx.QueryRowContext(ctx, `SELECT s.publisher_id::text,s.generation,COALESCE(run.actor_id::text,'') FROM owned_livestreams s LEFT JOIN owned_livestream_runs run ON run.stream_id=s.id AND run.generation=s.generation WHERE s.id=$1`, sid).Scan(&pid, &s.generation, &s.starter)
		if errors.Is(e, sql.ErrNoRows) {
			e = live.ErrNotFound
		}
		if e != nil {
			return
		}
	}
	s.publisher = pid
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(p.account_id::text,realm.account_id::text,''),COALESCE(p.realm_id::text,'') FROM owned_publishers p LEFT JOIN owned_realms realm ON realm.id=p.realm_id WHERE p.id=$1 AND p.type IN(0,1)`, pid).Scan(&s.owner, &s.realm)
	if errors.Is(e, sql.ErrNoRows) {
		e = live.ErrNotFound
	}
	if e == nil && s.owner == "" {
		e = live.ErrForbidden
	}
	return
}
func same(a, b scope) bool { return a == b }
func prelock(ctx context.Context, tx *sql.Tx, actor, sid string, s scope, background bool, extra ...string) error {
	ids := append([]string{actor, s.owner, s.starter}, extra...)
	sort.Strings(ids)
	prior := ""
	suffix := ""
	if background {
		suffix = " NOWAIT"
	}
	for _, id := range ids {
		if id == "" || id == prior {
			continue
		}
		prior = id
		q := `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR SHARE`
		if id == actor {
			q = `SELECT id::text FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`
		}
		var found string
		if e := tx.QueryRowContext(ctx, q+suffix, id).Scan(&found); e != nil {
			return e
		}
	}
	return nil
}
func lockScope(ctx context.Context, tx *sql.Tx, s scope, background bool) error {
	suffix := ""
	if background {
		suffix = " NOWAIT"
	}
	var found string
	if s.realm != "" {
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1 FOR SHARE`+suffix, s.realm).Scan(&found); e != nil {
			return e
		}
	}
	return tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR UPDATE`+suffix, s.publisher).Scan(&found)
}
func (r *Repository) locked(ctx context.Context, actor, sid, selector, permission string, editor, includingDeleted bool, extra ...string) (tx *sql.Tx, p authority.Principal, v live.Stream, e error) {
	if !authority.ValidID(actor) || sid != "" && !authority.ValidID(sid) {
		return nil, p, v, live.ErrInvalid
	}
	tx, e = r.begin(ctx)
	if e != nil {
		return
	}
	fail := func(err error) (*sql.Tx, authority.Principal, live.Stream, error) {
		tx.Rollback()
		return nil, p, v, authError(err)
	}
	var permissions []string
	if permission != "" {
		permissions = []string{permission}
	}
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, permissions...); e != nil {
		return fail(e)
	}
	pid := ""
	if sid == "" {
		pid, e = r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
		if e != nil {
			return fail(e)
		}
	}
	s, e := observe(ctx, tx, pid, sid)
	if e != nil {
		return fail(e)
	}
	if e = prelock(ctx, tx, actor, sid, s, false, extra...); e != nil {
		return fail(e)
	}
	p, e = r.authorize(ctx, tx, actor, permission)
	if e != nil {
		return fail(e)
	}
	// This first-party resource lane has no reviewed delegated/child-session
	// contract. Recorded historic parents are checked only for revocation.
	if p.Session.Type != 0 || p.Session.ParentID != "" {
		return fail(authority.ErrInvalid)
	}
	if e = lockScope(ctx, tx, s, false); e != nil {
		return fail(e)
	}
	if sid != "" {
		v, e = get(ctx, tx, sid, true, includingDeleted)
		if e != nil {
			return fail(e)
		}
	}
	fresh, e := observe(ctx, tx, s.publisher, sid)
	if e != nil {
		return fail(e)
	}
	if !same(s, fresh) {
		return fail(live.ErrConflict)
	}
	if sid == "" {
		freshID, x := r.publishers.ObservePostPublisherID(ctx, tx, actor, selector)
		if x != nil {
			return fail(x)
		}
		if freshID != s.publisher {
			return fail(live.ErrConflict)
		}
		v.PublisherID = s.publisher
	}
	if editor {
		pub, x := r.publishers.RequirePostEditor(ctx, tx, actor, s.publisher)
		if x != nil {
			return fail(x)
		}
		v.Publisher = &pub
	}
	if e = activeAccount(ctx, tx, s.owner); e != nil {
		return fail(e)
	}
	return tx, p, v, nil
}
func activeAccount(ctx context.Context, tx *sql.Tx, id string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, id).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return live.ErrForbidden
	}
	return nil
}
func (r *Repository) visible(ctx context.Context, tx *sql.Tx, actor string, v *live.Stream) (editor bool, e error) {
	p, e := r.publishers.VisiblePublisherInTransaction(ctx, tx, actor, v.PublisherID)
	if e != nil {
		return false, authError(e)
	}
	s, e := observe(ctx, tx, v.PublisherID, "")
	if e != nil {
		return false, e
	}
	if e = activeAccount(ctx, tx, s.owner); e != nil {
		return false, live.ErrNotFound
	}
	if actor != "" {
		blocked, x := r.social.IsBlockedInTransaction(ctx, tx, actor, s.owner)
		if x != nil {
			return false, x
		}
		if blocked {
			return false, live.ErrForbidden
		}
	}
	editor, e = r.publishers.CanEditPublisherInTransaction(ctx, tx, actor, v.PublisherID)
	if e != nil {
		return false, e
	}
	if v.Visibility == live.Private && !editor {
		return false, live.ErrNotFound
	}
	v.Publisher = &p
	if v.ThumbnailID != nil && r.media != nil {
		f, x := r.media.ResolvePublicInTransaction(ctx, tx, "", *v.ThumbnailID)
		if x == nil {
			v.Thumbnail = &f
		} else if !errors.Is(x, media.ErrNotFound) && !errors.Is(x, media.ErrForbidden) {
			return false, x
		}
	}
	return editor, nil
}
func (r *Repository) read(ctx context.Context, actor string) (*sql.Tx, error) {
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, e
	}
	if actor != "" {
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor); e != nil {
			tx.Rollback()
			return nil, e
		}
	}
	return tx, nil
}
func (r *Repository) Get(ctx context.Context, actor, id string) (live.Stream, error) {
	if !authority.ValidID(id) {
		return live.Stream{}, live.ErrInvalid
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return live.Stream{}, e
	}
	defer tx.Rollback()
	v, e := get(ctx, tx, id, false, false)
	if e != nil {
		return v, e
	}
	if _, e = r.visible(ctx, tx, actor, &v); e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *Repository) List(ctx context.Context, actor, pid string, limit, offset int) ([]live.Stream, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 || pid != "" && !authority.ValidID(pid) {
		return nil, live.ErrInvalid
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// Public discovery never reveals unlisted/private streams. Editor publisher
	// listing includes them, subject to the same canonical realm membership.
	where := `s.deleted_at IS NULL AND p.deleted_at IS NULL AND ` + r.social.PublisherVisibilitySQL("p", "$1") + ` AND(p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=p.account_id AND NOT a.disabled)) AND (s.visibility=0 OR ($2::text<>'' AND EXISTS(SELECT 1 FROM owned_publisher_members m WHERE m.publisher_id=p.id AND m.account_id=NULLIF($1::text,'')::uuid AND m.role>=50 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND ` + r.social.PublisherMembershipSQL("p", "$1") + `))) AND ($2::text='' AND s.status=1 OR p.id::text=$2) AND NOT EXISTS(SELECT 1 FROM owned_social_relationships b WHERE b.status=-100 AND((b.account_id=p.account_id AND b.related_id=NULLIF($1::text,'')::uuid)OR(b.related_id=p.account_id AND b.account_id=NULLIF($1::text,'')::uuid)))`
	order := `s.created_at DESC,s.id`
	if pid == "" {
		order = `s.viewer_count DESC,s.started_at DESC NULLS LAST,s.id`
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_livestreams s JOIN owned_publishers p ON p.id=s.publisher_id WHERE `+where+` ORDER BY `+order+` LIMIT $3 OFFSET $4`, actor, pid, limit, offset)
	if e != nil {
		return nil, e
	}
	items := []live.Stream{}
	for rows.Next() {
		v, x := scan(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []live.Stream{}
	for i := range items {
		_, x := r.visible(ctx, tx, actor, &items[i])
		if errors.Is(x, live.ErrNotFound) || errors.Is(x, live.ErrForbidden) {
			continue
		}
		if x != nil {
			return nil, x
		}
		out = append(out, items[i])
	}
	return out, tx.Commit()
}
func (r *Repository) thumbnail(ctx context.Context, tx *sql.Tx, actor string, id *string) (*media.File, error) {
	if id == nil {
		return nil, nil
	}
	if r.media == nil {
		return nil, live.ErrUnavailable
	}
	f, e := r.media.ResolvePublicInTransaction(ctx, tx, actor, *id)
	if e != nil {
		return nil, live.ErrInvalid
	}
	if f.AccountID != actor || !strings.HasPrefix(f.MIMEType, "image/") || f.MIMEType == "image/svg+xml" {
		return nil, live.ErrForbidden
	}
	return &f, nil
}
