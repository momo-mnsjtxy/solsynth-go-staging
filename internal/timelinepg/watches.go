// SPDX-License-Identifier: AGPL-3.0-only
package timelinepg

import (
	"context"
	"database/sql"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
	"strings"
	"time"
)

var _ sphere.WatchPreferenceRepository = (*Repository)(nil)
var _ sphere.WatchPreferencePatcher = (*Repository)(nil)
var _ sphere.WatcherRepository = (*Repository)(nil)
var watchSources = []sphere.WatchSource{sphere.WatchBookmark, sphere.WatchReaction, sphere.WatchReply}

func validSource(s sphere.WatchSource) bool {
	return s == sphere.WatchBookmark || s == sphere.WatchReaction || s == sphere.WatchReply
}
func loadWatch(ctx context.Context, tx *sql.Tx, ids []string) ([]sphere.WatchPreference, error) {
	rows, e := tx.QueryContext(ctx, `SELECT w.account_id::text,w.source,w.notify_reactions,w.notify_replies,w.notify_chains,w.notify_forwards,w.notify_edits FROM owned_post_watch_preferences w JOIN owned_authority_accounts a ON a.id=w.account_id WHERE w.account_id=ANY($1::uuid[]) AND NOT a.disabled ORDER BY w.account_id,w.source`, ids)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []sphere.WatchPreference{}
	for rows.Next() {
		var v sphere.WatchPreference
		if e = rows.Scan(&v.AccountID, &v.Source, &v.NotifyReactions, &v.NotifyReplies, &v.NotifyChains, &v.NotifyForwards, &v.NotifyEdits); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (r *Repository) LoadPreferences(ctx context.Context, ids []string) ([]sphere.WatchPreference, error) {
	if len(ids) > 1000 {
		return nil, timeline.ErrInvalid
	}
	for _, s := range ids {
		if !authority.ValidID(s) {
			return nil, posts.ErrUnauthenticated
		}
	}
	actor := ""
	if len(ids) == 1 {
		actor = ids[0]
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out, e := loadWatch(ctx, tx, ids)
	if e == nil {
		e = tx.Commit()
	}
	return out, e
}
func (r *Repository) WatchPreferences(ctx context.Context, actor string) ([]sphere.WatchPreference, error) {
	if !authority.ValidID(actor) {
		return nil, posts.ErrUnauthenticated
	}
	return (&sphere.WatchService{Repository: r}).GetEffectivePreferences(ctx, actor)
}
func (r *Repository) PatchWatch(ctx context.Context, actor string, v []sphere.WatchPreferenceUpdate) ([]sphere.WatchPreference, error) {
	return r.PatchPreferences(ctx, actor, v)
}
func (r *Repository) SavePreferences(ctx context.Context, actor string, values []sphere.WatchPreference) error {
	updates := make([]sphere.WatchPreferenceUpdate, 0, len(values))
	for _, v := range values {
		if v.AccountID != actor {
			return posts.ErrForbidden
		}
		v := v
		updates = append(updates, sphere.WatchPreferenceUpdate{Source: v.Source, NotifyReactions: &v.NotifyReactions, NotifyReplies: &v.NotifyReplies, NotifyChains: &v.NotifyChains, NotifyForwards: &v.NotifyForwards, NotifyEdits: &v.NotifyEdits})
	}
	_, e := r.PatchPreferences(ctx, actor, updates)
	return e
}
func (r *Repository) PatchPreferences(ctx context.Context, actor string, updates []sphere.WatchPreferenceUpdate) ([]sphere.WatchPreference, error) {
	out := []sphere.WatchPreference{}
	seen := map[sphere.WatchSource]bool{}
	for _, v := range updates {
		if !validSource(v.Source) || seen[v.Source] {
			return nil, timeline.ErrInvalid
		}
		seen[v.Source] = true
	}
	e := r.write(ctx, actor, "post.subscriptions.manage", func(tx *sql.Tx, now time.Time) error {
		rows, e := loadWatch(ctx, tx, []string{actor})
		if e != nil {
			return e
		}
		stored := map[sphere.WatchSource]sphere.WatchPreference{}
		for _, s := range watchSources {
			stored[s] = sphere.DefaultWatchPreference(actor, s)
		}
		for _, v := range rows {
			stored[v.Source] = v
		}
		for _, u := range updates {
			p := stored[u.Source]
			for _, f := range []struct {
				src *bool
				dst *bool
			}{{u.NotifyReactions, &p.NotifyReactions}, {u.NotifyReplies, &p.NotifyReplies}, {u.NotifyChains, &p.NotifyChains}, {u.NotifyForwards, &p.NotifyForwards}, {u.NotifyEdits, &p.NotifyEdits}} {
				if f.src != nil {
					*f.dst = *f.src
				}
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_watch_preferences(account_id,source,notify_reactions,notify_replies,notify_chains,notify_forwards,notify_edits,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account_id,source)DO UPDATE SET notify_reactions=EXCLUDED.notify_reactions,notify_replies=EXCLUDED.notify_replies,notify_chains=EXCLUDED.notify_chains,notify_forwards=EXCLUDED.notify_forwards,notify_edits=EXCLUDED.notify_edits,updated_at=EXCLUDED.updated_at`, actor, p.Source, p.NotifyReactions, p.NotifyReplies, p.NotifyChains, p.NotifyForwards, p.NotifyEdits, now)
			if e != nil {
				return e
			}
			stored[p.Source] = p
		}
		for _, s := range watchSources {
			out = append(out, stored[s])
		}
		if len(updates) > 0 {
			return audit(ctx, tx, actor, "post.watch", actor, now)
		}
		return nil
	})
	return out, e
}

// WatchAccounts is an internal notification-recipient resolver, never an HTTP
// identity endpoint. Each candidate's own visibility and bilateral block state
// is rechecked in the same snapshot as canonical bookmark/reaction/reply rows.
func (r *Repository) WatchAccounts(ctx context.Context, ids []string) (sphere.WatchAccounts, error) {
	out := sphere.WatchAccounts{}
	if len(ids) > 100 {
		return out, timeline.ErrInvalid
	}
	for _, s := range ids {
		if !authority.ValidID(s) {
			return out, timeline.ErrInvalid
		}
	}
	tx, e := r.read(ctx, "", false)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	q := `WITH candidates AS(SELECT b.account_id,b.post_id,'bookmark' AS source FROM owned_post_bookmarks b WHERE b.post_id=ANY($1::uuid[]) UNION SELECT r.account_id,r.post_id,'reaction' FROM owned_post_reactions r WHERE r.post_id=ANY($1::uuid[]) UNION SELECT u.account_id,l.replied_post_id,'reply' FROM owned_post_relations l JOIN owned_text_posts x ON x.id=l.post_id JOIN owned_publishers u ON u.id=x.publisher_id WHERE l.replied_post_id=ANY($1::uuid[]) AND x.deleted_at IS NULL AND x.drafted_at IS NULL AND x.published_at<=CURRENT_TIMESTAMP AND u.deleted_at IS NULL AND u.account_id IS NOT NULL) SELECT DISTINCT c.account_id::text,c.source FROM candidates c JOIN owned_authority_accounts a ON a.id=c.account_id WHERE NOT a.disabled AND EXISTS(SELECT 1` + postpg.ActivePostFrom + ` AND p.id=c.post_id AND p.drafted_at IS NULL AND ` + strings.ReplaceAll(postpg.DetailPredicate(r.visibility), "$1", "c.account_id::text") + `) ORDER BY c.source,c.account_id::text`
	rows, e := tx.QueryContext(ctx, q, ids)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var actor, source string
		if e = rows.Scan(&actor, &source); e != nil {
			rows.Close()
			return out, e
		}
		switch source {
		case "bookmark":
			out.Bookmarkers = append(out.Bookmarkers, actor)
		case "reaction":
			out.Reactors = append(out.Reactors, actor)
		case "reply":
			out.Repliers = append(out.Repliers, actor)
		}
	}
	e = rows.Err()
	rows.Close()
	if e == nil {
		e = tx.Commit()
	}
	return out, e
}

// ChainPostIDs supplies only internal IDs to WatchAccounts, which subsequently
// applies recipient-specific visibility. It does not expose a public chain API.
func (r *Repository) ChainPostIDs(ctx context.Context, head string) ([]string, error) {
	if !authority.ValidID(head) {
		return nil, posts.ErrNotFound
	}
	tx, e := r.read(ctx, "", false)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT p.id::text`+postpg.ActivePostFrom+` AND p.drafted_at IS NULL AND p.published_at<=CURRENT_TIMESTAMP AND(p.id=$1 OR EXISTS(SELECT 1 FROM owned_post_relations l WHERE l.post_id=p.id AND l.chained_post_id=$1)) ORDER BY p.created_at,p.id LIMIT 102`, head)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, s)
	}
	e = rows.Err()
	rows.Close()
	if len(out) > 101 {
		return nil, timeline.ErrDepth
	}
	if e == nil {
		e = tx.Commit()
	}
	return out, e
}
