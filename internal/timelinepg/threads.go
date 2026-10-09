// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

func (r *Repository) related(ctx context.Context, tx *sql.Tx, actor, id, kind string, o timeline.SearchOptions) ([]posts.Post, int64, error) {
	column := ""
	switch kind {
	case "replies":
		column = "replied_post_id"
	case "forwards":
		column = "forwarded_post_id"
	case "chain":
		column = "chained_post_id"
	default:
		return nil, 0, timeline.ErrInvalid
	}
	if _, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility); e != nil {
		return nil, 0, e
	}
	where := r.base() + ` AND EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.` + column + `=$2)`
	return r.selectPosts(ctx, tx, actor, where, []any{actor, id}, searchOrder(o), o.Offset, o.Take)
}
func (r *Repository) Related(ctx context.Context, actor, id, kind string, o timeline.SearchOptions) ([]posts.Post, int64, error) {
	if !authority.ValidID(id) {
		return nil, 0, posts.ErrNotFound
	}
	if e := validateSearch(o); e != nil {
		return nil, 0, e
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	out, total, e := r.related(ctx, tx, actor, id, kind, o)
	if e == nil {
		e = tx.Commit()
	}
	return out, total, e
}
func node(p posts.Post, depth int) timeline.ThreadNode {
	return timeline.ThreadNode{Post: p, Depth: depth, ParentID: p.RepliedPostID}
}
func (r *Repository) descendants(ctx context.Context, tx *sql.Tx, actor, id string, limit int) ([]timeline.ThreadNode, error) {
	rows, e := tx.QueryContext(ctx, r.descendantCTE()+` SELECT id::text,depth FROM tree ORDER BY ordering LIMIT $3`, actor, id, limit)
	if e != nil {
		return nil, e
	}
	type entry struct {
		id    string
		depth int
	}
	entries := []entry{}
	for rows.Next() {
		var v entry
		if e = rows.Scan(&v.id, &v.depth); e != nil {
			rows.Close()
			return nil, e
		}
		entries = append(entries, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []timeline.ThreadNode{}
	for _, v := range entries {
		p, e := r.post(ctx, tx, actor, v.id)
		if e != nil {
			return nil, e
		}
		out = append(out, node(p, v.depth))
	}
	return out, nil
}
func (r *Repository) Thread(ctx context.Context, actor, id string, o timeline.ThreadOptions) (timeline.Thread, error) {
	out := timeline.Thread{Descendants: []timeline.ThreadNode{}}
	if !authority.ValidID(id) {
		return out, posts.ErrNotFound
	}
	if o.Take < 1 || o.Take > 100 || o.AncestorLimit < 0 || o.AncestorLimit > maxDepth {
		return out, timeline.ErrInvalid
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	p, e := r.post(ctx, tx, actor, id)
	if e != nil {
		return out, e
	}
	if o.Ancestors {
		out.Ancestors = []timeline.ThreadNode{}
		seen := map[string]bool{id: true}
		next := p.RepliedPostID
		for next != nil && len(out.Ancestors) < o.AncestorLimit {
			if seen[*next] {
				return out, timeline.ErrDepth
			}
			seen[*next] = true
			ancestor, e := r.post(ctx, tx, actor, *next)
			if errors.Is(e, posts.ErrNotFound) {
				break
			}
			if e != nil {
				return out, e
			}
			out.Ancestors = append(out.Ancestors, node(ancestor, 0))
			next = ancestor.RepliedPostID
		}
		for i, j := 0, len(out.Ancestors)-1; i < j; i, j = i+1, j-1 {
			out.Ancestors[i], out.Ancestors[j] = out.Ancestors[j], out.Ancestors[i]
		}
		for i := range out.Ancestors {
			out.Ancestors[i].Depth = i
		}
	}
	out.Current = node(p, len(out.Ancestors))
	out.Descendants, e = r.descendants(ctx, tx, actor, id, o.Take+1)
	if e != nil {
		return out, e
	}
	if len(out.Descendants) > o.Take {
		out.HasMore = true
		out.Descendants = out.Descendants[:o.Take]
	}
	return out, tx.Commit()
}
func (r *Repository) ThreadedReplies(ctx context.Context, actor, id string, o timeline.SearchOptions) ([]timeline.ThreadNode, int64, error) {
	if !authority.ValidID(id) {
		return nil, 0, posts.ErrNotFound
	}
	if e := validateSearch(o); e != nil {
		return nil, 0, e
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	roots, total, e := r.related(ctx, tx, actor, id, "replies", o)
	if e != nil {
		return nil, 0, e
	}
	out := []timeline.ThreadNode{}
	for _, root := range roots {
		out = append(out, node(root, 0))
		children, e := r.descendants(ctx, tx, actor, root.ID, 501-len(out))
		if e != nil {
			return nil, 0, e
		}
		for _, v := range children {
			v.Depth++
			out = append(out, v)
		}
		if len(out) > 500 {
			return nil, 0, timeline.ErrDepth
		}
	}
	return out, total, tx.Commit()
}
func (r *Repository) Chain(ctx context.Context, actor, id string) ([]posts.Post, error) {
	if !authority.ValidID(id) {
		return nil, posts.ErrNotFound
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	p, e := r.post(ctx, tx, actor, id)
	if e != nil {
		return nil, e
	}
	if p.ChainedPostID != nil {
		p, e = r.post(ctx, tx, actor, *p.ChainedPostID)
		if e != nil {
			return nil, e
		}
	}
	// A hidden/deleted head does not convert a child into a public standalone head.
	var actual *string
	e = tx.QueryRowContext(ctx, `SELECT chained_post_id::text FROM owned_post_relations WHERE post_id=$1`, p.ID).Scan(&actual)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if actual != nil {
		return nil, posts.ErrNotFound
	}
	members, total, e := r.related(ctx, tx, actor, p.ID, "chain", timeline.SearchOptions{Take: 100, Ascending: true})
	if e != nil {
		return nil, e
	}
	if total > 100 {
		return nil, timeline.ErrDepth
	}
	p.ChainedPosts = []posts.Post{}
	p.ChainedCount = 0
	out := append([]posts.Post{p}, members...)
	return out, tx.Commit()
}
