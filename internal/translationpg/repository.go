// SPDX-License-Identifier: AGPL-3.0-only
package translationpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/translation"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed schema.sql
var Schema string

type Config struct {
	Provider             translation.Provider
	Entitlement          translation.Entitlement
	TTL                  time.Duration
	MaxEntriesPerAccount int
	Now                  func() time.Time
}
type Repository struct {
	db     *sql.DB
	guard  *authoritypg.TransactionGuard
	config Config
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, c Config) (*Repository, error) {
	if db == nil || guard == nil || c.Entitlement == nil {
		return nil, translation.ErrInvalid
	}
	if c.TTL == 0 {
		c.TTL = 24 * time.Hour
	}
	if c.MaxEntriesPerAccount == 0 {
		c.MaxEntriesPerAccount = 1000
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.TTL < time.Minute || c.TTL > 24*time.Hour || c.MaxEntriesPerAccount < 1 || c.MaxEntriesPerAccount > 1000 {
		return nil, translation.ErrInvalid
	}
	if c.Provider != nil && (strings.TrimSpace(c.Provider.Namespace()) == "" || len(c.Provider.Namespace()) > 256) {
		return nil, translation.ErrInvalid
	}
	return &Repository{db, guard, c}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	var yes bool
	e := r.db.QueryRowContext(ctx, `SELECT bool_and(to_regclass(name)IS NOT NULL)FROM(VALUES('owned_translation_cache'),('commerce_v1.definitions'),('commerce_v1.subscriptions'))required(name)`).Scan(&yes)
	if e != nil {
		return e
	}
	if !yes {
		return translation.ErrUnavailable
	}
	return nil
}
func (r *Repository) begin(ctx context.Context, actor string) (*sql.Tx, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, translation.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return nil, translation.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return nil, e
	}
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e == nil {
		e = r.config.Entitlement.Require(ctx, tx, actor, r.config.Now().UTC())
	}
	if e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}
func (r *Repository) Translate(ctx context.Context, actor string, input translation.Request) (string, error) {
	req, e := input.Normalize()
	if e != nil {
		return "", e
	}
	if r == nil || r.config.Provider == nil {
		return "", translation.ErrUnavailable
	}
	b, _ := json.Marshal([]string{r.config.Provider.Namespace(), req.Source, req.Target, req.Text})
	sum := sha256.Sum256(b)
	key := hex.EncodeToString(sum[:])
	tx, e := r.begin(ctx, actor)
	if e != nil {
		return "", e
	}
	var result string
	e = tx.QueryRowContext(ctx, `SELECT result FROM owned_translation_cache WHERE account_id=$1 AND cache_key=$2 AND expires_at>$3`, actor, key, r.config.Now().UTC()).Scan(&result)
	if e == nil {
		if e = tx.Commit(); e != nil {
			return "", e
		}
		return result, nil
	}
	if e != sql.ErrNoRows {
		tx.Rollback()
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	// Outbound work holds no SQL lock. A second fresh proof/entitlement check
	// prevents revoked callers from receiving or persisting the result.
	result, e = r.config.Provider.Translate(ctx, req)
	if e != nil {
		return "", e
	}
	if !utf8.ValidString(result) || strings.TrimSpace(result) == "" || len(result) > 32000 {
		return "", translation.ErrProvider
	}
	tx, e = r.begin(ctx, actor)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	now := r.config.Now().UTC()
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_translation_cache WHERE account_id=$1 AND expires_at<=$2`, actor, now); e != nil {
		return "", e
	}
	var count int
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT count(*),EXISTS(SELECT 1 FROM owned_translation_cache WHERE account_id=$1 AND cache_key=$2) FROM owned_translation_cache WHERE account_id=$1`, actor, key).Scan(&count, &exists); e != nil {
		return "", e
	}
	if !exists && count >= r.config.MaxEntriesPerAccount {
		return "", translation.ErrBusy
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_translation_cache(account_id,cache_key,result,created_at,expires_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id,cache_key) DO UPDATE SET result=EXCLUDED.result,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at`, actor, key, result, now, now.Add(r.config.TTL))
	if e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return result, nil
}
