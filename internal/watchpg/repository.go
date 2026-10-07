// SPDX-License-Identifier: AGPL-3.0-only
// PostgreSQL integration for the licensed Sphere post-watch contract.
package watchpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/sphere"
)

var (
	ErrInvalidAccountID        = errors.New("account ID must be a canonical lowercase UUID")
	ErrInvalidSource           = errors.New("invalid watch source")
	ErrDuplicateLivePreference = errors.New("duplicate live account/source preference")
	ErrAccountMismatch         = errors.New("preference belongs to another account")
)

// Repository does not own the database handle and never applies DDL. The caller
// supplies a configured database/sql driver, normally pgx/stdlib. All writers
// must follow this adapter's account advisory-lock protocol; legacy C# writers
// do not and must not run concurrently against the same preference records.
type Repository struct {
	db *sql.DB
	// Only the explicitly named compatibility-test constructor sets this.
	// It is immutable after construction and never enabled by the command.
	softDeleteOnlyWatcherCandidate bool
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }

var _ sphere.WatchPreferenceRepository = (*Repository)(nil)
var _ sphere.WatchPreferencePatcher = (*Repository)(nil)

func validAccount(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func sourceNumber(s sphere.WatchSource) (int, error) {
	switch s {
	case sphere.WatchBookmark:
		return 0, nil
	case sphere.WatchReaction:
		return 1, nil
	case sphere.WatchReply:
		return 2, nil
	default:
		return 0, ErrInvalidSource
	}
}
func sourceName(n int) (sphere.WatchSource, error) {
	switch n {
	case 0:
		return sphere.WatchBookmark, nil
	case 1:
		return sphere.WatchReaction, nil
	case 2:
		return sphere.WatchReply, nil
	default:
		return "", ErrInvalidSource
	}
}
func (r *Repository) ready(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("PostgreSQL database is required")
	}
	return ctx.Err()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type record struct {
	id         string
	preference sphere.WatchPreference
}

const selectColumns = `SELECT id::text, account_id::text, source, notify_reactions, notify_replies, notify_chains, notify_forwards, notify_edits FROM post_watch_preferences `

func load(ctx context.Context, q queryer, ids []string, lock bool) ([]record, error) {
	result := []record{}
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(ids))
	marks := make([]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		if !validAccount(id) {
			return nil, ErrInvalidAccountID
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		args = append(args, id)
		marks = append(marks, fmt.Sprintf("$%d::uuid", len(args)))
	}
	query := selectColumns + "WHERE deleted_at IS NULL AND account_id IN (" + strings.Join(marks, ",") + ") ORDER BY account_id, source, id"
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unique := make(map[string]bool)
	for rows.Next() {
		var row record
		var number int
		p := &row.preference
		if err := rows.Scan(&row.id, &p.AccountID, &number, &p.NotifyReactions, &p.NotifyReplies, &p.NotifyChains, &p.NotifyForwards, &p.NotifyEdits); err != nil {
			return nil, err
		}
		p.Source, err = sourceName(number)
		if err != nil {
			return nil, err
		}
		key := p.AccountID + ":" + string(p.Source)
		if unique[key] {
			return nil, ErrDuplicateLivePreference
		}
		unique[key] = true
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *Repository) LoadPreferences(ctx context.Context, ids []string) ([]sphere.WatchPreference, error) {
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := load(ctx, r.db, ids, false)
	if err != nil {
		return nil, err
	}
	result := make([]sphere.WatchPreference, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.preference)
	}
	return result, nil
}

// transact serializes the whole read/merge/write per canonical account across
// cooperating processes. The advisory lock is acquired before reading at READ
// COMMITTED so a waiter sees the preceding writer's committed state.
func (r *Repository) transact(ctx context.Context, account string, fn func(*sql.Tx, map[sphere.WatchSource]record) error) ([]sphere.WatchPreference, error) {
	if !validAccount(account) {
		return nil, ErrInvalidAccountID
	}
	if err := r.ready(ctx); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('solsynth.watch:' || $1::text, 0))`, account); err != nil {
		return nil, err
	}
	rows, err := load(ctx, tx, []string{account}, true)
	if err != nil {
		return nil, err
	}
	stored := make(map[sphere.WatchSource]record, len(rows))
	for _, row := range rows {
		stored[row.preference.Source] = row
	}
	if err = fn(tx, stored); err != nil {
		return nil, err
	}
	effective := make([]sphere.WatchPreference, 0, 3)
	for _, source := range []sphere.WatchSource{sphere.WatchBookmark, sphere.WatchReaction, sphere.WatchReply} {
		row, ok := stored[source]
		if ok {
			effective = append(effective, row.preference)
		} else {
			effective = append(effective, sphere.DefaultWatchPreference(account, source))
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return effective, nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func write(ctx context.Context, tx *sql.Tx, stored map[sphere.WatchSource]record, p sphere.WatchPreference) error {
	number, err := sourceNumber(p.Source)
	if err != nil {
		return err
	}
	old, exists := stored[p.Source]
	now := time.Now().UTC()
	if exists {
		result, err := tx.ExecContext(ctx, `UPDATE post_watch_preferences SET notify_reactions=$1,notify_replies=$2,notify_chains=$3,notify_forwards=$4,notify_edits=$5,updated_at=$6 WHERE id=$7::uuid AND account_id=$8::uuid AND source=$9 AND deleted_at IS NULL`, p.NotifyReactions, p.NotifyReplies, p.NotifyChains, p.NotifyForwards, p.NotifyEdits, now, old.id, p.AccountID, number)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("live watch preference changed during update")
		}
		old.preference = p
		stored[p.Source] = old
		return nil
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO post_watch_preferences(id,account_id,source,notify_reactions,notify_replies,notify_chains,notify_forwards,notify_edits,created_at,updated_at,deleted_at) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$9,NULL)`, id, p.AccountID, number, p.NotifyReactions, p.NotifyReplies, p.NotifyChains, p.NotifyForwards, p.NotifyEdits, now)
	if err != nil {
		return err
	}
	stored[p.Source] = record{id: id, preference: p}
	return nil
}

func (r *Repository) SavePreferences(ctx context.Context, account string, rows []sphere.WatchPreference) error {
	seen := make(map[sphere.WatchSource]bool)
	for _, p := range rows {
		if p.AccountID != account {
			return ErrAccountMismatch
		}
		if _, err := sourceNumber(p.Source); err != nil {
			return err
		}
		if seen[p.Source] {
			return ErrDuplicateLivePreference
		}
		seen[p.Source] = true
	}
	_, err := r.transact(ctx, account, func(tx *sql.Tx, stored map[sphere.WatchSource]record) error {
		for _, p := range rows {
			if err := write(ctx, tx, stored, p); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func (r *Repository) PatchPreferences(ctx context.Context, account string, updates []sphere.WatchPreferenceUpdate) ([]sphere.WatchPreference, error) {
	seen := make(map[sphere.WatchSource]bool)
	for _, u := range updates {
		if _, err := sourceNumber(u.Source); err != nil {
			return nil, err
		}
		if seen[u.Source] {
			return nil, ErrDuplicateLivePreference
		}
		seen[u.Source] = true
	}
	return r.transact(ctx, account, func(tx *sql.Tx, stored map[sphere.WatchSource]record) error {
		for _, u := range updates {
			old, exists := stored[u.Source]
			p := old.preference
			if !exists {
				p = sphere.DefaultWatchPreference(account, u.Source)
			}
			if u.NotifyReactions != nil {
				p.NotifyReactions = *u.NotifyReactions
			}
			if u.NotifyReplies != nil {
				p.NotifyReplies = *u.NotifyReplies
			}
			if u.NotifyChains != nil {
				p.NotifyChains = *u.NotifyChains
			}
			if u.NotifyForwards != nil {
				p.NotifyForwards = *u.NotifyForwards
			}
			if u.NotifyEdits != nil {
				p.NotifyEdits = *u.NotifyEdits
			}
			if err := write(ctx, tx, stored, p); err != nil {
				return err
			}
		}
		return nil
	})
}
