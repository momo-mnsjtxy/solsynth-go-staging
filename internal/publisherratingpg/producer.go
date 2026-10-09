// SPDX-License-Identifier: AGPL-3.0-only
package publisherratingpg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	wallet "solsynth.local/go-migration/ports/wallet/accounts"
	"time"
)

// This explicitly enabled local worker records canonical reaction counts once
// per publisher/prior UTC day. It never synthesizes awards, XP or social credit.
func (r *Repository) ProcessDay(ctx context.Context, day time.Time, limit int) (int, error) {
	return r.processDay(ctx, "", day, limit)
}

// ProcessPublisherDay scopes an operator retry to one canonical publisher while
// preserving the same unique daily receipt. It cannot add a second reward.
func (r *Repository) ProcessPublisherDay(ctx context.Context, publisher string, day time.Time) (int, error) {
	if !authority.ValidID(publisher) {
		return 0, s.ErrInvalid
	}
	return r.processDay(ctx, publisher, day, 1)
}
func (r *Repository) processDay(ctx context.Context, publisher string, day time.Time, limit int) (processed int, e error) {
	if r == nil || !r.cfg.ReactionOnly && !r.cfg.PaidAwards {
		return 0, s.ErrUnavailable
	}
	today := r.cfg.Now().UTC().Truncate(24 * time.Hour)
	if !day.Equal(day.UTC().Truncate(24*time.Hour)) || day.Before(r.cfg.EarliestDay) || !day.Before(today) || day.Before(today.AddDate(0, 0, -31)) {
		return 0, s.ErrInvalid
	}
	if limit < 1 || limit > 1000 {
		return 0, s.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, "publisher-reaction-rating:"+day.Format("2006-01-02")); e != nil {
		return
	}
	// All input counts share one SQL snapshot. Private/realm-linked content and
	// disabled reaction authors cannot leak through public activity deltas.
	rows, e := tx.QueryContext(ctx, `WITH eligible AS(SELECT p.id,p.publisher_id,p.awarded_score`+postpg.ActivePostFrom+` AND ($5::text='' OR p.publisher_id=NULLIF($5::text,'')::uuid) AND p.created_at>=$2::timestamptz AND p.created_at<$3::timestamptz AND p.realm_id IS NULL AND p.visibility=0 AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND p.shadowbanned_at IS NULL AND u.shadowbanned_at IS NULL AND `+postpg.DetailPredicate(r.visibility)+`),candidates AS(SELECT DISTINCT publisher_id FROM eligible e WHERE NOT EXISTS(SELECT 1 FROM owned_publisher_rating_records done WHERE done.publisher_id=e.publisher_id AND done.period_start=$2::timestamptz) ORDER BY publisher_id LIMIT $4),counts AS(SELECT c.publisher_id,count(r.id)FILTER(WHERE r.attitude=0)AS positive,count(r.id)FILTER(WHERE r.attitude=2)AS negative FROM candidates c JOIN eligible p ON p.publisher_id=c.publisher_id LEFT JOIN owned_post_reactions r ON r.post_id=p.id AND EXISTS(SELECT 1 FROM owned_authority_accounts author WHERE author.id=r.account_id AND NOT author.disabled) GROUP BY c.publisher_id),award_counts AS(SELECT publisher_id,CASE WHEN $6::boolean THEN sum(awarded_score) ELSE 0 END AS score FROM eligible GROUP BY publisher_id)SELECT c.publisher_id::text,c.positive,c.negative,a.score::text,trunc(c.positive-c.negative+a.score/10)::bigint FROM counts c JOIN award_counts a ON a.publisher_id=c.publisher_id ORDER BY c.publisher_id`, "", day, day.AddDate(0, 0, 1), limit, publisher, r.cfg.PaidAwards)
	if e != nil {
		return
	}
	type metric struct {
		pub                string
		positive, negative int64
		score              wallet.Amount
		delta              int64
	}
	all := []metric{}
	for rows.Next() {
		var m metric
		if e = rows.Scan(&m.pub, &m.positive, &m.negative, &m.score, &m.delta); e != nil {
			rows.Close()
			return
		}
		if m.positive > 1000000 || m.negative > 1000000 || m.delta < -1000000 || m.delta > 1000000 {
			rows.Close()
			return 0, s.ErrUnavailable
		}
		all = append(all, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	for _, m := range all {
		rid, x := id()
		if x != nil {
			return 0, x
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_rating_records(id,publisher_id,period_start,policy,delta,positive_count,negative_count,created_at,awarded_score)VALUES($1::uuid,$2::uuid,$3::timestamptz,$8::text,$4::bigint,$5::bigint,$6::bigint,$7::timestamptz,$9::numeric)`, rid, m.pub, day, m.delta, m.positive, m.negative, r.cfg.Now(), r.PolicyName(), m.score); e != nil {
			return 0, e
		}
		processed++
	}
	return processed, tx.Commit()
}
func (r *Repository) ProcessYesterday(ctx context.Context, limit int) (int, error) {
	if r == nil {
		return 0, s.ErrUnavailable
	}
	return r.ProcessDay(ctx, r.cfg.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1), limit)
}
