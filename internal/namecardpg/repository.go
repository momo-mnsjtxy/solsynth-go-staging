// SPDX-License-Identifier: AGPL-3.0-only
package namecardpg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/passport/namecards"
	"solsynth.local/go-migration/ports/passport/social"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	commerce "solsynth.local/go-migration/ports/wallet/commerce"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed schema.sql
var Schema string

type Commerce interface {
	NameCardRequest(context.Context, *sql.DB, *sql.Tx, string, string, string, string, string) (string, error)
	CreateNameCardOrder(context.Context, *sql.DB, *sql.Tx, string, string, string, string, wallet.Amount, time.Time) (commerce.Order, error)
}
type AccountRenamer interface {
	RenameAccountForCard(context.Context, *sql.DB, *sql.Tx, string, string, time.Time) (string, error)
	RenamePublisherForCard(context.Context, *sql.DB, *sql.Tx, string, string, string, time.Time) (string, error)
}
type RealmRenamer interface {
	RenameRealmForCard(context.Context, *sql.DB, *sql.Tx, string, string, string, time.Time) (string, error)
}
type Options struct {
	Enabled                          bool
	PricePoints                      wallet.Amount
	PointsCurrency, PayeeWalletID    string
	PurchasePeriodDays, MaxPurchases int
	Now                              func() time.Time
}
type Repository struct {
	db       *sql.DB
	guard    *authoritypg.TransactionGuard
	commerce Commerce
	accounts AccountRenamer
	realms   RealmRenamer
	options  Options
}

func New(db *sql.DB, g *authoritypg.TransactionGuard, c Commerce, a AccountRenamer, r RealmRenamer, o Options) (*Repository, error) {
	if db == nil || g == nil {
		return nil, namecards.ErrUnavailable
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Enabled && (o.PricePoints.Sign() <= 0 || o.PurchasePeriodDays < 1 || o.PurchasePeriodDays > 366 || o.MaxPurchases < 1 || o.MaxPurchases > 100 || !authority.ValidID(o.PayeeWalletID) || len(o.PointsCurrency) == 0 || len(o.PointsCurrency) > 128 || !utf8.ValidString(o.PointsCurrency) || strings.TrimSpace(o.PointsCurrency) != o.PointsCurrency || strings.ContainsAny(o.PointsCurrency, "\x00\r\n")) {
		return nil, namecards.ErrInvalid
	}
	return &Repository{db, g, c, a, r, o}, nil
}
func (r *Repository) BindAuthority(ctx context.Context, p authority.Principal) context.Context {
	return r.guard.Bind(ctx, p)
}
func (r *Repository) Ready(ctx context.Context) error {
	var index sql.NullString
	if e := r.db.QueryRowContext(ctx, `SELECT to_regclass('owned_authority_accounts_folded_name')::text`).Scan(&index); e != nil {
		return e
	}
	if !index.Valid {
		return namecards.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,fulfilled_at,consumed_at FROM owned_name_card_purchases LIMIT 0;SELECT transaction_id FROM owned_name_card_receipts LIMIT 0;SELECT resource_id FROM commerce_v1.idempotency LIMIT 0`)
	return e
}
func (r *Repository) now() time.Time { return r.options.Now().UTC().Truncate(time.Microsecond) }
func (r *Repository) within(ctx context.Context, actor string, fn func(*sql.Tx, time.Time) error) error {
	if r == nil || r.db == nil || r.guard == nil || !authority.ValidID(actor) {
		return namecards.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = r.guard.Authenticate(ctx, r.db, tx, actor); e != nil {
		return e
	}
	if e = fn(tx, r.now()); e != nil {
		return mapped(e)
	}
	return tx.Commit()
}
func mapped(e error) error {
	if errors.Is(e, accounts.ErrForbidden) || errors.Is(e, social.ErrForbidden) {
		return authority.ErrForbidden
	}
	if errors.Is(e, accounts.ErrInvalid) || errors.Is(e, social.ErrInvalid) {
		return namecards.ErrInvalid
	}
	if errors.Is(e, accounts.ErrConflict) || errors.Is(e, social.ErrConflict) || errors.Is(e, accounts.ErrNotFound) || errors.Is(e, social.ErrNotFound) {
		return namecards.ErrUse
	}
	if errors.Is(e, sql.ErrNoRows) {
		return namecards.ErrNotFound
	}
	if errors.Is(e, commerce.ErrIdempotency) {
		return namecards.ErrIdempotency
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return namecards.ErrUse
	}
	return e
}
func newID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

const fields = `id::text,account_id::text,order_id::text,amount,fulfilled_at,consumed_at,target_type,target_id::text,old_name,new_name,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (v namecards.Purchase, e error) {
	e = row.Scan(&v.ID, &v.AccountID, &v.OrderID, &v.Amount, &v.FulfilledAt, &v.ConsumedAt, &v.TargetType, &v.TargetID, &v.OldName, &v.NewName, &v.CreatedAt, &v.UpdatedAt)
	return v, mapped(e)
}
func purchase(ctx context.Context, tx *sql.Tx, actor, id string, locked bool) (namecards.Purchase, error) {
	suffix := ""
	if locked {
		suffix = " FOR UPDATE"
	}
	return scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_name_card_purchases WHERE account_id=$1::uuid AND id=$2::uuid`+suffix, actor, id))
}
func audit(ctx context.Context, tx *sql.Tx, actor, id, action string, now time.Time) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_name_card_audit(account_id,purchase_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::timestamptz)`, actor, id, action, now)
	return e
}

func (r *Repository) Purchase(ctx context.Context, actor, key string) (out namecards.OrderResponse, e error) {
	if !namecards.ValidKey(key) {
		return out, namecards.ErrInvalid
	}
	if r.commerce == nil {
		return out, namecards.ErrUnavailable
	}
	e = r.within(ctx, actor, func(tx *sql.Tx, now time.Time) error {
		fp := fingerprint(namecards.Product)
		prior, e := r.commerce.NameCardRequest(ctx, r.db, tx, actor, key, "order", fp, "")
		if e != nil {
			return e
		}
		if prior != "" {
			p, e := purchase(ctx, tx, actor, prior, false)
			if e != nil {
				return e
			}
			out = namecards.OrderResponse{PurchaseID: p.ID, OrderID: p.OrderID, Amount: p.Amount}
			return nil
		}
		if !r.options.Enabled {
			return namecards.ErrPurchase
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_name_card_purchases WHERE account_id=$1::uuid AND created_at>=$2::timestamptz`, actor, now.Add(-time.Duration(r.options.PurchasePeriodDays)*24*time.Hour)).Scan(&count); e != nil {
			return e
		}
		if count >= r.options.MaxPurchases {
			return namecards.ErrPurchase
		}
		pid, e := newID()
		if e != nil {
			return e
		}
		o, e := r.commerce.CreateNameCardOrder(ctx, r.db, tx, actor, pid, r.options.PointsCurrency, r.options.PayeeWalletID, r.options.PricePoints, now)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_name_card_purchases(id,account_id,order_id,amount,points_currency,payee_wallet_id,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::numeric,$5::text,$6::uuid,$7::timestamptz,$7::timestamptz)`, pid, actor, o.ID, o.Amount, o.Currency, o.PayeeWalletID, now); e != nil {
			return e
		}
		if _, e = r.commerce.NameCardRequest(ctx, r.db, tx, actor, key, "order", fp, pid); e != nil {
			return e
		}
		if e = audit(ctx, tx, actor, pid, "card.order", now); e != nil {
			return e
		}
		out = namecards.OrderResponse{PurchaseID: pid, OrderID: o.ID, Amount: o.Amount}
		return nil
	})
	return
}
func (r *Repository) List(ctx context.Context, actor string, take, offset int) (out []namecards.Purchase, total int64, e error) {
	out = []namecards.Purchase{}
	if take < 1 || take > 200 || offset < 0 || offset > 1000000 {
		return nil, 0, namecards.ErrInvalid
	}
	e = r.within(ctx, actor, func(tx *sql.Tx, _ time.Time) error {
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_name_card_purchases WHERE account_id=$1::uuid`, actor).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT `+fields+` FROM owned_name_card_purchases WHERE account_id=$1::uuid ORDER BY created_at DESC,id DESC LIMIT $2::integer OFFSET $3::integer`, actor, take, offset)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scan(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}
