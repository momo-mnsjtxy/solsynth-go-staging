// SPDX-License-Identifier: AGPL-3.0-only
package publisherratingpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"math"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	a "solsynth.local/go-migration/ports/passport/accounts"
	rating "solsynth.local/go-migration/ports/sphere/publisherrating"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	"time"
)

//go:embed schema.sql
var Schema string

type Config struct {
	ReactionOnly bool
	PaidAwards   bool
	EarliestDay  time.Time
	Now          func() time.Time
}
type Repository struct {
	db         *sql.DB
	visibility postpg.VisibilityProvider
	publishers *accountpg.Repository
	cfg        Config
}

func New(db *sql.DB, v postpg.VisibilityProvider, c Config) (*Repository, error) {
	if db == nil || v == nil {
		return nil, s.ErrUnavailable
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.ReactionOnly && c.PaidAwards {
		return nil, s.ErrInvalid
	}
	if (c.ReactionOnly || c.PaidAwards) && (c.EarliestDay.IsZero() || !c.EarliestDay.Equal(c.EarliestDay.UTC().Truncate(24*time.Hour))) {
		return nil, s.ErrInvalid
	}
	realms, _ := v.(accountpg.RealmProvider)
	return &Repository{db, v, accountpg.New(db).WithRealms(realms), c}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return s.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT publisher_id,delta,period_start,awarded_score FROM owned_publisher_rating_records LIMIT 0;SELECT awarded_score FROM owned_text_posts LIMIT 0;SELECT post_id,attitude FROM owned_post_reactions LIMIT 0`)
	return e
}
func missing(e error) error {
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, a.ErrNotFound) {
		return s.ErrNotFound
	}
	return e
}
func (r *Repository) read(ctx context.Context, actor string) (*sql.Tx, error) {
	if r == nil {
		return nil, s.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, authority.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, e
	}
	if actor != "" {
		var ok bool
		e = tx.QueryRowContext(ctx, `SELECT NOT disabled FROM owned_authority_accounts WHERE id=$1::uuid`, actor).Scan(&ok)
		if e != nil || !ok {
			tx.Rollback()
			return nil, authority.ErrInvalid
		}
	}
	return tx, nil
}
func (r *Repository) visible() string {
	return ` FROM owned_publishers u WHERE u.deleted_at IS NULL AND u.type IN(0,1) AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=u.account_id AND NOT owner.disabled)) AND ` + postpg.PublisherPredicate(r.visibility) + ` AND NOT (` + r.visibility.PostBlockedSQL() + `)`
}
func (r *Repository) ranking() string {
	return `WITH visible AS(SELECT u.id,u.name,u.nick,(100+COALESCE((SELECT sum(delta) FROM owned_publisher_rating_records r WHERE r.publisher_id=u.id),0))::bigint AS rating` + r.visible() + `),ranked AS(SELECT *,rank()OVER(ORDER BY rating DESC)::integer AS rank,count(*)OVER()::integer AS total FROM visible) `
}
func enrich(o *rating.Overview) {
	o.Policy = rating.Policy
	if o.TotalPublishers > 0 {
		p := float64(o.TotalPublishers-o.Rank+1) / float64(o.TotalPublishers) * 100
		o.Percentile = math.RoundToEven(p*10) / 10
		o.Grade = rating.Grade(p, o.Rank)
	}
}
func (r *Repository) target(ctx context.Context, tx *sql.Tx, actor, ref string) (a.Publisher, error) {
	p, e := r.publishers.VisiblePublisherInTransaction(ctx, tx, actor, ref)
	if e != nil {
		return p, missing(e)
	}
	var ok bool
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+r.visible()+` AND u.id=$2::uuid)`, actor, p.ID).Scan(&ok)
	if e != nil {
		return p, e
	}
	if !ok {
		return p, s.ErrNotFound
	}
	return p, nil
}
func (r *Repository) Rating(ctx context.Context, actor, ref string) (int64, error) {
	v, e := r.Overview(ctx, actor, ref)
	return v.Rating, e
}
func (r *Repository) Overview(ctx context.Context, actor, ref string) (out rating.Overview, e error) {
	tx, e := r.read(ctx, actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	p, e := r.target(ctx, tx, actor, ref)
	if e != nil {
		return out, e
	}
	e = tx.QueryRowContext(ctx, r.ranking()+`SELECT rating,rank,total FROM ranked WHERE id=$2::uuid`, actor, p.ID).Scan(&out.Rating, &out.Rank, &out.TotalPublishers)
	if e != nil {
		return out, missing(e)
	}
	enrich(&out)
	out.Policy = r.PolicyName()
	out.AwardScoreIncluded = r.cfg.PaidAwards
	return out, tx.Commit()
}
func (r *Repository) Leaderboard(ctx context.Context, actor string, q s.Page) (out []rating.Entry, total int, e error) {
	out = []rating.Entry{}
	q = q.Bounds()
	tx, e := r.read(ctx, actor)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+r.visible(), actor).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, r.ranking()+`SELECT id::text,name,nick,rating,rank,total FROM ranked ORDER BY rating DESC,id OFFSET $2 LIMIT $3`, actor, q.Offset, q.Take)
	if e != nil {
		return
	}
	for rows.Next() {
		var v rating.Entry
		var n int
		if e = rows.Scan(&v.PublisherID, &v.Name, &v.Nick, &v.Rating, &v.Rank, &n); e != nil {
			rows.Close()
			return
		}
		o := rating.Overview{Rating: v.Rating, Rank: v.Rank, TotalPublishers: n}
		enrich(&o)
		v.Percentile = o.Percentile
		v.Grade = o.Grade
		v.Policy = r.PolicyName()
		v.AwardScoreIncluded = r.cfg.PaidAwards
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
func (r *Repository) History(ctx context.Context, actor, ref string, q s.Page) (out rating.History, total int, e error) {
	out.Records = []rating.Record{}
	q = q.Bounds()
	tx, e := r.read(ctx, actor)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	p, e := r.target(ctx, tx, actor, ref)
	if e != nil {
		return out, 0, e
	}
	if actor != "" {
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publishers u JOIN owned_publisher_members m ON m.publisher_id=u.id WHERE u.id=$2::uuid AND m.account_id=NULLIF($1::text,'')::uuid AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=0 AND `+postpg.PublisherMemberPredicate(r.visibility)+`)`, actor, p.ID).Scan(&out.IsMember); e != nil {
			return
		}
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid AND delta<>0`, p.ID).Scan(&total); e != nil {
		return
	}
	rows, e := tx.QueryContext(ctx, `SELECT id::text,publisher_id::text,delta,created_at,period_start,policy FROM owned_publisher_rating_records WHERE publisher_id=$1::uuid AND delta<>0 ORDER BY period_start DESC,id OFFSET $2 LIMIT $3`, p.ID, q.Offset, q.Take)
	if e != nil {
		return
	}
	for rows.Next() {
		var v rating.Record
		if e = rows.Scan(&v.ID, &v.PublisherID, &v.Delta, &v.CreatedAt, &v.PeriodStart, &v.Policy); e != nil {
			rows.Close()
			return
		}
		v.UpdatedAt = v.CreatedAt
		v.ReasonType = "publishers.rewards"
		v.Reason = "Publisher activity (" + v.Policy + ") for " + v.PeriodStart.UTC().Format("2006-01-02")
		out.Records = append(out.Records, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	e = tx.Commit()
	return
}
func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	x := hex.EncodeToString(b)
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:], nil
}

var _ rating.Repository = (*Repository)(nil)

func (r *Repository) PolicyName() string {
	if r != nil && r.cfg.PaidAwards {
		return "reaction_and_paid_awards_v1"
	}
	return rating.Policy
}
