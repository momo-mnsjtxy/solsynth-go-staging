// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"solsynth.local/go-migration/ports/sphere/posts"
)

// Identity/ancestry observation only. No observed state authorizes delivery.
// The worker locks the complete sorted account set before any domain lock.
type publicationDependencies struct {
	collectionDependencyPlan
	Collections []struct{ ID, Publisher string }
	raw         string
}

const publicationDependenciesSQL = `WITH target AS(SELECT p.post_id FROM owned_collection_publications p JOIN owned_collection_publication_notifications q ON q.publication_id=p.id WHERE q.id=$1),
 targets AS(SELECT post_id AS id FROM target UNION SELECT source_id FROM owned_post_relations relation JOIN target ON target.post_id=relation.post_id CROSS JOIN LATERAL unnest(ARRAY[relation.replied_post_id,relation.forwarded_post_id,relation.chained_post_id]) source_id WHERE source_id IS NOT NULL),
 pp AS(SELECT p.id,p.publisher_id,p.realm_id FROM owned_text_posts p JOIN targets t ON t.id=p.id),
 collections AS(SELECT c.id,c.publisher_id FROM owned_post_collections c JOIN owned_collection_publication_subscriptions s ON s.collection_id=c.id WHERE s.notification_id=$1),
 pubs AS(SELECT u.id,u.account_id,u.realm_id FROM owned_publishers u WHERE u.id IN(SELECT publisher_id FROM pp UNION SELECT publisher_id FROM collections)),
 realms AS(SELECT r.id,r.account_id FROM owned_realms r WHERE r.id IN(SELECT realm_id FROM pp UNION SELECT realm_id FROM pubs)),
 links AS(SELECT m.post_id,m.file_id,m.owner_authorized FROM owned_post_media m JOIN targets t ON t.id=m.post_id UNION SELECT m.post_id,m.file_id,false FROM owned_post_thumbnails m JOIN targets t ON t.id=m.post_id),
 files AS(SELECT f.id,f.account_id FROM owned_media_files f WHERE f.id IN(SELECT file_id FROM links))
 SELECT jsonb_build_object(
 'Posts',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Publisher',publisher_id,'Realm',COALESCE(realm_id::text,'')) ORDER BY id) FROM pp),'[]'::jsonb),
 'Publishers',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',COALESCE(account_id::text,''),'Realm',COALESCE(realm_id::text,'')) ORDER BY id) FROM pubs),'[]'::jsonb),
 'Realms',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',account_id) ORDER BY id) FROM realms),'[]'::jsonb),
 'Files',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Owner',account_id) ORDER BY id) FROM files),'[]'::jsonb),
 'Collections',COALESCE((SELECT jsonb_agg(jsonb_build_object('ID',id,'Publisher',publisher_id) ORDER BY id) FROM collections),'[]'::jsonb),
 'Links',COALESCE((SELECT jsonb_agg(to_jsonb(links) ORDER BY post_id,file_id,owner_authorized) FROM links),'[]'::jsonb),
 'Relations',COALESCE((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.post_id) FROM owned_post_relations l JOIN target t ON t.post_id=l.post_id),'[]'::jsonb)
 )::text`

func observePublicationDependencies(ctx context.Context, tx *sql.Tx, id string) (*publicationDependencies, error) {
	var p publicationDependencies
	if e := tx.QueryRowContext(ctx, publicationDependenciesSQL, id).Scan(&p.raw); e != nil {
		return nil, e
	}
	if e := json.Unmarshal([]byte(p.raw), &p); e != nil {
		return nil, e
	}
	return &p, nil
}
func (p *publicationDependencies) verify(ctx context.Context, tx *sql.Tx, id string) error {
	current, e := observePublicationDependencies(ctx, tx, id)
	if e != nil {
		return e
	}
	if p.raw != current.raw {
		return posts.ErrConflict
	}
	return nil
}
func (p *publicationDependencies) lock(ctx context.Context, tx *sql.Tx, recipient string) error {
	for _, id := range sortedCollectionIDs(append(p.accounts(), recipient)) {
		mode := ` FOR SHARE`
		if id == recipient {
			mode = ` FOR UPDATE`
		}
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1`+mode, id).Scan(&held); e != nil {
			return e
		}
	}
	for _, s := range p.Realms {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1 FOR SHARE`, s.ID).Scan(&held); e != nil {
			return e
		}
	}
	for _, s := range p.Publishers {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 FOR SHARE`, s.ID).Scan(&held); e != nil {
			return e
		}
	}
	for _, s := range p.Collections {
		var held string
		if e := tx.QueryRowContext(ctx, `SELECT id::text FROM owned_post_collections WHERE id=$1 FOR SHARE`, s.ID).Scan(&held); e != nil {
			return e
		}
	}
	return p.lockPostsAndMedia(ctx, tx)
}
