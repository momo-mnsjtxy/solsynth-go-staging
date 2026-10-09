// SPDX-License-Identifier: AGPL-3.0-only
package postpg

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/posts"
)

func boostPublisher(ctx context.Context, tx *sql.Tx, actor string) (string, error) {
	var id string
	e := tx.QueryRowContext(ctx, `SELECT p.id::text FROM owned_publishers p JOIN owned_publisher_members m ON m.publisher_id=p.id AND m.account_id=$1 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=50 LEFT JOIN owned_publishing_settings s ON s.account_id=$1 WHERE p.deleted_at IS NULL AND (p.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=p.account_id AND NOT owner.disabled)) ORDER BY CASE WHEN p.id=s.default_fediverse_publisher_id THEN 0 WHEN p.id=s.default_publisher_id THEN 1 WHEN p.type=0 AND p.account_id=$1 THEN 2 ELSE 3 END,p.created_at,p.id LIMIT 1`, actor).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return "", posts.ErrForbidden
	}
	return id, e
}

func (r *Repository) BoostPost(ctx context.Context, actor, id string, content *string) (out posts.Boost, err error) {
	if !authority.ValidID(id) {
		return out, posts.ErrNotFound
	}
	if content != nil && (!utf8.ValidString(*content) || strings.ContainsRune(*content, 0) || utf8.RuneCountInString(*content) > 1024) {
		return out, posts.ErrInvalid
	}
	err = r.write(ctx, actor, "posts.boost", func(tx *sql.Tx, now time.Time) error {
		publisher, e := boostPublisher(ctx, tx, actor)
		if e != nil {
			return e
		}
		pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
		if e != nil {
			return e
		}
		if _, e = ReadWithVisibility(ctx, tx, actor, id, r.visibility); e != nil {
			return e
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&held); e != nil {
			return e
		}
		if _, e = ReadWithVisibility(ctx, tx, actor, id, r.visibility); e != nil {
			return e
		}
		bid, e := newID()
		if e != nil {
			return e
		}
		out = posts.Boost{ID: bid, PostID: id, PublisherID: publisher, AccountID: actor, Content: content, BoostedAt: now, Publisher: pub}
		if _, e = tx.ExecContext(ctx, `INSERT INTO owned_post_boosts(id,post_id,publisher_id,account_id,content,boosted_at)VALUES($1,$2,$3,$4,$5,$6)`, bid, id, publisher, actor, content, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, id, "boost", now)
	})
	return
}

func (r *Repository) UnboostPost(ctx context.Context, actor, id string) error {
	if !authority.ValidID(id) {
		return posts.ErrNotFound
	}
	return r.write(ctx, actor, "posts.boost", func(tx *sql.Tx, now time.Time) error {
		var publisher string
		e := tx.QueryRowContext(ctx, `SELECT b.publisher_id::text FROM owned_post_boosts b JOIN owned_publisher_members m ON m.publisher_id=b.publisher_id AND m.account_id=$2 AND m.joined_at IS NOT NULL AND m.revoked_at IS NULL AND m.role>=50 WHERE b.post_id=$1 ORDER BY b.boosted_at,b.publisher_id LIMIT 1`, id, actor).Scan(&publisher)
		if errors.Is(e, sql.ErrNoRows) {
			return posts.ErrNotFound
		}
		if e != nil {
			return e
		}
		if _, e = r.publishers.RequirePostEditor(ctx, tx, actor, publisher); e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `DELETE FROM owned_post_boosts WHERE post_id=$1 AND publisher_id=$2`, id, publisher)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return posts.ErrNotFound
		}
		return audit(ctx, tx, actor, id, "unboost", now)
	})
}

func (r *Repository) PostBoosts(ctx context.Context, actor, id string, offset, take int) (out []posts.Boost, total int64, err error) {
	if !authority.ValidID(id) {
		return nil, 0, posts.ErrNotFound
	}
	if offset < 0 || take < 1 || take > 100 {
		return nil, 0, posts.ErrInvalid
	}
	out = []posts.Boost{}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if _, e = ReadWithVisibility(ctx, tx, actor, id, r.visibility); e != nil {
		return nil, 0, e
	}
	boostFrom := ` FROM owned_post_boosts b JOIN owned_publishers u ON u.id=b.publisher_id WHERE b.post_id=$2 AND ` + VisiblePublisherPredicate(r.visibility, "u", "$1", true)
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+boostFrom, actor, id).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT b.id::text,b.post_id::text,b.publisher_id::text,b.account_id::text,b.content,b.boosted_at,`+accountpgPublisherColumns+boostFrom+` ORDER BY b.boosted_at DESC,b.id LIMIT $3 OFFSET $4`, actor, id, take, offset)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	for rows.Next() {
		var v posts.Boost
		var verification []byte
		e = rows.Scan(&v.ID, &v.PostID, &v.PublisherID, &v.AccountID, &v.Content, &v.BoostedAt, &v.Publisher.ID, &v.Publisher.Type, &v.Publisher.Name, &v.Publisher.Nick, &v.Publisher.Bio, &v.Publisher.AccountID, &v.Publisher.RealmID, &v.Publisher.CreatedAt, &v.Publisher.UpdatedAt, &verification)
		if e != nil {
			return nil, 0, e
		}
		v.Publisher.Verification, e = accounts.DecodeVerification(verification)
		if e != nil {
			return nil, 0, e
		}
		out = append(out, v)
	}
	if e = rows.Err(); e != nil {
		return nil, 0, e
	}
	err = tx.Commit()
	return
}

const accountpgPublisherColumns = `u.id::text,u.type,u.name,u.nick,u.bio,u.account_id::text,u.realm_id::text,u.created_at,u.updated_at,u.verification`

func (r *Repository) loadCompletionPage(ctx context.Context, tx *sql.Tx, actor string, out []posts.Post) error {
	for i := range out {
		if e := r.loadAutomod(ctx, tx, actor, &out[i]); e != nil {
			return e
		}
		if r.associations != nil {
			if e := r.associations.Load(ctx, tx, actor, &out[i]); e != nil {
				return e
			}
		}
		if e := r.loadSchedule(ctx, tx, actor, &out[i]); e != nil {
			return e
		}
	}
	return nil
}

func (r *Repository) Featured(ctx context.Context, actor string, offset, take int) (out []posts.Post, total int64, err error) {
	if offset < 0 || take < 1 || take > 100 {
		return nil, 0, posts.ErrInvalid
	}
	out = []posts.Post{}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	eligible := strings.NewReplacer("p.", "candidate.", "u.", "publisher.").Replace(ListingPredicate(r.visibility))
	featured := `WITH eligible AS (
	 SELECT candidate.id,candidate.created_at,candidate.awarded_score
	 FROM owned_text_posts candidate JOIN owned_publishers publisher ON publisher.id=candidate.publisher_id
	 WHERE candidate.deleted_at IS NULL AND candidate.visibility=0
	 AND candidate.created_at>=(date_trunc('day',statement_timestamp() AT TIME ZONE 'UTC')-interval '1 day') AT TIME ZONE 'UTC'
	 AND candidate.created_at<(date_trunc('day',statement_timestamp() AT TIME ZONE 'UTC')) AT TIME ZONE 'UTC'
	 AND ` + eligible + `
	), ranked AS (
	 SELECT candidate.id,COALESCE((SELECT sum(CASE WHEN reaction.attitude=0 THEN 1 ELSE -1 END) FROM owned_post_reactions reaction WHERE reaction.post_id=candidate.id),0)
	 +COALESCE((SELECT count(*) FROM owned_post_relations relation JOIN owned_text_posts reply ON reply.id=relation.post_id WHERE relation.replied_post_id=candidate.id AND reply.deleted_at IS NULL),0)
	 +(candidate.awarded_score/10) AS score
	 FROM eligible candidate
	 ORDER BY score DESC,candidate.created_at DESC,candidate.id DESC LIMIT 5
	), selected AS (SELECT id,0 AS sponsored FROM ranked UNION SELECT slot.post_id,1 FROM owned_post_sponsor_placements slot WHERE slot.valid_from<=statement_timestamp() AND slot.valid_until>statement_timestamp()) `
	where := base + ` AND p.id IN(SELECT id FROM selected) AND ` + ListingPredicate(r.visibility)
	if e = tx.QueryRowContext(ctx, featured+`SELECT count(*)`+where, actor).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, featured+`SELECT `+columns+where+` ORDER BY (SELECT max(sponsored) FROM selected selected_order WHERE selected_order.id=p.id) DESC,(SELECT score FROM ranked ranked_order WHERE ranked_order.id=p.id) DESC NULLS LAST,p.published_at DESC,p.id DESC OFFSET $2 LIMIT $3`, actor, offset, take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if e = r.loadCompletionPage(ctx, tx, actor, out); e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	for i := range out {
		posts.RedactAutomodForEditor(&out[i])
	}
	return out, total, nil
}

func (r *Repository) Adjacent(ctx context.Context, actor, id string, next bool, o posts.ListOptions) (out posts.Post, err error) {
	if !authority.ValidID(id) {
		return out, posts.ErrNotFound
	}
	if len(o.Publisher) > 256 || len(o.Realm) > 256 {
		return out, posts.ErrInvalid
	}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	current, e := ReadWithVisibility(ctx, tx, actor, id, r.visibility)
	if e != nil {
		return out, e
	}
	realm, e := ResolveRealmFilter(ctx, tx, r.visibility, actor, o.Realm)
	if e != nil {
		return out, e
	}
	op, order := "<", "DESC"
	if next {
		op, order = ">", "ASC"
	}
	where := base + ` AND ` + ListingPredicate(r.visibility) + ` AND (p.published_at,p.id) ` + op + ` ($2::timestamptz,$3::uuid) AND ($4='' OR u.id::text=$4 OR ` + accountpg.PublisherNamePredicate("u", "$4") + `) AND ($5::text='' OR p.realm_id=NULLIF($5::text,'')::uuid) AND ($6::integer IS NULL OR p.type=$6)` + pinFilter(o)
	out, e = scan(tx.QueryRowContext(ctx, `SELECT `+columns+where+` ORDER BY p.published_at `+order+`,p.id `+order+` LIMIT 1`, actor, current.PublishedAt, current.ID, o.Publisher, realm, o.Type))
	if e != nil {
		return posts.Post{}, e
	}
	if e = r.loadAutomod(ctx, tx, actor, &out); e != nil {
		return posts.Post{}, e
	}
	if r.associations != nil {
		if e = r.associations.Load(ctx, tx, actor, &out); e != nil {
			return posts.Post{}, e
		}
	}
	if e = r.loadSchedule(ctx, tx, actor, &out); e != nil {
		return posts.Post{}, e
	}
	if e = tx.Commit(); e != nil {
		return posts.Post{}, e
	}
	posts.RedactAutomodForEditor(&out)
	return out, nil
}

func (r *Repository) ReactionUser(ctx context.Context, actor, name string, offset, take int) (out []posts.ReactionListing, total int64, err error) {
	if strings.TrimSpace(name) == "" || len(name) > 256 || offset < 0 || take < 1 || take > 100 {
		return nil, 0, posts.ErrInvalid
	}
	out = []posts.ReactionListing{}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	reactionAccount, e := r.publishers.LookupAccountInTransaction(ctx, tx, name)
	if e != nil {
		return nil, 0, mapped(e)
	}
	account := reactionAccount.ID
	where := ` FROM owned_post_reactions x JOIN owned_text_posts p ON p.id=x.post_id JOIN owned_publishers u ON u.id=p.publisher_id WHERE x.account_id=$2 AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts owner WHERE owner.id=u.account_id AND NOT owner.disabled)) AND ` + ListingPredicate(r.visibility)
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, actor, account).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT x.id::text,x.post_id::text,x.account_id::text,x.symbol,x.attitude,x.created_at`+where+` ORDER BY x.created_at DESC,x.id DESC OFFSET $3 LIMIT $4`, actor, account, offset, take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		var v posts.ReactionListing
		if e = rows.Scan(&v.ID, &v.PostID, &v.AccountID, &v.Symbol, &v.Attitude, &v.CreatedAt); e != nil {
			rows.Close()
			return nil, 0, e
		}
		v.Account = reactionAccount
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		out[i].Post, e = ReadWithVisibility(ctx, tx, actor, out[i].PostID, r.visibility)
		if e != nil {
			return nil, 0, e
		}
		p := []posts.Post{out[i].Post}
		if e = r.loadCompletionPage(ctx, tx, actor, p); e != nil {
			return nil, 0, e
		}
		out[i].Post = p[0]
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}

func (r *Repository) Replies(ctx context.Context, actor, parent, mode string, offset, take int, ascending bool) (out []posts.Post, total int64, err error) {
	if !authority.ValidID(parent) {
		return nil, 0, posts.ErrNotFound
	}
	if offset < 0 || take < 1 || take > 100 {
		return nil, 0, posts.ErrInvalid
	}
	out = []posts.Post{}
	tx, e := r.read(ctx, actor)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if _, e = ReadWithVisibility(ctx, tx, actor, parent, r.visibility); e != nil {
		return nil, 0, e
	}
	where := base + ` AND EXISTS(SELECT 1 FROM owned_post_relations relation WHERE relation.post_id=p.id AND relation.replied_post_id=$2) AND ` + ListingPredicate(r.visibility)
	order := `p.published_at DESC,p.id DESC`
	switch mode {
	case "list":
		if ascending {
			order = `p.published_at ASC,p.id ASC`
		}
	case "featured":
		order = `(COALESCE((SELECT sum(CASE WHEN x.attitude=0 THEN 2 WHEN x.attitude=2 THEN -1 ELSE 0 END)FROM owned_post_reactions x WHERE x.post_id=p.id),0)+CASE WHEN p.created_at>statement_timestamp()-interval '1 hour' THEN 5 ELSE 0 END) DESC,p.created_at DESC,p.id`
		take = 1
		offset = 0
	case "pinned":
		where += ` AND p.pin_mode=2`
		order = `p.created_at DESC,p.id DESC`
	default:
		return nil, 0, posts.ErrInvalid
	}
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, actor, parent).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+columns+where+` ORDER BY `+order+` OFFSET $3 LIMIT $4`, actor, parent, offset, take)
	if e != nil {
		return nil, 0, e
	}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if e = r.loadCompletionPage(ctx, tx, actor, out); e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	for i := range out {
		posts.RedactAutomodForEditor(&out[i])
	}
	return out, total, nil
}

var _ posts.CompletionRepository = (*Repository)(nil)
