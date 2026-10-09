// SPDX-License-Identifier: AGPL-3.0-only
package activitypg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
)

//go:embed schema.sql
var Schema string

type Guard interface {
	Preflight(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
	StoredAuthenticationEligible(context.Context, *sql.DB, *sql.Tx, string, string, int64, int64) (bool, error)
}
type Repository struct {
	db    *sql.DB
	guard Guard
}

func New(db *sql.DB, g Guard) *Repository { return &Repository{db: db, guard: g} }

var _ activities.Repository = (*Repository)(nil)

func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil {
		return activities.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,document,session_id,revision FROM owned_presence_activities LIMIT 0;SELECT id,total_seconds,session_count FROM owned_activity_catalog LIMIT 0;SELECT activity_id,revision FROM owned_activity_events LIMIT 0;SELECT id,kind,presence_version FROM owned_friend_events LIMIT 0;SELECT event_id,recipient_id FROM owned_friend_audiences LIMIT 0;SELECT account_id,revision FROM owned_friend_presence_versions LIMIT 0`)
	return e
}
func uuid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func clock(ctx context.Context, tx *sql.Tx) (t time.Time, e error) {
	e = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&t)
	return t.UTC(), e
}
func (r *Repository) check(ctx context.Context, tx *sql.Tx, actor, permission string) (authority.Principal, error) {
	if actor == "" {
		return authority.Principal{}, nil
	}
	if r.guard == nil {
		return authority.Principal{}, authority.ErrUnavailable
	}
	if permission != "" {
		return r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	return r.guard.Authenticate(ctx, r.db, tx, actor)
}

// Resolve names only after the original proof preflight, then acquire the same
// sorted account locks used by canonical relationship, disable and policy writes.
func (r *Repository) begin(ctx context.Context, actor, reference, permission string) (tx *sql.Tx, target string, p authority.Principal, e error) {
	if r == nil || r.db == nil {
		return nil, "", p, activities.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, "", p, authority.ErrInvalid
	}
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	ok := false
	defer func() {
		if !ok {
			_ = tx.Rollback()
		}
	}()
	if actor != "" {
		if r.guard == nil {
			e = authority.ErrUnavailable
			return
		}
		if permission != "" {
			p, e = r.guard.Preflight(ctx, r.db, tx, actor, permission)
		} else {
			p, e = r.guard.Preflight(ctx, r.db, tx, actor)
		}
		if e != nil {
			return
		}
	}
	if reference == "" || len(reference) > 200 {
		e = activities.ErrAccountNotFound
		return
	}
	col := "name"
	if authority.ValidID(reference) {
		col = "id::text"
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE `+col+`=$1`, reference).Scan(&target)
	if errors.Is(e, sql.ErrNoRows) {
		e = activities.ErrAccountNotFound
	}
	if e != nil {
		return
	}
	ids := []string{target}
	if actor != "" && actor != target {
		ids = append(ids, actor)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var found string
		e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, id).Scan(&found)
		if e != nil {
			return
		}
	}
	if col == "name" {
		var current string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE name=$1`, reference).Scan(&current); e != nil || current != target {
			if errors.Is(e, sql.ErrNoRows) || e == nil {
				e = activities.ErrAccountNotFound
			}
			return
		}
	}
	p, e = r.check(ctx, tx, actor, permission)
	if e != nil {
		return
	}
	if e = activeAccount(ctx, tx, target); e != nil {
		return
	}
	ok = true
	return
}
func activeAccount(ctx context.Context, tx *sql.Tx, id string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`, id).Scan(&active)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !active {
		return activities.ErrAccountNotFound
	}
	return e
}
func audience(ctx context.Context, tx *sql.Tx, actor, target string) (allow, friend bool, e error) {
	if e = activeAccount(ctx, tx, target); e != nil {
		return
	}
	if actor == target {
		return true, true, nil
	}
	var blocked bool
	if actor != "" {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE ((account_id=$1::uuid AND related_id=$2::uuid)OR(account_id=$2::uuid AND related_id=$1::uuid))AND status=-100),EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1::uuid AND related_id=$2::uuid AND status>=100)AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$2::uuid AND related_id=$1::uuid AND status>=100)`, target, actor).Scan(&blocked, &friend)
		if e != nil {
			return
		}
		if blocked {
			return false, false, activities.ErrAccountNotFound
		}
	}
	var visibility string
	var invisible bool
	e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT status_visibility FROM owned_presence_preferences WHERE account_id=$1::uuid),'public'),COALESCE((SELECT type=3 FROM owned_presence_statuses WHERE account_id=$1::uuid AND(cleared_at IS NULL OR cleared_at>clock_timestamp())ORDER BY created_at DESC,id LIMIT 1),false)`, target).Scan(&visibility, &invisible)
	if e != nil {
		return
	}
	return !invisible && (visibility == "public" || visibility == "friends" && friend), friend, nil
}

type row struct {
	activities.Activity
	session                  string
	version, epoch, revision int64
}

const fields = `id::text,account_id::text,manual_id,catalog_id::text,document,visibility,session_id::text,account_version,session_epoch,lease_minutes,lease_expires_at,started_at,ended_at,created_at,updated_at,deleted_at,revision`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (v row, e error) {
	var data []byte
	e = s.Scan(&v.ID, &v.AccountID, &v.ManualID, &v.CatalogID, &data, &v.Visibility, &v.session, &v.version, &v.epoch, &v.LeaseMinutes, &v.LeaseExpiresAt, &v.StartedAt, &v.EndedAt, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.revision)
	if e == nil {
		e = json.Unmarshal(data, &v.Content)
	}
	activities.NormalizeContent(&v.Content)
	return
}
func encoded(v any) []byte { b, _ := json.Marshal(v); return b }
func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (r *Repository) eligible(ctx context.Context, tx *sql.Tx, v row) (bool, error) {
	if r.guard == nil {
		return false, authority.ErrUnavailable
	}
	return r.guard.StoredAuthenticationEligible(ctx, r.db, tx, v.AccountID, v.session, v.version, v.epoch)
}
func (r *Repository) event(ctx context.Context, tx *sql.Tx, v row, action string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_activity_events(account_id,activity_id,revision,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::bigint,$4::text,clock_timestamp())`, v.AccountID, v.ID, v.revision, action)
	if e != nil {
		return e
	}
	return friendeventpg.CaptureActivity(ctx, tx, friendeventpg.Source{Owner: v.AccountID, Session: v.session, Activity: v.ID, Version: v.version, Epoch: v.epoch, Revision: v.revision}, v.Visibility)
}

// Hash references resolve only through canonical artwork associations. Writes
// require the current account's ready file; they never transfer file ownership.
func artwork(ctx context.Context, tx *sql.Tx, actor string, c *activities.Content, touch bool) error {
	hashes := []string{}
	for _, p := range []*string{c.LargeImage, c.SmallImage} {
		if p != nil && strings.HasPrefix(*p, "sha256:") {
			hashes = append(hashes, *p)
		}
	}
	sort.Strings(hashes)
	for i, hash := range hashes {
		if i > 0 && hashes[i-1] == hash {
			continue
		}
		// Hold the association and file stable through the final projection. The
		// account lock precedes these locks, matching canonical artwork writes.
		if _, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('presence-artwork:' || $1::text,0))`, hash); e != nil {
			return e
		}
		var id, owner string
		var live bool
		e := tx.QueryRowContext(ctx, `SELECT f.id::text,f.account_id::text,f.state='ready' AND f.sha256=substring(a.hash FROM 8) AND f.mime_type LIKE 'image/%' AND a.last_referenced_at>clock_timestamp()-(a.retention_seconds * interval '1 second') FROM owned_presence_artworks a JOIN owned_media_files f ON f.id=a.file_id WHERE a.hash=$1::text`, hash).Scan(&id, &owner, &live)
		if errors.Is(e, sql.ErrNoRows) || e == nil && (!live || owner != actor) {
			if touch {
				return activities.ErrArtwork
			}
			clearImage(c, hash)
			continue
		}
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `SELECT id FROM owned_media_files WHERE id=$1::uuid FOR SHARE`, id); e != nil {
			return e
		}
		e = tx.QueryRowContext(ctx, `SELECT f.state='ready' AND f.account_id=$2::uuid AND f.sha256=substring(a.hash FROM 8) AND f.mime_type LIKE 'image/%' AND a.last_referenced_at>clock_timestamp()-(a.retention_seconds * interval '1 second') FROM owned_presence_artworks a JOIN owned_media_files f ON f.id=a.file_id WHERE a.hash=$1::text`, hash, actor).Scan(&live)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !live {
			if touch {
				return activities.ErrArtwork
			}
			clearImage(c, hash)
			continue
		}
		if e != nil {
			return e
		}
		if touch {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_presence_artworks SET last_referenced_at=clock_timestamp() WHERE hash=$1::text`, hash); e != nil {
				return e
			}
		}
		if e = activeAccount(ctx, tx, owner); e != nil {
			if errors.Is(e, activities.ErrAccountNotFound) {
				if touch {
					return activities.ErrArtwork
				}
				clearImage(c, hash)
			} else {
				return e
			}
		}
	}
	return nil
}
func clearImage(c *activities.Content, hash string) {
	for _, p := range []**string{&c.LargeImage, &c.SmallImage} {
		if *p != nil && **p == hash {
			*p = nil
		}
	}
}
