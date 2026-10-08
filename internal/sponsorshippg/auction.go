// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

import (
	"context"
	"crypto/rand"
	"database/sql"
	"math/big"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"strings"
	"time"
)

// Exact thousandths preserve proportional odds without float monetary rounding.
func WeightedIndex(weights []wallet.Amount, draw func(*big.Int) (*big.Int, error)) (int, error) {
	if len(weights) == 0 || len(weights) > 10000 {
		return 0, s.ErrInvalid
	}
	total := new(big.Int)
	units := make([]*big.Int, len(weights))
	for i, v := range weights {
		if v.Sign() <= 0 {
			return 0, s.ErrInvalid
		}
		n, ok := new(big.Int).SetString(strings.ReplaceAll(v.String(), ".", ""), 10)
		if !ok {
			return 0, s.ErrInvalid
		}
		units[i] = n
		total.Add(total, n)
	}
	if total.Sign() == 0 {
		return 0, s.ErrInvalid
	}
	if draw == nil {
		draw = func(n *big.Int) (*big.Int, error) { return rand.Int(rand.Reader, n) }
	}
	roll, e := draw(new(big.Int).Set(total))
	if e != nil {
		return 0, e
	}
	if roll == nil || roll.Sign() < 0 || roll.Cmp(total) >= 0 {
		return 0, s.ErrInvalid
	}
	for i, v := range units {
		if roll.Cmp(v) < 0 {
			return i, nil
		}
		roll.Sub(roll, v)
	}
	return 0, s.ErrInvalid
}

// Auction creates only an hourly placement from settled bids, never funding.
func (r *Repository) Auction(ctx context.Context) (created bool, e error) {
	if r == nil {
		return false, s.ErrUnavailable
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	at := r.cfg.Now().UTC().Truncate(time.Microsecond)
	hour := at.Truncate(time.Hour)
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, "sponsor-auction:"+hour.Format(time.RFC3339)); e != nil {
		return
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_post_sponsor_placements WHERE valid_from=$1::timestamptz)`, hour).Scan(&exists); e != nil {
		return
	}
	if exists {
		return false, tx.Commit()
	}
	rows, e := tx.QueryContext(ctx, `WITH eligible AS (`+r.aggregateSQL()+`) SELECT post_id::text,amount::text FROM eligible WHERE amount>0 ORDER BY post_id LIMIT 10001`, "", at)
	if e != nil {
		return
	}
	ids := []string{}
	weights := []wallet.Amount{}
	for rows.Next() {
		var id string
		var a wallet.Amount
		if e = rows.Scan(&id, &a); e != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
		weights = append(weights, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	if len(ids) > 10000 {
		return false, s.ErrUnavailable
	}
	if len(ids) == 0 {
		return false, tx.Commit()
	}
	index, e := WeightedIndex(weights, nil)
	if e != nil {
		return
	}
	winner := ids[index]
	before, e := observe(ctx, tx, winner)
	if e != nil {
		return false, e
	}
	if e = lockAuctionScope(ctx, tx, before, winner); e != nil {
		return false, mapped(e)
	}
	after, e := observe(ctx, tx, winner)
	if e != nil {
		return false, e
	}
	if after != before {
		return false, s.ErrConflict
	}
	if _, e = r.eligible(ctx, tx, "", winner); e != nil {
		return false, e
	}
	id, e := uid()
	if e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_sponsor_placements(id,post_id,total_amount,valid_from,valid_until,created_at)VALUES($1::uuid,$2::uuid,$3::numeric,$4::timestamptz,$5::timestamptz,$6::timestamptz)`, id, winner, weights[index], hour, hour.Add(time.Hour), at)
	if e != nil {
		return
	}
	if e = audit(ctx, tx, "", winner, "", "placement", at); e != nil {
		return
	}
	return true, tx.Commit()
}
func lockAuctionScope(ctx context.Context, tx *sql.Tx, o scope, id string) error {
	owner := o.Owner
	if owner == "" {
		owner = o.RealmOwner
	}
	var held string
	if owner != "" {
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid AND NOT disabled FOR SHARE NOWAIT`, owner).Scan(&held); e != nil {
			return e
		}
	}
	if o.Realm != "" {
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, o.Realm).Scan(&held); e != nil {
			return e
		}
	}
	if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, o.Publisher).Scan(&held); e != nil {
		return e
	}
	return tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1::uuid AND deleted_at IS NULL FOR SHARE NOWAIT`, id).Scan(&held)
}

// This host-only callback follows an actual sponsored projection. It has no
// HTTP route and accepts no client impression count or entitlement override.
func (r *Repository) RecordDisplay(ctx context.Context, actor, id string) (e error) {
	if r == nil {
		return s.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	before, e := observe(ctx, tx, id)
	if e != nil {
		return e
	}
	if e = lockAuctionScope(ctx, tx, before, id); e != nil {
		return mapped(e)
	}
	after, e := observe(ctx, tx, id)
	if e != nil {
		return e
	}
	if after != before {
		return s.ErrConflict
	}
	if actor != "" {
		var active bool
		if e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&active); e != nil || !active {
			return authority.ErrInvalid
		}
	}
	if _, e = r.eligible(ctx, tx, actor, id); e != nil {
		return e
	}
	at := r.cfg.Now()
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_post_sponsor_placements WHERE post_id=$1::uuid AND valid_from<=$2::timestamptz AND valid_until>$2::timestamptz)`, id, at).Scan(&active); e != nil {
		return e
	}
	if !active {
		return s.ErrConflict
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_sponsor_impressions(post_id,shown_count,last_shown_at)VALUES($1::uuid,1,$2::timestamptz)ON CONFLICT(post_id)DO UPDATE SET shown_count=owned_post_sponsor_impressions.shown_count+1,last_shown_at=GREATEST(owned_post_sponsor_impressions.last_shown_at,excluded.last_shown_at)`, id, at)
	if e != nil {
		return e
	}
	return tx.Commit()
}
