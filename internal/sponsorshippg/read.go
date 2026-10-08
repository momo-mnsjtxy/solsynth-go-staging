// SPDX-License-Identifier: AGPL-3.0-only
package sponsorshippg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
)

func (r *Repository) read(ctx context.Context, actor string, required bool) (*sql.Tx, error) {
	if r == nil {
		return nil, s.ErrUnavailable
	}
	if actor == "" && required || actor != "" && !authority.ValidID(actor) {
		return nil, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		var enabled bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&enabled)
		if e != nil || !enabled {
			tx.Rollback()
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			return nil, authority.ErrInvalid
		}
	}
	return tx, nil
}
func (r *Repository) Total(ctx context.Context, actor, id string) (out wallet.Amount, e error) {
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = r.eligible(ctx, tx, actor, id); e != nil {
		return out, e
	}
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(amount),0)::text FROM owned_post_sponsorships WHERE post_id=$1::uuid AND expires_at>$2::timestamptz`, id, r.cfg.Now()).Scan(&out)
	if e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) History(ctx context.Context, actor, id string, q s.Page) (out []s.Bid, total int, e error) {
	out = []s.Bid{}
	q = q.Bounds()
	tx, e := r.read(ctx, actor, true)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	p, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
	if e != nil {
		return out, 0, mapped(e)
	}
	all := p.Publisher.AccountID != nil && *p.Publisher.AccountID == actor
	const where = ` FROM owned_post_sponsorships WHERE post_id=$1::uuid AND fulfilled_at IS NOT NULL AND ($2::boolean OR account_id=$3::uuid)`
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, id, all, actor).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,post_id::text,account_id::text,amount::text,expires_at,fulfilled_at`+where+` ORDER BY fulfilled_at DESC,id OFFSET $4 LIMIT $5`, id, all, actor, q.Offset, q.Take)
	if e != nil {
		return
	}
	for rows.Next() {
		var v s.Bid
		if e = rows.Scan(&v.ID, &v.PostID, &v.AccountID, &v.Amount, &v.ExpiresAt, &v.CreatedAt); e != nil {
			rows.Close()
			return
		}
		v.UpdatedAt = v.CreatedAt
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (r *Repository) aggregateSQL() string {
	return `SELECT p.id AS post_id,p.publisher_id,p.title,p.slug,COALESCE((SELECT sum(b.amount) FROM owned_post_sponsorships b WHERE b.post_id=p.id AND b.expires_at>$2::timestamptz),0) AS amount,(SELECT count(*) FROM owned_post_sponsorships b WHERE b.post_id=p.id AND b.expires_at>$2::timestamptz) AS bids` + postpg.ActivePostFrom + r.eligibleSQL()
}
func (r *Repository) Leaderboard(ctx context.Context, actor string, q s.Page) (out []s.Entry, total int, e error) {
	out = []s.Entry{}
	q = q.Bounds()
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	sqlBase := `WITH eligible AS (` + r.aggregateSQL() + `) `
	at := r.cfg.Now()
	if e = tx.QueryRowContext(ctx, sqlBase+`SELECT count(*) FROM eligible WHERE amount>0`, actor, at).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, sqlBase+`SELECT post_id::text,amount::text,bids FROM eligible WHERE amount>0 ORDER BY amount DESC,post_id OFFSET $3 LIMIT $4`, actor, at, q.Offset, q.Take)
	if e != nil {
		return
	}
	for rows.Next() {
		var v s.Entry
		if e = rows.Scan(&v.PostID, &v.TotalAmount, &v.BidCount); e != nil {
			rows.Close()
			return
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = tx.Commit()
	return
}
func (r *Repository) Current(ctx context.Context, actor string) (out s.Current, e error) {
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	var id string
	e = tx.QueryRowContext(ctx, `SELECT post_id::text FROM owned_post_sponsor_placements WHERE valid_from<=$1::timestamptz AND valid_until>$1::timestamptz ORDER BY valid_from DESC LIMIT 1`, r.cfg.Now()).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return out, tx.Commit()
	}
	if e != nil {
		return out, e
	}
	p, e := r.eligible(ctx, tx, actor, id)
	if errors.Is(e, s.ErrNotFound) {
		return out, tx.Commit()
	}
	if e != nil {
		return out, e
	}
	if r.associations != nil {
		if e = r.associations.Load(ctx, tx, actor, &p); e != nil {
			return out, e
		}
	}
	out = s.Current{Sponsored: true, Post: &s.SponsoredPost{Post: p, Sponsored: true}}
	return out, tx.Commit()
}
func (r *Repository) Ads(ctx context.Context, actor, ref string, members bool, q s.Page) (out []s.Ad, total int, e error) {
	out = []s.Ad{}
	q = q.Bounds()
	tx, e := r.read(ctx, actor, members)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	pub, e := r.publishers.VisiblePublisherInTransaction(ctx, tx, actor, ref)
	if e != nil {
		return out, 0, mapped(e)
	}
	if members {
		var allowed bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers u JOIN owned_publisher_members m ON m.publisher_id=u.id WHERE u.id=$2::uuid AND m.account_id=NULLIF($1::text,'')::uuid AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=0 AND `+postpg.PublisherMemberPredicate(r.visibility)+`)`, actor, pub.ID).Scan(&allowed)
		if e != nil {
			return out, 0, e
		}
		if !allowed {
			return out, 0, s.ErrForbidden
		}
	}
	sqlBase := `WITH eligible AS (` + r.aggregateSQL() + `),shown AS (SELECT x.*,COALESCE(i.shown_count,0) AS shown_count,i.last_shown_at,EXISTS(SELECT 1 FROM owned_post_sponsor_placements slot WHERE slot.post_id=x.post_id AND slot.valid_from<=$2::timestamptz AND slot.valid_until>$2::timestamptz) AS placed FROM eligible x LEFT JOIN owned_post_sponsor_impressions i ON i.post_id=x.post_id) `
	where := ` WHERE publisher_id=$3::uuid AND(amount>0 OR shown_count>0 OR placed)`
	at := r.cfg.Now()
	if e = tx.QueryRowContext(ctx, sqlBase+`SELECT count(*) FROM shown`+where, actor, at, pub.ID).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, sqlBase+`SELECT post_id::text,title,slug,amount::text,bids,placed,shown_count,last_shown_at,CASE WHEN (SELECT sum(amount) FROM eligible)>0 THEN (amount/(SELECT sum(amount) FROM eligible))::double precision ELSE 0 END FROM shown`+where+` ORDER BY amount DESC,shown_count DESC,post_id OFFSET $4 LIMIT $5`, actor, at, pub.ID, q.Offset, q.Take)
	if e != nil {
		return
	}
	for rows.Next() {
		var v s.Ad
		var chance float64
		if e = rows.Scan(&v.PostID, &v.Title, &v.Slug, &v.ActiveBidTotal, &v.BidCount, &v.IsCurrentlyPlaced, &v.ShownCount, &v.LastShownAt, &chance); e != nil {
			rows.Close()
			return
		}
		if !members {
			v.DisplayChance = &chance
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = tx.Commit()
	return
}

var _ s.Repository = (*Repository)(nil)
