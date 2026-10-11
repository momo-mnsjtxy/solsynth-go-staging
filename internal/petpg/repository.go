// SPDX-License-Identifier: AGPL-3.0-only
package petpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/pets"
	"time"
)

//go:embed schema.sql
var Schema string

type TransactionGuard interface {
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
}
type Repository struct {
	db    *sql.DB
	guard TransactionGuard
}

func New(db *sql.DB, guard TransactionGuard) *Repository { return &Repository{db: db, guard: guard} }

var _ pets.Repository = (*Repository)(nil)

// within shares authority's canonical account lock and original opaque proof.
// It rechecks revocation before commit. No unguarded repository path exists.
func (r *Repository) within(ctx context.Context, actor string, fn func(*sql.Tx) error) error {
	if !authority.ValidID(actor) {
		return authority.ErrInvalid
	}
	if r == nil || r.db == nil || r.guard == nil {
		return pets.ErrUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := r.guard.Authenticate(ctx, r.db, tx, actor)
	if err != nil {
		return err
	}
	if p.Account.ID != actor {
		return authority.ErrInvalid
	}
	if err = fn(tx); err != nil {
		return err
	}
	p, err = r.guard.Authenticate(ctx, r.db, tx, actor)
	if err != nil {
		return err
	}
	if p.Account.ID != actor {
		return authority.ErrInvalid
	}
	return tx.Commit()
}
func clock(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), err
}
func (r *Repository) Get(ctx context.Context, actor string) (out pets.State, err error) {
	err = r.within(ctx, actor, func(tx *sql.Tx) error {
		var raw []byte
		if e := tx.QueryRowContext(ctx, `SELECT state FROM owned_pets WHERE account_id=$1`, actor).Scan(&raw); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return pets.ErrNotFound
			}
			return e
		}
		if e := json.Unmarshal(raw, &out); e != nil {
			return e
		}
		now, e := clock(ctx, tx)
		if e != nil {
			return e
		}
		out = pets.Settle(out, now)
		return nil
	})
	return
}
func (r *Repository) Mutate(ctx context.Context, actor string, c pets.Command) (out pets.State, err error) {
	if err = pets.Validate(c); err != nil {
		return
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	err = r.within(ctx, actor, func(tx *sql.Tx) error {
		// The guard owns the canonical account lock even before the first pet exists.
		// Competing adoptions, requests, debits and quest claims therefore serialize.
		var oldHash string
		var response []byte
		e := tx.QueryRowContext(ctx, `SELECT request_hash,response FROM owned_pet_commands WHERE account_id=$1 AND request_id=$2`, actor, c.RequestID).Scan(&oldHash, &response)
		if e == nil {
			if oldHash != hash {
				return pets.ErrConflict
			}
			return json.Unmarshal(response, &out)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var before *pets.State
		var state []byte
		var version, coins int64
		e = tx.QueryRowContext(ctx, `SELECT state,version,coins FROM owned_pets WHERE account_id=$1 FOR UPDATE`, actor).Scan(&state, &version, &coins)
		if e == nil {
			before = &pets.State{}
			if e = json.Unmarshal(state, before); e != nil {
				return e
			}
			if before.Version != version || before.Coins != coins {
				return pets.ErrUnavailable
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		now, e := clock(ctx, tx)
		if e != nil {
			return e
		}
		out, e = pets.Apply(before, c, now)
		if e != nil {
			return e
		}
		response, e = json.Marshal(out)
		if e != nil {
			return e
		}
		if before == nil {
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_pets(account_id,version,coins,state,updated_at) VALUES($1,$2,$3,$4,$5)`, actor, out.Version, out.Coins, response, out.UpdatedAt)
		} else {
			var result sql.Result
			result, e = tx.ExecContext(ctx, `UPDATE owned_pets SET version=$2,coins=$3,state=$4,updated_at=$5 WHERE account_id=$1 AND version=$6`, actor, out.Version, out.Coins, response, out.UpdatedAt, c.ExpectedVersion)
			if e == nil {
				var n int64
				n, e = result.RowsAffected()
				if e == nil && n != 1 {
					return pets.ErrConflict
				}
			}
		}
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_pet_commands(account_id,request_id,request_hash,kind,response,coin_delta,balance_after,version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, actor, c.RequestID, hash, c.Kind, response, out.Coins-coins, out.Coins, out.Version, now)
		return e
	})
	return
}

// Ready verifies both table relations and the schema columns actually used.
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil {
		return pets.ErrUnavailable
	}
	var ready bool
	if err := r.db.QueryRowContext(ctx, `SELECT to_regclass('owned_pets') IS NOT NULL AND to_regclass('owned_pet_commands') IS NOT NULL`).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return pets.ErrUnavailable
	}
	rows, err := r.db.QueryContext(ctx, `SELECT p.account_id,p.version,p.coins,p.state,p.updated_at,c.request_id,c.request_hash,c.kind,c.response,c.coin_delta,c.balance_after,c.version,c.created_at FROM owned_pets p LEFT JOIN owned_pet_commands c ON false WHERE false`)
	if err != nil {
		return err
	}
	return rows.Close()
}
