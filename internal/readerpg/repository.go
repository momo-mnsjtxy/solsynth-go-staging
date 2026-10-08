// SPDX-License-Identifier: AGPL-3.0-only
package readerpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/reader"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Policy     reader.Policy
	Fetcher    reader.Fetcher // nil permits cache reads and guarded invalidation only
	TTL        time.Duration
	MaxEntries int
	Now        func() time.Time
}
type Repository struct {
	db     *sql.DB
	guard  *authoritypg.TransactionGuard
	config Config
}

var _ reader.Repository = (*Repository)(nil)

func New(db *sql.DB, guard *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || guard == nil || !c.Policy.Configured() {
		return nil, reader.ErrInvalid
	}
	if c.TTL == 0 {
		c.TTL = 24 * time.Hour
	}
	if c.MaxEntries == 0 {
		c.MaxEntries = 5000
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.TTL < time.Minute || c.TTL > 24*time.Hour || c.MaxEntries < 1 || c.MaxEntries > 10000 {
		return nil, reader.ErrInvalid
	}
	return &Repository{db: db, guard: guard, config: c}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func cacheKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}
func (r *Repository) Ready(ctx context.Context) error {
	var ready bool
	if r == nil || r.db == nil {
		return reader.ErrUnavailable
	}
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name) IS NOT NULL) FROM (VALUES('owned_reader_cache_state'),('owned_reader_cache'),('owned_reader_cache_audit')) required(name)`).Scan(&ready)
	if e != nil {
		return e
	}
	if !ready {
		return reader.ErrUnavailable
	}
	var generation int64
	return r.db.QueryRowContext(ctx, `SELECT generation FROM owned_reader_cache_state WHERE singleton`).Scan(&generation)
}
func (r *Repository) validate(v reader.Link) error {
	canonical, e := r.config.Policy.Normalize(v.URL)
	if e != nil || canonical != v.URL || v.Title == "" || len(v.Title) > 2048 {
		return reader.ErrFetch
	}
	for _, raw := range []*string{v.ImageURL, v.FaviconURL} {
		if raw != nil {
			canonical, e := r.config.Policy.Normalize(*raw)
			if e != nil || canonical != *raw {
				return reader.ErrFetch
			}
		}
	}
	for _, field := range []struct {
		v     *string
		limit int
	}{{v.Description, 8192}, {v.SiteName, 1024}, {v.ContentType, 512}, {v.Author, 1024}} {
		if field.v != nil && len(*field.v) > field.limit {
			return reader.ErrLimit
		}
	}
	return nil
}
func (r *Repository) Preview(ctx context.Context, raw string) (reader.Link, error) {
	if r == nil || r.db == nil {
		return reader.Link{}, reader.ErrUnavailable
	}
	url, e := r.config.Policy.Normalize(raw)
	if e != nil {
		return reader.Link{}, e
	}
	key := cacheKey(url)
	var generation int64
	var data []byte
	var expires *time.Time
	e = r.db.QueryRowContext(ctx, `SELECT s.generation,c.preview,c.expires_at FROM owned_reader_cache_state s LEFT JOIN owned_reader_cache c ON c.cache_key=$1 WHERE s.singleton`, key).Scan(&generation, &data, &expires)
	if e != nil {
		return reader.Link{}, e
	}
	if expires != nil && expires.After(r.config.Now()) {
		var v reader.Link
		if json.Unmarshal(data, &v) != nil || r.validate(v) != nil {
			return reader.Link{}, reader.ErrUnavailable
		}
		return v, nil
	}
	if r.config.Fetcher == nil {
		return reader.Link{}, reader.ErrUnavailable
	}
	// No database transaction/row lock remains held during outbound work.
	v, e := r.config.Fetcher.Fetch(ctx, url)
	if e != nil {
		return reader.Link{}, e
	}
	if e = r.validate(v); e != nil {
		return reader.Link{}, e
	}
	data, e = json.Marshal(v)
	if e != nil || len(data) > 65536 {
		return reader.Link{}, reader.ErrLimit
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return reader.Link{}, e
	}
	defer tx.Rollback()
	var actual int64
	if e = tx.QueryRowContext(ctx, `SELECT generation FROM owned_reader_cache_state WHERE singleton FOR UPDATE`).Scan(&actual); e != nil {
		return reader.Link{}, e
	}
	// Every administrative invalidation advances the durable generation. It
	// fences all in-flight fills, while a URL invalidation retains other hits.
	if actual != generation {
		return reader.Link{}, reader.ErrConflict
	}
	now := r.config.Now().UTC()
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_reader_cache WHERE expires_at<=$1`, now); e != nil {
		return reader.Link{}, e
	}
	var count int
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT count(*),EXISTS(SELECT 1 FROM owned_reader_cache WHERE cache_key=$1) FROM owned_reader_cache`, key).Scan(&count, &exists); e != nil {
		return reader.Link{}, e
	}
	if !exists && count >= r.config.MaxEntries {
		return reader.Link{}, reader.ErrBusy
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_reader_cache(cache_key,url,preview,updated_at,expires_at) VALUES($1,$2,$3::jsonb,$4,$5) ON CONFLICT(cache_key) DO UPDATE SET url=EXCLUDED.url,preview=EXCLUDED.preview,updated_at=EXCLUDED.updated_at,expires_at=EXCLUDED.expires_at`, key, url, data, now, now.Add(r.config.TTL))
	if e != nil {
		return reader.Link{}, e
	}
	if e = tx.Commit(); e != nil {
		return reader.Link{}, e
	}
	return v, nil
}
func (r *Repository) Invalidate(ctx context.Context, actor, raw string) error {
	if r == nil {
		return reader.ErrUnavailable
	}
	url, e := r.config.Policy.Normalize(raw)
	if e != nil {
		return e
	}
	key := cacheKey(url)
	return r.invalidate(ctx, actor, &key)
}
func (r *Repository) InvalidateAll(ctx context.Context, actor string) error {
	return r.invalidate(ctx, actor, nil)
}
func (r *Repository) invalidate(ctx context.Context, actor string, key *string) error {
	if r == nil || r.db == nil || r.guard == nil {
		return reader.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return reader.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "cache.scrap"); e != nil {
		return e
	}
	var generation int64
	if e = tx.QueryRowContext(ctx, `UPDATE owned_reader_cache_state SET generation=generation+1 WHERE singleton RETURNING generation`).Scan(&generation); e != nil {
		return e
	}
	action := "invalidate_all"
	if key == nil {
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_reader_cache`)
	} else {
		action = "invalidate_url"
		_, e = tx.ExecContext(ctx, `DELETE FROM owned_reader_cache WHERE cache_key=$1`, *key)
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_reader_cache_audit(actor_id,action,cache_key,generation,occurred_at) VALUES($1,$2,$3,$4,$5)`, actor, action, key, generation, r.config.Now().UTC())
	if e != nil {
		return e
	}
	return tx.Commit()
}
