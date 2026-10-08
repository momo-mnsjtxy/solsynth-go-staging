// SPDX-License-Identifier: AGPL-3.0-only
package collectionadminpg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/moderationpg"
	c "solsynth.local/go-migration/ports/admin/collections"
	"solsynth.local/go-migration/ports/authority"
	"time"
)

type Repository struct {
	db     *sql.DB
	global *moderationpg.Repository
	now    func() time.Time
}

func New(db *sql.DB, guard *authoritypg.TransactionGuard, now func() time.Time) (*Repository, error) {
	if now == nil {
		now = time.Now
	}
	g, e := moderationpg.New(db, guard, moderationpg.Config{Now: now})
	if e != nil {
		return nil, e
	}
	return &Repository{db, g, now}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if r == nil {
		return c.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,publisher_id FROM owned_post_collections LIMIT 0;SELECT collection_id FROM owned_post_collection_items LIMIT 0;SELECT collection_id FROM owned_post_interaction_audit LIMIT 0`)
	return e
}
func missing(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return c.ErrNotFound
	}
	return e
}

const projection = `jsonb_build_object('id',c.id,'publisher_id',c.publisher_id,'slug',c.slug,'name',c.name,'description',c.description,'background',NULL,'icon',NULL,'created_at',c.created_at,'updated_at',c.updated_at,'deleted_at',c.deleted_at,'item_count',(SELECT count(*) FROM owned_post_collection_items i WHERE i.collection_id=c.id),'publisher',jsonb_build_object('id',u.id,'name',u.name,'nick',u.nick,'bio',u.bio,'type',u.type,'account_id',u.account_id,'realm_id',u.realm_id,'created_at',u.created_at,'updated_at',u.updated_at,'verification',u.verification))`
const from = ` FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id WHERE c.deleted_at IS NULL`

func get(ctx context.Context, tx *sql.Tx, id string) (out c.Collection, e error) {
	if !authority.ValidID(id) {
		return out, c.ErrNotFound
	}
	var b []byte
	e = tx.QueryRowContext(ctx, `SELECT `+projection+from+` AND c.id=$1::uuid`, id).Scan(&b)
	if e != nil {
		return out, missing(e)
	}
	e = json.Unmarshal(b, &out)
	return
}
func (r *Repository) commit(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if e := r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) List(ctx context.Context, actor string, q c.Page) (out []c.Collection, total int, e error) {
	out = []c.Collection{}
	q = q.Bounds()
	if len(q.Query) > 512 || q.PublisherID != "" && !authority.ValidID(q.PublisherID) {
		return out, 0, c.ErrInvalid
	}
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "posts.moderate", nil)
	if e != nil {
		return out, 0, e
	}
	defer tx.Rollback()
	var b []byte
	e = tx.QueryRowContext(ctx, `WITH filtered AS(SELECT c.id`+from+` AND ($1::text='' OR strpos(lower(concat_ws(' ',c.slug,c.name,c.description,u.name,u.nick)),lower($1::text))>0) AND ($2::text='' OR c.publisher_id=NULLIF($2::text,'')::uuid)),page AS(SELECT c.id,c.updated_at,`+projection+` AS document FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id JOIN filtered f ON f.id=c.id ORDER BY c.updated_at DESC,c.id OFFSET $3 LIMIT $4) SELECT (SELECT count(*) FROM filtered),COALESCE((SELECT jsonb_agg(document ORDER BY updated_at DESC,id) FROM page),'[]'::jsonb)`, q.Query, q.PublisherID, q.Offset, q.Take).Scan(&total, &b)
	if e != nil {
		return
	}
	if e = json.Unmarshal(b, &out); e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "posts.moderate")
	return
}
func (r *Repository) Get(ctx context.Context, actor, id string) (out c.Collection, e error) {
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, "posts.moderate", nil)
	if e != nil {
		return
	}
	defer tx.Rollback()
	out, e = get(ctx, tx, id)
	if e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "posts.moderate")
	return
}

type ancestry struct{ Publisher, Owner, Realm, RealmOwner string }

func observe(ctx context.Context, tx *sql.Tx, id string) (o ancestry, e error) {
	e = tx.QueryRowContext(ctx, `SELECT u.id::text,COALESCE(u.account_id::text,''),COALESCE(u.realm_id::text,''),COALESCE(r.account_id::text,'') FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id LEFT JOIN owned_realms r ON r.id=u.realm_id WHERE c.id=$1::uuid AND c.deleted_at IS NULL`, id).Scan(&o.Publisher, &o.Owner, &o.Realm, &o.RealmOwner)
	return o, missing(e)
}
func (r *Repository) locked(ctx context.Context, actor, id, permission string) (*sql.Tx, error) {
	if r == nil {
		return nil, c.ErrUnavailable
	}
	if !authority.ValidID(id) {
		return nil, c.ErrNotFound
	}
	var before ancestry
	tx, e := r.global.BeginGlobalTransaction(ctx, actor, permission, func(tx *sql.Tx) ([]string, error) {
		var e error
		before, e = observe(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		ids := []string{}
		for _, v := range []string{before.Owner, before.RealmOwner} {
			if v != "" {
				ids = append(ids, v)
			}
		}
		return ids, nil
	})
	if e != nil {
		return nil, e
	}
	fail := func(e error) (*sql.Tx, error) { tx.Rollback(); return nil, missing(e) }
	var held string
	if before.Realm != "" {
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_realms WHERE id=$1::uuid FOR SHARE`, before.Realm).Scan(&held); e != nil {
			return fail(e)
		}
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1::uuid FOR UPDATE`, before.Publisher).Scan(&held); e != nil {
		return fail(e)
	}
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_post_collections WHERE id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, id).Scan(&held); e != nil {
		return fail(e)
	}
	after, e := observe(ctx, tx, id)
	if e != nil {
		return fail(e)
	}
	if before != after {
		return fail(c.ErrConflict)
	}
	if e = r.global.RequireGlobalInTransaction(ctx, tx, actor, permission); e != nil {
		return fail(e)
	}
	return tx, nil
}
func (r *Repository) audit(ctx context.Context, tx *sql.Tx, actor, id, action string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO owned_post_interaction_audit(actor_id,collection_id,action,occurred_at)VALUES($1::uuid,$2::uuid,$3::text,$4::timestamptz)`, actor, id, action, r.now().UTC())
	return e
}
func (r *Repository) Update(ctx context.Context, actor, id string, p c.Patch) (out c.Collection, e error) {
	if e = p.Validate(); e != nil {
		return
	}
	tx, e := r.locked(ctx, actor, id, "post.collections.update")
	if e != nil {
		return
	}
	defer tx.Rollback()
	out, e = get(ctx, tx, id)
	if e != nil {
		return
	}
	if p.Name != nil {
		out.Name = p.Name
	}
	if p.Description != nil {
		out.Description = p.Description
	}
	out.UpdatedAt = r.now().UTC()
	_, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET name=$2::text,description=$3::text,updated_at=$4::timestamptz WHERE id=$1::uuid`, id, out.Name, out.Description, out.UpdatedAt)
	if e != nil {
		return
	}
	if e = r.audit(ctx, tx, actor, id, "collection.update"); e != nil {
		return
	}
	e = r.commit(ctx, tx, actor, "post.collections.update")
	return
}
func (r *Repository) Delete(ctx context.Context, actor, id string) error {
	tx, e := r.locked(ctx, actor, id, "post.collections.delete")
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_collection_items WHERE collection_id=$1::uuid`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET deleted_at=$2::timestamptz,updated_at=$2::timestamptz WHERE id=$1::uuid`, id, r.now().UTC()); e != nil {
		return e
	}
	if e = r.audit(ctx, tx, actor, id, "collection.delete"); e != nil {
		return e
	}
	return r.commit(ctx, tx, actor, "post.collections.delete")
}

var _ c.Repository = (*Repository)(nil)
