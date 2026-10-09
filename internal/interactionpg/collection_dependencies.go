// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"solsynth.local/go-migration/ports/sphere/posts"
)

// A navigation response includes one-level parents, chained children and reply
// counts. Plan their complete canonical ancestry before acquiring any account,
// realm or publisher lock. Hidden/deleted rows are included in lock planning,
// never in the response. A changed plan is a conflict, not a late lock upgrade.
type collectionDependencyPlan struct {
	raw        string
	Posts      []struct{ ID, Publisher, Realm string }
	Publishers []struct{ ID, Owner, Realm string }
	Realms     []struct{ ID, Owner string }
	Files      []struct{ ID, Owner string }
}

const collectionDependencySQL = `WITH RECURSIVE replies(id,depth,path) AS (
 SELECT $2::uuid,0,ARRAY[$2::uuid]
 UNION ALL SELECT l.post_id,r.depth+1,r.path||l.post_id FROM replies r JOIN owned_post_relations l ON l.replied_post_id=r.id WHERE r.depth<50 AND NOT l.post_id=ANY(r.path)
), targets AS (
 SELECT post_id AS id FROM owned_post_collection_items WHERE collection_id=$1::uuid
 UNION SELECT id FROM replies
 UNION SELECT replied_post_id FROM owned_post_relations WHERE post_id=$2::uuid AND replied_post_id IS NOT NULL
 UNION SELECT forwarded_post_id FROM owned_post_relations WHERE post_id=$2::uuid AND forwarded_post_id IS NOT NULL
 UNION SELECT chained_post_id FROM owned_post_relations WHERE post_id=$2::uuid AND chained_post_id IS NOT NULL
 UNION SELECT post_id FROM owned_post_relations WHERE forwarded_post_id=$2::uuid OR chained_post_id=$2::uuid
), pp AS (SELECT p.id,p.publisher_id,p.realm_id FROM owned_text_posts p JOIN targets t ON t.id=p.id),
 pubs AS (SELECT u.id,u.account_id,u.realm_id FROM owned_publishers u WHERE u.id IN(SELECT publisher_id FROM pp UNION SELECT publisher_id FROM owned_post_collections WHERE id=$1::uuid)),
 realms AS (SELECT r.id,r.account_id FROM owned_realms r WHERE r.id IN(SELECT realm_id FROM pp UNION SELECT realm_id FROM pubs)),
 links AS (SELECT m.post_id,m.file_id FROM owned_post_media m JOIN targets t ON t.id=m.post_id UNION SELECT m.post_id,m.file_id FROM owned_post_thumbnails m JOIN targets t ON t.id=m.post_id),
 files AS (SELECT f.id,f.account_id FROM owned_media_files f WHERE f.id IN(SELECT file_id FROM links))
 SELECT jsonb_build_object(
 'Posts',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Publisher',publisher_id,'Realm',COALESCE(realm_id::text,'')) ORDER BY id) FROM pp),'[]'::jsonb),
 'Publishers',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',COALESCE(account_id::text,''),'Realm',COALESCE(realm_id::text,'')) ORDER BY id) FROM pubs),'[]'::jsonb),
 'Realms',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',account_id) ORDER BY id) FROM realms),'[]'::jsonb),
 'Files',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',account_id) ORDER BY id) FROM files),'[]'::jsonb),
 'Links',COALESCE((SELECT jsonb_agg(to_jsonb(links) ORDER BY post_id,file_id) FROM links),'[]'::jsonb),
 'Relations',COALESCE((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.post_id) FROM owned_post_relations l JOIN targets t ON t.id=l.post_id),'[]'::jsonb)
 )::text`

func observeCollectionDependencies(ctx context.Context, tx *sql.Tx, collection, target string) (*collectionDependencyPlan, error) {
	var p collectionDependencyPlan
	if e := tx.QueryRowContext(ctx, collectionDependencySQL, collection, target).Scan(&p.raw); e != nil {
		return nil, e
	}
	if e := json.Unmarshal([]byte(p.raw), &p); e != nil {
		return nil, e
	}
	return &p, nil
}
func (p *collectionDependencyPlan) verify(ctx context.Context, tx *sql.Tx, collection, target string) error {
	current, e := observeCollectionDependencies(ctx, tx, collection, target)
	if e != nil {
		return e
	}
	if current.raw != p.raw {
		return posts.ErrConflict
	}
	return nil
}
func sortedCollectionIDs(ids ...[]string) []string {
	set := map[string]bool{}
	for _, values := range ids {
		for _, id := range values {
			if id != "" {
				set[id] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func (p *collectionDependencyPlan) accounts() []string {
	var ids []string
	for _, s := range p.Publishers {
		ids = append(ids, s.Owner)
	}
	for _, s := range p.Realms {
		ids = append(ids, s.Owner)
	}
	for _, s := range p.Files {
		ids = append(ids, s.Owner)
	}
	return sortedCollectionIDs(ids)
}
func (p *collectionDependencyPlan) lockPostsAndMedia(ctx context.Context, tx *sql.Tx) error {
	for _, v := range p.Posts {
		var id string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_text_posts WHERE id=$1::uuid FOR SHARE`, v.ID).Scan(&id); e != nil {
			return e
		}
	}
	for _, v := range p.Files {
		var id string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_media_files WHERE id=$1::uuid FOR SHARE`, v.ID).Scan(&id); e != nil {
			return e
		}
	}
	return nil
}
