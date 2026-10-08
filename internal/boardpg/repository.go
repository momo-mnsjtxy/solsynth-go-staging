// SPDX-License-Identifier: AGPL-3.0-only
// Modified 2026-10-07. Uses licensed DysonNetwork-owned board migrations and
// snapshot at 4ee2d0cebb9da8284a92483fee6dce795bf6947c; see README.md.
// No excluded protocol schemas or generated bindings are used.
package boardpg

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"solsynth.local/go-migration/ports/passport/board"
)

var (
	ErrPolicyUnverified = errors.New("boardpg: upstream row filtering and audit policy unverified; adapter disabled")
	ErrInvalidID        = errors.New("boardpg: canonical lowercase UUID required")
	ErrAccountMismatch  = errors.New("boardpg: item or transaction belongs to another account")
	ErrInvalidPayload   = errors.New("boardpg: payload must be a JSON object or JSON null")
)

// Repository never creates/migrates tables and does not own db. The default
// constructor fails closed pending eligible evidence of shared audit/filter
// semantics. Every cooperating writer uses the same per-account advisory lock;
// concurrent legacy/external writers are explicitly unsupported.
type Repository struct {
	db         *sql.DB
	testPolicy bool
	now        func() time.Time
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// NewForCompatibilityTests enables ONLY an isolated candidate policy:
// deleted_at IS NULL reads/deletes; preserve creation time for retained active
// IDs; injected current time for new IDs/updates. This is not verified EF parity.
// A nil clock selects time.Now. Never wire this constructor into a public server.
func NewForCompatibilityTests(db *sql.DB, now func() time.Time) *Repository {
	return NewWithIndependentPolicy(db, now)
}

// NewWithIndependentPolicy explicitly selects the authored storage policy:
// active means deleted_at IS NULL, supplied IDs are preserved, retained active
// IDs retain created_at, updates use the injected UTC clock, and cooperating
// writers serialize by account. It is an independently selected architecture,
// not an emulation of excluded shared hooks. Authentication is outside this
// repository and must still fail closed unless a verified authority is supplied.
func NewWithIndependentPolicy(db *sql.DB, now func() time.Time) *Repository {
	if now == nil {
		now = time.Now
	}
	return &Repository{db: db, testPolicy: true, now: now}
}

var _ board.Repository = (*Repository)(nil)
var _ board.AccountTransactionRepository = (*Repository)(nil)

func validID(id string) bool {
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
func (r *Repository) ready(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || !r.testPolicy {
		return ErrPolicyUnverified
	}
	if r.db == nil {
		return errors.New("boardpg: database required")
	}
	if !validID(account) {
		return ErrInvalidID
	}
	return nil
}

// WithinAccount acquires its lock before the first snapshot at READ COMMITTED.
// The callback is invoked once, not retried, and receives no transaction wrapper
// interface. Long remote validators should use bounded request contexts.
func (r *Repository) WithinAccount(ctx context.Context, account string, fn func(board.Repository) error) error {
	if err := r.ready(ctx, account); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("boardpg: transaction callback required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('solsynth.board:' || $1::text, 0))`, account); err != nil {
		return err
	}
	scoped := &transactionRepository{tx: tx, account: account, now: r.now}
	if err = fn(scoped); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}
type record struct {
	item    board.Item
	created time.Time
}

const columns = `SELECT id::text, account_id::text, "order", kind, widget_key,
 custom_app_id::text, custom_app_widget_key, is_enabled, payload, created_at
 FROM account_board_items `

func read(ctx context.Context, q queryer, where string, args ...any) ([]record, error) {
	rows, err := q.QueryContext(ctx, columns+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []record{}
	for rows.Next() {
		var rec record
		var widget, app, key sql.NullString
		var payload []byte
		if err = rows.Scan(&rec.item.ID, &rec.item.AccountID, &rec.item.Order, &rec.item.Kind, &widget, &app, &key, &rec.item.IsEnabled, &payload, &rec.created); err != nil {
			return nil, err
		}
		if widget.Valid {
			v := widget.String
			rec.item.WidgetKey = &v
		}
		if app.Valid {
			v := app.String
			rec.item.CustomAppID = &v
		}
		if key.Valid {
			v := key.String
			rec.item.CustomAppWidgetKey = &v
		}
		rec.item.Payload, err = decodePayload(payload)
		if err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}
func decodePayload(raw []byte) (board.Payload, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidPayload
	}
	if value == nil {
		return nil, nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidPayload
	}
	return board.Payload(m), nil
}
func items(records []record) []board.Item {
	out := make([]board.Item, len(records))
	for i, r := range records {
		out[i] = r.item
	}
	return out
}
func (r *Repository) List(ctx context.Context, account string) ([]board.Item, error) {
	if err := r.ready(ctx, account); err != nil {
		return nil, err
	}
	records, err := read(ctx, r.db, `WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY "order"`, account)
	return items(records), err
}
func (r *Repository) FindByID(ctx context.Context, account, id string) (board.Item, error) {
	if err := r.ready(ctx, account); err != nil {
		return board.Item{}, err
	}
	return findID(ctx, r.db, account, id)
}
func (r *Repository) FindFirstWidget(ctx context.Context, account, app, key string) (board.Item, error) {
	if err := r.ready(ctx, account); err != nil {
		return board.Item{}, err
	}
	return findWidget(ctx, r.db, account, app, key)
}
func (r *Repository) Replace(ctx context.Context, account string, input []board.Item) error {
	return r.WithinAccount(ctx, account, func(repo board.Repository) error { return repo.Replace(ctx, account, input) })
}
func (r *Repository) Save(ctx context.Context, item board.Item) error {
	return r.WithinAccount(ctx, item.AccountID, func(repo board.Repository) error { return repo.Save(ctx, item) })
}

func findID(ctx context.Context, q queryer, account, id string) (board.Item, error) {
	if !validID(id) {
		return board.Item{}, ErrInvalidID
	}
	rows, err := read(ctx, q, `WHERE account_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL`, account, id)
	if err != nil {
		return board.Item{}, err
	}
	if len(rows) == 0 {
		return board.Item{}, board.ErrNotFound
	}
	return rows[0].item, nil
}
func findWidget(ctx context.Context, q queryer, account, app, key string) (board.Item, error) {
	if !validID(app) {
		return board.Item{}, ErrInvalidID
	}
	// Preserve the licensed service's ILIKE predicate and lack of ordering.
	// The service rechecks exact case-insensitive widget identity afterward.
	rows, err := read(ctx, q, `WHERE account_id=$1::uuid AND custom_app_id=$2::uuid
 AND custom_app_widget_key ILIKE $3 AND kind=1 AND deleted_at IS NULL LIMIT 1`, account, app, key)
	if err != nil {
		return board.Item{}, err
	}
	if len(rows) == 0 {
		return board.Item{}, board.ErrNotFound
	}
	return rows[0].item, nil
}

type transactionRepository struct {
	tx      *sql.Tx
	account string
	now     func() time.Time
}

var _ board.Repository = (*transactionRepository)(nil)

func (t *transactionRepository) scope(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if account != t.account {
		return ErrAccountMismatch
	}
	return nil
}
func (t *transactionRepository) List(ctx context.Context, account string) ([]board.Item, error) {
	if err := t.scope(ctx, account); err != nil {
		return nil, err
	}
	records, err := read(ctx, t.tx, `WHERE account_id=$1::uuid AND deleted_at IS NULL ORDER BY "order"`, account)
	return items(records), err
}
func (t *transactionRepository) FindByID(ctx context.Context, account, id string) (board.Item, error) {
	if err := t.scope(ctx, account); err != nil {
		return board.Item{}, err
	}
	return findID(ctx, t.tx, account, id)
}
func (t *transactionRepository) FindFirstWidget(ctx context.Context, account, app, key string) (board.Item, error) {
	if err := t.scope(ctx, account); err != nil {
		return board.Item{}, err
	}
	return findWidget(ctx, t.tx, account, app, key)
}
func (t *transactionRepository) Replace(ctx context.Context, account string, input []board.Item) error {
	if err := t.scope(ctx, account); err != nil {
		return err
	}
	for _, item := range input {
		if item.AccountID != account {
			return ErrAccountMismatch
		}
		if !validID(item.ID) || item.CustomAppID != nil && !validID(*item.CustomAppID) {
			return ErrInvalidID
		}
	}
	old, err := read(ctx, t.tx, `WHERE account_id=$1::uuid AND deleted_at IS NULL`, account)
	if err != nil {
		return err
	}
	created := make(map[string]time.Time, len(old))
	for _, r := range old {
		created[r.item.ID] = r.created
	}
	if _, err = t.tx.ExecContext(ctx, `DELETE FROM account_board_items WHERE account_id=$1::uuid AND deleted_at IS NULL`, account); err != nil {
		return err
	}
	now := t.now().UTC()
	for _, item := range input {
		raw, err := json.Marshal(item.Payload)
		if err != nil {
			return err
		}
		start, ok := created[item.ID]
		if !ok {
			start = now
		}
		if _, err = t.tx.ExecContext(ctx, `INSERT INTO account_board_items
 (id,account_id,"order",kind,widget_key,custom_app_id,custom_app_widget_key,is_enabled,payload,created_at,updated_at,deleted_at)
 VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7,$8,$9::jsonb,$10,$11,NULL)`,
			item.ID, account, item.Order, item.Kind, nullable(item.WidgetKey), nullable(item.CustomAppID), nullable(item.CustomAppWidgetKey), item.IsEnabled, string(raw), start, now); err != nil {
			return err
		}
	}
	return nil
}
func (t *transactionRepository) Save(ctx context.Context, item board.Item) error {
	if err := t.scope(ctx, item.AccountID); err != nil {
		return err
	}
	if !validID(item.ID) {
		return ErrInvalidID
	}
	raw, err := json.Marshal(item.Payload)
	if err != nil {
		return err
	}
	// Payload-only write prevents stale detached layout/ownership fields from
	// overwriting a concurrent layout. Service ownership checks run under lock.
	result, err := t.tx.ExecContext(ctx, `UPDATE account_board_items SET payload=$3::jsonb,updated_at=$4
 WHERE account_id=$1::uuid AND id=$2::uuid AND deleted_at IS NULL`,
		item.AccountID, item.ID, string(raw), t.now().UTC())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return board.ErrNotFound
	}
	return nil
}
func nullable(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
