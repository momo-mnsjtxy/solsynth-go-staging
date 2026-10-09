// SPDX-License-Identifier: AGPL-3.0-only
package walletpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/wallet/accounts"
	"sort"
	"time"
)

//go:embed schema.sql
var SchemaSQL string

//go:embed pin_schema.sql
var PINSchemaSQL string

// Repository implements the independent wallet_v1 policy. It does not load a
// legacy database or create schema. Only cooperating writers are supported.
type Repository struct {
	payments    PaymentAuthorization
	db          *sql.DB
	now         func() time.Time
	authority   *authoritypg.TransactionGuard
	realmWrites RealmWritePolicy
	realmReads  RealmReadPolicy
	exchange    *accounts.ExchangePolicy
}

func New(db *sql.DB) *Repository { return &Repository{db: db, now: time.Now} }
func NewWithAuthority(db *sql.DB, guard *authoritypg.TransactionGuard) *Repository {
	r := New(db)
	if guard == nil {
		guard = authoritypg.NewTransactionGuard(nil, nil)
	}
	r.authority = guard
	return r
}
func NewWithClock(db *sql.DB, clock func() time.Time) *Repository { return New(db).WithClock(clock) }

// WithClock injects a trusted server clock without replacing authority or payment
// adapters. It performs no I/O and does not accept an HTTP-provided timestamp.
func (r *Repository) WithClock(clock func() time.Time) *Repository {
	if clock != nil {
		r.now = clock
	}
	return r
}

var _ accounts.Store = (*Repository)(nil)

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func uuid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func publicID() (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	for i := range b { // rejection sampling avoids modulo bias
		for {
			var n [1]byte
			if _, e := rand.Read(n[:]); e != nil {
				return "", e
			}
			if int(n[0]) < 256-256%len(chars) {
				b[i] = chars[int(n[0])%len(chars)]
				break
			}
		}
	}
	return "DNW-" + string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:]), nil
}
func mapErr(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return accounts.ErrNotFound
	}
	return e
}
func (r *Repository) begin(ctx context.Context, ro bool) (*sql.Tx, error) {
	if r == nil || r.db == nil {
		return nil, accounts.ErrUnavailable
	}
	level := sql.LevelReadCommitted
	if ro {
		level = sql.LevelRepeatableRead
	}
	return r.db.BeginTx(ctx, &sql.TxOptions{Isolation: level, ReadOnly: ro})
}
func lock(ctx context.Context, q querier, key string) error {
	_, e := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,987324))`, key)
	return e
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const walletCols = `id::text,account_id::text,realm_id::text,name,is_primary,public_id,created_at,updated_at,deleted_at`

type scanner interface{ Scan(...any) error }

func scanWallet(row scanner) (accounts.Wallet, error) {
	w := accounts.Wallet{Pockets: []accounts.Pocket{}}
	e := row.Scan(&w.ID, &w.AccountID, &w.RealmID, &w.Name, &w.IsPrimary, &w.PublicID, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt)
	return w, mapErr(e)
}
func pockets(ctx context.Context, q querier, w *accounts.Wallet) error {
	rows, e := q.QueryContext(ctx, `SELECT id::text,wallet_id::text,currency,amount::text,held_amount::text,amount::text,created_at,updated_at,deleted_at FROM wallet_v1.pockets WHERE wallet_id=$1 AND deleted_at IS NULL ORDER BY currency,id`, w.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var p accounts.Pocket
		if e = rows.Scan(&p.ID, &p.WalletID, &p.Currency, &p.Amount, &p.HeldAmount, &p.AvailableAmount, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); e != nil {
			return e
		}
		w.Pockets = append(w.Pockets, p)
	}
	return rows.Err()
}
func wallet(ctx context.Context, q querier, id string, forUpdate bool) (accounts.Wallet, error) {
	sql := `SELECT ` + walletCols + ` FROM wallet_v1.wallets WHERE id=$1 AND deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	return scanWallet(q.QueryRowContext(ctx, sql, id))
}
func (r *Repository) get(ctx context.Context, where string, arg string) (accounts.Wallet, error) {
	tx, e := r.begin(ctx, true)
	if e != nil {
		return accounts.Wallet{}, e
	}
	defer tx.Rollback()
	w, e := scanWallet(tx.QueryRowContext(ctx, `SELECT `+walletCols+` FROM wallet_v1.wallets WHERE deleted_at IS NULL AND `+where, arg))
	if e != nil {
		return w, e
	}
	if e = pockets(ctx, tx, &w); e != nil {
		return w, e
	}
	e = tx.Commit()
	return w, e
}
func (r *Repository) GetWallet(ctx context.Context, id string) (accounts.Wallet, error) {
	if !accounts.ValidID(id) {
		return accounts.Wallet{}, accounts.ErrInvalid
	}
	return r.get(ctx, `id=$1`, id)
}
func (r *Repository) DefaultWallet(ctx context.Context, account string) (accounts.Wallet, error) {
	if !accounts.ValidID(account) {
		return accounts.Wallet{}, accounts.ErrInvalid
	}
	return r.get(ctx, `account_id=$1 ORDER BY is_primary DESC,created_at,id LIMIT 1`, account)
}
func (r *Repository) WalletByPublicID(ctx context.Context, id string) (accounts.Wallet, error) {
	return r.get(ctx, `public_id=$1`, id)
}
func (r *Repository) ListWallets(ctx context.Context, account string, realms []string) ([]accounts.Wallet, error) {
	tx, e := r.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if realms == nil {
		realms = []string{}
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+walletCols+` FROM wallet_v1.wallets WHERE deleted_at IS NULL AND (account_id=$1 OR realm_id::text=ANY($2::text[])) ORDER BY account_id NULLS LAST,realm_id,is_primary DESC,created_at,id`, account, realms)
	if e != nil {
		return nil, e
	}
	list := []accounts.Wallet{}
	for rows.Next() {
		w, x := scanWallet(rows)
		if x != nil {
			rows.Close()
			return nil, x
		}
		list = append(list, w)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range list {
		if e = pockets(ctx, tx, &list[i]); e != nil {
			return nil, e
		}
	}
	e = tx.Commit()
	return list, e
}
func (r *Repository) CreateWallet(ctx context.Context, owner accounts.Owner, name string) (accounts.Wallet, error) {
	if !owner.Valid() || name == "" {
		return accounts.Wallet{}, accounts.ErrInvalid
	}
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.Wallet{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, owner, "", ""); e != nil {
		return accounts.Wallet{}, e
	}
	if e = lock(ctx, tx, "owner:"+owner.Key()); e != nil {
		return accounts.Wallet{}, e
	}
	now := r.now().UTC()
	w, e := createWalletInTransaction(ctx, tx, owner, name, now)
	if e != nil {
		return w, e
	}
	return w, tx.Commit()
}
func (r *Repository) SetDefault(ctx context.Context, id, account string) (accounts.Wallet, error) {
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.Wallet{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, accounts.Owner{AccountID: account}, account, ""); e != nil {
		return accounts.Wallet{}, e
	}
	if e = lock(ctx, tx, "owner:account:"+account); e != nil {
		return accounts.Wallet{}, e
	}
	w, e := wallet(ctx, tx, id, true)
	if e != nil {
		return w, e
	}
	if w.AccountID == nil || *w.AccountID != account {
		return w, accounts.ErrForbidden
	}
	now := r.now().UTC()
	if !w.IsPrimary {
		if _, e = tx.ExecContext(ctx, `UPDATE wallet_v1.wallets SET is_primary=false,updated_at=$2 WHERE account_id=$1 AND deleted_at IS NULL AND is_primary`, account, now); e != nil {
			return w, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE wallet_v1.wallets SET is_primary=true,updated_at=$2 WHERE id=$1`, id, now); e != nil {
			return w, e
		}
		w.IsPrimary = true
		w.UpdatedAt = now
		if e = event(ctx, tx, "wallet.default.changed", id, w, now); e != nil {
			return w, e
		}
	}
	if e = pockets(ctx, tx, &w); e != nil {
		return w, e
	}
	return w, tx.Commit()
}
func (r *Repository) SetPublicID(ctx context.Context, id string, owner accounts.Owner, enable bool) (accounts.Wallet, error) {
	tx, e := r.begin(ctx, false)
	if e != nil {
		return accounts.Wallet{}, e
	}
	defer tx.Rollback()
	if e = r.writeAuthority(ctx, tx, owner, "", accounts.PermissionPublicID); e != nil {
		return accounts.Wallet{}, e
	}
	w, e := wallet(ctx, tx, id, true)
	if e != nil {
		return w, e
	}
	if w.Owner() != owner {
		return w, accounts.ErrForbidden
	}
	now := r.now().UTC()
	changed := false
	if enable && w.PublicID == nil { // serialize ID allocation; unique index remains final backstop
		if e = lock(ctx, tx, "public-id"); e != nil {
			return w, e
		}
		for i := 0; i < 16; i++ {
			value, x := publicID()
			if x != nil {
				return w, x
			}
			var exists bool
			if x = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_v1.wallets WHERE public_id=$1)`, value).Scan(&exists); x != nil {
				return w, x
			}
			if !exists {
				w.PublicID = &value
				changed = true
				break
			}
		}
		if !changed {
			return w, accounts.ErrConflict
		}
	} else if !enable && w.PublicID != nil {
		w.PublicID = nil
		changed = true
	}
	if changed {
		if _, e = tx.ExecContext(ctx, `UPDATE wallet_v1.wallets SET public_id=$2,updated_at=$3 WHERE id=$1`, id, w.PublicID, now); e != nil {
			return w, e
		}
		w.UpdatedAt = now
		if e = event(ctx, tx, "wallet.public_id.changed", id, w, now); e != nil {
			return w, e
		}
	}
	if e = pockets(ctx, tx, &w); e != nil {
		return w, e
	}
	return w, tx.Commit()
}
func event(ctx context.Context, q querier, kind, id string, payload any, now time.Time) error {
	eventID, e := uuid()
	if e != nil {
		return e
	}
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO wallet_v1.outbox(event_id,event_type,aggregate_id,payload,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, eventID, kind, id, string(b), now)
	return e
}
func lockWallets(ctx context.Context, q querier, ids ...string) (map[string]accounts.Wallet, error) {
	sort.Strings(ids)
	m := map[string]accounts.Wallet{}
	for _, id := range ids {
		if _, ok := m[id]; ok {
			continue
		}
		w, e := wallet(ctx, q, id, true)
		if e != nil {
			return nil, e
		}
		m[id] = w
	}
	return m, nil
}
func ensurePocket(ctx context.Context, q querier, walletID, currency string, now time.Time) error {
	id, e := uuid()
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO wallet_v1.pockets(id,wallet_id,currency,created_at,updated_at) VALUES($1,$2,$3,$4,$4) ON CONFLICT(wallet_id,currency) DO NOTHING`, id, walletID, currency, now)
	if e != nil {
		return e
	}
	var active bool
	e = q.QueryRowContext(ctx, `SELECT deleted_at IS NULL FROM wallet_v1.pockets WHERE wallet_id=$1 AND currency=$2`, walletID, currency).Scan(&active)
	if e != nil {
		return e
	}
	if !active {
		return accounts.ErrConflict
	}
	return nil
}
func changed(result sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return accounts.ErrInsufficient
	}
	return nil
}
func pocketEvent(ctx context.Context, q querier, id, currency string, now time.Time) error {
	var p accounts.Pocket
	e := q.QueryRowContext(ctx, `SELECT id::text,wallet_id::text,currency,amount::text,held_amount::text,amount::text,created_at,updated_at,deleted_at FROM wallet_v1.pockets WHERE wallet_id=$1 AND currency=$2 AND deleted_at IS NULL`, id, currency).Scan(&p.ID, &p.WalletID, &p.Currency, &p.Amount, &p.HeldAmount, &p.AvailableAmount, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
	if e != nil {
		return e
	}
	return event(ctx, q, "wallet.pocket.updated", id, p, now)
}
