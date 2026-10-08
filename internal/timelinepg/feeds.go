// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"time"
	"unicode/utf8"
)

func Cursor(v string) (*time.Time, string, error) {
	if v == "" {
		return nil, "", nil
	}
	if len(v) > 256 {
		return nil, "", timeline.ErrCursor
	}
	key := ""
	if strings.HasPrefix(v, "v1.") {
		b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(v, "v1."))
		if e != nil {
			return nil, "", timeline.ErrCursor
		}
		parts := strings.Split(string(b), "|")
		if len(parts) != 2 || !authority.ValidID(parts[1]) {
			return nil, "", timeline.ErrCursor
		}
		v = parts[0]
		key = parts[1]
	}
	t, e := time.Parse(time.RFC3339Nano, v)
	if e != nil || t.IsZero() {
		return nil, "", timeline.ErrCursor
	}
	return &t, key, nil
}
func encodeCursor(p posts.Post) string {
	return "v1." + base64.RawURLEncoding.EncodeToString([]byte(p.PublishedAt.UTC().Format(time.RFC3339Nano)+"|"+p.ID))
}
func validateSearch(o timeline.SearchOptions) error {
	if o.Offset < 0 || o.Offset > 1000000 || o.Take < 1 || o.Take > 100 || len(o.Realm) > 256 || len(o.Publisher) > 256 || len(o.Query) > 1024 || !utf8.ValidString(o.Query) || strings.ContainsRune(o.Query, 0) {
		return timeline.ErrInvalid
	}
	if o.Type != nil && *o.Type != posts.Moment && *o.Type != posts.Article {
		return posts.ErrUnsupported
	}
	if o.Order != "" && o.Order != "date" && o.Order != "created_at" && o.Order != "created" && o.Order != "createdat" && o.Order != "rank" {
		return posts.ErrUnsupported
	}
	if o.Start != nil && o.End != nil && o.Start.After(*o.End) {
		return timeline.ErrInvalid
	}
	for _, xs := range [][]string{o.Tags, o.Categories} {
		if len(xs) > 16 {
			return timeline.ErrInvalid
		}
		for _, s := range xs {
			if len(s) > 512 || strings.TrimSpace(s) == "" || strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
				return timeline.ErrInvalid
			}
		}
	}
	return nil
}
func (r *Repository) searchWhere(ctx context.Context, tx *sql.Tx, actor string, o timeline.SearchOptions) (string, []any, error) {
	where := r.base()
	args := []any{actor}
	add := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if o.Realm != "" {
		id, e := postpg.ResolveRealmFilter(ctx, tx, r.visibility, actor, o.Realm)
		if e != nil {
			return "", nil, e
		}
		where += ` AND p.realm_id=` + add(id) + `::uuid`
	}
	if o.Publisher != "" {
		pid, _, e := r.publisher(ctx, tx, actor, o.Publisher, false)
		if e != nil {
			return "", nil, e
		}
		where += ` AND p.publisher_id=` + add(pid) + `::uuid`
	}
	if o.Type != nil {
		where += ` AND p.type=` + add(*o.Type)
	}
	if q := strings.TrimSpace(o.Query); q != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		a := add(pattern)
		where += ` AND (COALESCE(p.title,'') ILIKE ` + a + ` OR COALESCE(p.description,'') ILIKE ` + a + ` OR p.content ILIKE ` + a + `)`
	}
	if o.Replies != nil {
		op := ""
		if !*o.Replies {
			op = "NOT "
		}
		where += ` AND ` + op + `EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.replied_post_id IS NOT NULL)`
	}
	for _, f := range []struct {
		xs                []string
		table, links, key string
	}{{o.Tags, "post_tags", "post_tag_links", "tags_id"}, {o.Categories, "post_categories", "post_category_links", "categories_id"}} {
		if len(f.xs) == 0 {
			continue
		}
		slugs := make([]string, len(f.xs))
		for i, s := range f.xs {
			slugs[i] = taxonomy.NormalizeSlug(s)
		}
		where += ` AND EXISTS(SELECT 1 FROM owned_taxonomy.` + f.links + ` l JOIN owned_taxonomy.` + f.table + ` t ON t.id=l.` + f.key + ` WHERE l.posts_id=p.id AND t.deleted_at IS NULL AND lower(t.slug)=ANY(` + add(slugs) + `::text[]))`
	}
	if o.Start != nil {
		where += ` AND p.published_at>=` + add(*o.Start)
	}
	if o.End != nil {
		where += ` AND p.published_at<=` + add(*o.End)
	}
	return where, args, nil
}
func searchOrder(o timeline.SearchOptions) string {
	direction := " DESC"
	if o.Ascending {
		direction = " ASC"
	}
	s := ""
	if o.Order == "rank" {
		s = `COALESCE((SELECT SUM(CASE WHEN attitude=0 THEN 1 WHEN attitude=2 THEN -1 ELSE 0 END) FROM owned_post_reactions r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.post_id=p.id AND NOT a.disabled),0)` + direction + ","
	}
	return s + `p.published_at` + direction + `,p.id` + direction
}
func (r *Repository) selectPosts(ctx context.Context, tx *sql.Tx, actor, where string, args []any, order string, offset, take int) ([]posts.Post, int64, error) {
	var total int64
	if e := tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	args = append(args, offset, take)
	q := `SELECT ` + postpg.ProjectionColumns + where + ` ORDER BY ` + order + fmt.Sprintf(" OFFSET $%d LIMIT $%d", len(args)-1, len(args))
	rows, e := tx.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, 0, e
	}
	out := []posts.Post{}
	for rows.Next() {
		p, e := postpg.ScanProjection(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	for i := range out {
		if e = r.Load(ctx, tx, actor, &out[i]); e != nil {
			return nil, 0, e
		}
	}
	return out, total, nil
}
func (r *Repository) Search(ctx context.Context, actor string, o timeline.SearchOptions) ([]posts.Post, int64, error) {
	if e := validateSearch(o); e != nil {
		return nil, 0, e
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	where, args, e := r.searchWhere(ctx, tx, actor, o)
	if e != nil {
		return nil, 0, e
	}
	out, total, e := r.selectPosts(ctx, tx, actor, where, args, searchOrder(o), o.Offset, o.Take)
	if e == nil {
		e = tx.Commit()
	}
	return out, total, e
}
func (r *Repository) Feed(ctx context.Context, actor string, o timeline.Options) (timeline.Page, error) {
	page := timeline.Page{Items: []timeline.Event{}, Mode: o.Mode}
	if o.Mode == "" {
		o.Mode = sphere.Personalized
		page.Mode = o.Mode
	}
	if o.Mode != sphere.Personalized && o.Mode != sphere.Latest && o.Mode != sphere.Top {
		return page, timeline.ErrMode
	}
	if o.Take < 1 || o.Take > 100 || len(o.Realm) > 256 || len(o.Publisher) > 256 || len(o.Collection) > 128 {
		return page, timeline.ErrInvalid
	}
	if o.Collection != "" && o.Publisher == "" {
		return page, timeline.ErrScope
	}
	if o.Filter != "" && o.Filter != "subscriptions" && o.Filter != "friends" {
		return page, timeline.ErrInvalid
	}
	if o.Filter != "" && actor == "" {
		return page, posts.ErrUnauthenticated
	}
	cursor, key, e := Cursor(o.Cursor)
	if e != nil {
		return page, e
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return page, e
	}
	defer tx.Rollback()
	where, args, e := r.searchWhere(ctx, tx, actor, timeline.SearchOptions{Publisher: o.Publisher, Realm: o.Realm})
	if e != nil {
		return page, e
	}
	add := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where += ` AND NOT EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND(l.replied_post_id IS NOT NULL OR l.chained_post_id IS NOT NULL))`
	if cursor != nil {
		a := add(*cursor)
		if key == "" {
			where += ` AND p.published_at<` + a
		} else {
			where += ` AND(p.published_at,p.id)<(` + a + `,` + add(key) + `::uuid)`
		}
	}
	if o.Collection != "" {
		var cid string
		e = tx.QueryRowContext(ctx, `SELECT c.id::text FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id WHERE c.deleted_at IS NULL AND u.deleted_at IS NULL AND `+accountpg.PublisherNamePredicate("u", "$1")+` AND lower(c.slug)=lower($2)`, o.Publisher, o.Collection).Scan(&cid)
		if errors.Is(e, sql.ErrNoRows) {
			return page, timeline.ErrCollectionNotFound
		}
		if e != nil {
			return page, e
		}
		where += ` AND EXISTS(SELECT 1 FROM owned_post_collection_items ci WHERE ci.post_id=p.id AND ci.collection_id=` + add(cid) + `::uuid)`
	}
	if actor != "" {
		where += ` AND NOT EXISTS(SELECT 1 FROM owned_timeline_preferences x WHERE x.account_id=$1::uuid AND ((x.kind='publisher' AND x.reference_id=p.publisher_id) OR(x.kind='account' AND x.reference_id=u.account_id)))`
		if o.Filter == "subscriptions" {
			where += ` AND EXISTS(SELECT 1 FROM owned_publisher_follows f WHERE f.account_id=$1::uuid AND f.publisher_id=p.publisher_id AND f.state='accepted' AND f.ended_at IS NULL)`
		}
		if o.Filter == "friends" {
			where += ` AND EXISTS(SELECT 1 FROM owned_social_relationships a WHERE a.account_id=$1::uuid AND a.related_id=u.account_id AND a.status>=100) AND EXISTS(SELECT 1 FROM owned_social_relationships b WHERE b.account_id=u.account_id AND b.related_id=$1::uuid AND b.status>=100)`
		}
	}
	// Rank a complete chronological page. The cursor represents its last scanned
	// (timestamp, UUID), so equal timestamps and score reordering cannot skip posts.
	out, total, e := r.selectPosts(ctx, tx, actor, where, args, `p.published_at DESC,p.id DESC`, 0, o.Take)
	if e != nil {
		return page, e
	}
	if int64(len(out)) < total && len(out) > 0 {
		c := encodeCursor(out[len(out)-1])
		page.NextCursor = &c
	}
	ranked := make([]sphere.Post, 0, len(out))
	byID := map[string]posts.Post{}
	signals := map[string]sphere.Signals{}
	interests, e := r.interestMap(ctx, tx, actor)
	if e != nil {
		return page, e
	}
	for _, p := range out {
		rp := sphere.Post{ID: p.ID, PublisherID: p.PublisherID, CreatedAt: p.CreatedAt, PublishedAt: p.PublishedAt, ThreadRepliesCount: int(p.ThreadRepliesCount)}
		if p.Type == posts.Article {
			rp.Type = "Article"
		}
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN attitude=0 THEN 1 WHEN attitude=2 THEN -1 ELSE 0 END),0) FROM owned_post_reactions r JOIN owned_authority_accounts a ON a.id=r.account_id WHERE r.post_id=$1 AND NOT a.disabled`, p.ID).Scan(&rp.ReactionScore); e != nil {
			return page, e
		}
		signal := sphere.Signals{}
		if actor != "" {
			signal.PersonalizationBonus = r.bonus(p, interests, r.now())
			var followed bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owned_publisher_follows WHERE account_id=$1 AND publisher_id=$2 AND state='accepted' AND ended_at IS NULL)`, actor, p.PublisherID).Scan(&followed); e != nil {
				return page, e
			}
			if followed {
				signal.SubscriptionBoost = 1.5
			}
			var tags, categories int
			e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM owned_taxonomy.post_category_subscriptions s JOIN owned_taxonomy.post_tag_links l ON l.tags_id=s.tag_id JOIN owned_taxonomy.post_tags t ON t.id=l.tags_id WHERE s.account_id=$1 AND l.posts_id=$2 AND s.deleted_at IS NULL AND t.deleted_at IS NULL),(SELECT count(*) FROM owned_taxonomy.post_category_subscriptions s JOIN owned_taxonomy.post_category_links l ON l.categories_id=s.category_id JOIN owned_taxonomy.post_categories c ON c.id=l.categories_id WHERE s.account_id=$1 AND l.posts_id=$2 AND s.deleted_at IS NULL AND c.deleted_at IS NULL)`, actor, p.ID).Scan(&tags, &categories)
			if e != nil {
				return page, e
			}
			signal.PersonalizationBonus += float64(tags)*1.25 + float64(categories)*1.5
		}
		ranked = append(ranked, rp)
		byID[p.ID] = p
		signals[p.ID] = signal
	}
	for _, rp := range sphere.RankPosts(ranked, signals, sphere.Options{Now: r.now().UTC(), Take: o.Take, Mode: o.Mode, Authenticated: actor != "", Aggressive: o.Aggressive}) {
		p := byID[rp.ID]
		page.Items = append(page.Items, timeline.Event{ID: p.ID, ResourceIdentifier: p.ID, Type: "posts.new", CreatedAt: *p.PublishedAt, UpdatedAt: p.UpdatedAt, Data: &p})
	}
	if len(page.Items) == 0 {
		page.Items = append(page.Items, timeline.Event{ID: "empty", ResourceIdentifier: "empty", Type: "empty", CreatedAt: r.now().UTC(), UpdatedAt: r.now().UTC()})
	}
	return page, tx.Commit()
}
func (r *Repository) bonus(p posts.Post, in map[string]timeline.Interest, now time.Time) float64 {
	score := func(kind, id string) float64 {
		v := in[kind+":"+id]
		if v.LastInteractedAt.IsZero() {
			return 0
		}
		return sphere.DecayedInterestScore(v.Score, &v.LastInteractedAt, now)
	}
	b := 0.
	for _, t := range p.Tags {
		b += score("tag", t.ID) * .8
	}
	for _, c := range p.Categories {
		b += score("category", c.ID) * .75
	}
	for _, c := range p.PublisherCollections {
		b += score("collection", c.ID) * .6
	}
	s := score("publisher", p.PublisherID)
	if s >= 0 {
		b += math.Min(2, s*.2)
	} else {
		b += math.Max(-6, s*.5)
	}
	return b
}
