// SPDX-License-Identifier: AGPL-3.0-only
package socialpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/social"
	"sort"
	"time"
)

//go:embed schema.sql
var Schema string

// New performs no I/O, migrations, account enrollment, quota grants or role seeding.
type TransactionGuard interface {
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db           *sql.DB
	guard        TransactionGuard
	requireGuard bool
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewWithAuthority(db *sql.DB, guard TransactionGuard) *Repository {
	return &Repository{db: db, guard: guard, requireGuard: true}
}

var _ social.Repository = (*Repository)(nil)
var _ social.RelationshipProvider = (*Repository)(nil)

type scanner interface{ Scan(...any) error }
type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func mapped(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return social.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return social.ErrConflict
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
func account(ctx context.Context, q querier, id string) (*accounts.Account, error) {
	a := &accounts.Account{}
	e := q.QueryRowContext(ctx, `SELECT id::text,name,nick,language,region,created_at FROM owned_authority_accounts WHERE id=$1 AND NOT disabled`, id).Scan(&a.ID, &a.Name, &a.Nick, &a.Language, &a.Region, &a.CreatedAt)
	return a, mapped(e)
}

// Lock account IDs in canonical order before domain rows. Every social writer
// uses this order, including friendship/block and invite/accept races.
func (r *Repository) write(ctx context.Context, actor string, ids []string, permissions []string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil {
		return social.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return social.ErrForbidden
	}
	ids = append(append([]string{}, ids...), actor)
	sort.Strings(ids)
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	prev := ""
	for _, id := range ids {
		if !authority.ValidID(id) {
			return social.ErrInvalid
		}
		if id == prev {
			continue
		}
		prev = id
		var disabled bool
		if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&disabled); e != nil {
			return mapped(e)
		}
		if disabled {
			return social.ErrForbidden
		}
	}
	if r.requireGuard {
		if r.guard == nil {
			return social.ErrUnavailable
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permissions...); e != nil {
			return e
		}
	}
	var now time.Time
	if e = tx.QueryRowContext(ctx, `SELECT statement_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e = fn(tx, now.UTC()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
func audit(ctx context.Context, tx *sql.Tx, actor, action, target string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_social_audit(actor_id,action,target_id,occurred_at)VALUES($1,$2,$3,$4)`, actor, action, target, now)
	return e
}
func (r *Repository) read(ctx context.Context, actor string, fn func(*sql.Tx) error) error {
	if r == nil || r.db == nil {
		return social.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return social.ErrForbidden
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if actor != "" {
		if _, e = account(ctx, tx, actor); e != nil {
			return e
		}
	}
	if e = fn(tx); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
