// SPDX-License-Identifier: AGPL-3.0-only
package interactionpg

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/accounts"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

const collectionColumns = `c.id::text,c.publisher_id::text,c.slug,c.name,c.description,
 CASE WHEN EXISTS(SELECT 1 FROM owned_media_files artwork JOIN owned_authority_accounts artwork_owner ON artwork_owner.id=artwork.account_id WHERE artwork.id=c.background_file_id AND artwork.state='ready' AND artwork.is_public AND artwork.mime_type LIKE 'image/%' AND NOT artwork_owner.disabled) THEN c.background_file_id::text END,
 CASE WHEN EXISTS(SELECT 1 FROM owned_media_files artwork JOIN owned_authority_accounts artwork_owner ON artwork_owner.id=artwork.account_id WHERE artwork.id=c.icon_file_id AND artwork.state='ready' AND artwork.is_public AND artwork.mime_type LIKE 'image/%' AND NOT artwork_owner.disabled) THEN c.icon_file_id::text END,
 c.created_at,c.updated_at,c.deleted_at,u.id::text,u.name,u.nick,u.bio,u.type,u.account_id::text,u.realm_id::text,u.created_at,u.updated_at,u.verification`
const collectionFrom = ` FROM owned_post_collections c JOIN owned_publishers u ON u.id=c.publisher_id WHERE c.deleted_at IS NULL AND u.deleted_at IS NULL AND (u.account_id IS NULL OR EXISTS(SELECT 1 FROM owned_authority_accounts a WHERE a.id=u.account_id AND NOT a.disabled))`

func scanCollection(s interface{ Scan(...any) error }) (posts.Collection, error) {
	var c posts.Collection
	var verification []byte
	e := s.Scan(&c.ID, &c.PublisherID, &c.Slug, &c.Name, &c.Description, &c.BackgroundID, &c.IconID, &c.CreatedAt, &c.UpdatedAt, &c.DeletedAt, &c.Publisher.ID, &c.Publisher.Name, &c.Publisher.Nick, &c.Publisher.Bio, &c.Publisher.Type, &c.Publisher.AccountID, &c.Publisher.RealmID, &c.Publisher.CreatedAt, &c.Publisher.UpdatedAt, &verification)
	if errors.Is(e, sql.ErrNoRows) {
		e = interactions.ErrCollectionNotFound
	}
	if e == nil {
		c.Publisher.Verification, e = accounts.DecodeVerification(verification)
	}
	return c, e
}
func getCollection(ctx context.Context, tx *sql.Tx, publisher, slug string, lock bool) (posts.Collection, error) {
	q := `SELECT ` + collectionColumns + collectionFrom + ` AND ` + accountpg.PublisherNamePredicate("u", "$1") + ` AND c.slug=$2`
	if lock {
		q += ` FOR UPDATE OF c`
	}
	c, e := scanCollection(tx.QueryRowContext(ctx, q, publisher, taxonomy.NormalizeSlug(slug)))
	if e == nil {
		e = hydrateCollectionArtwork(ctx, tx, &c)
	}
	return c, e
}
func (r *Repository) Collection(ctx context.Context, actor, publisher, slug string) (posts.Collection, error) {
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return posts.Collection{}, e
	}
	defer tx.Rollback()
	v, e := getCollection(ctx, tx, publisher, slug, false)
	if e != nil {
		return v, e
	}
	allowed, e := postpg.CanViewPublisher(ctx, tx, actor, v.PublisherID, r.visibility)
	if e != nil {
		return posts.Collection{}, e
	}
	if !allowed {
		return posts.Collection{}, interactions.ErrCollectionNotFound
	}
	if e = tx.Commit(); e != nil {
		return posts.Collection{}, e
	}
	return v, nil
}
func (r *Repository) Collections(ctx context.Context, actor, publisher string, p interactions.Page) ([]posts.Collection, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	q := strings.TrimSpace(p.Query)
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	where := collectionFrom + ` AND ($2='' OR ` + accountpg.PublisherNamePredicate("u", "$2") + `) AND ($3='' OR strpos(lower(c.slug),lower($3))>0 OR strpos(lower(coalesce(c.name,'')),lower($3))>0 OR strpos(lower(coalesce(c.description,'')),lower($3))>0 OR strpos(lower(u.name),lower($3))>0 OR strpos(lower(u.nick),lower($3))>0)`
	where += ` AND ` + postpg.VisiblePublisherPredicate(r.visibility, "u", "$1", publisher == "")
	args := []any{actor, publisher, q}
	var total int64
	if e = tx.QueryRowContext(ctx, `SELECT count(*)`+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	paging := ""
	if publisher == "" {
		paging = ` OFFSET $4 LIMIT $5`
		args = append(args, p.Offset, p.Take)
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+collectionColumns+where+` ORDER BY coalesce(c.name,c.slug),c.slug,c.id`+paging, args...)
	if e != nil {
		return nil, 0, e
	}
	out := []posts.Collection{}
	for rows.Next() {
		v, e := scanCollection(rows)
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
	for i := range out {
		if e = hydrateCollectionArtwork(ctx, tx, &out[i]); e != nil {
			return nil, 0, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
func (r *Repository) CreateCollection(ctx context.Context, actor, publisher string, v interactions.CollectionRequest) (posts.Collection, error) {
	var out posts.Collection
	if e := interactions.ValidateCollection(v, true); e != nil {
		return out, e
	}
	e := r.collectionArtworkWrite(ctx, actor, "post.collections.create", []*string{v.BackgroundID, v.IconID}, func(tx *sql.Tx, now time.Time, artwork map[string]string) error {
		pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
		if e != nil {
			return e
		}
		if e = validateCollectionArtwork(ctx, tx, artwork, v.BackgroundID, v.IconID); e != nil {
			return e
		}
		id, e := newID()
		if e != nil {
			return e
		}
		out = posts.Collection{ID: id, PublisherID: pub.ID, Publisher: pub, Slug: taxonomy.NormalizeSlug(v.Slug), Name: v.Name, Description: v.Description, BackgroundID: v.BackgroundID, IconID: v.IconID, CreatedAt: now, UpdatedAt: now}
		_, e = tx.ExecContext(ctx, `INSERT INTO owned_post_collections(id,publisher_id,slug,name,description,background_file_id,icon_file_id,created_at,updated_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, id, pub.ID, out.Slug, v.Name, v.Description, v.BackgroundID, v.IconID, now)
		if conflict(e) {
			return interactions.ErrCollectionConflict
		}
		if e != nil {
			return e
		}
		if e = hydrateCollectionArtwork(ctx, tx, &out); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "", id, "collection.create", now)
	})
	if e != nil {
		return posts.Collection{}, e
	}
	return out, nil
}
func (r *Repository) lockedCollection(ctx context.Context, tx *sql.Tx, actor, publisher, slug string) (posts.Collection, error) {
	pub, e := r.publishers.RequirePostEditor(ctx, tx, actor, publisher)
	if e != nil {
		return posts.Collection{}, e
	}
	return getCollection(ctx, tx, pub.Name, slug, true)
}
func (r *Repository) UpdateCollection(ctx context.Context, actor, publisher, slug string, v interactions.CollectionRequest) (posts.Collection, error) {
	var out posts.Collection
	if e := interactions.ValidateCollection(v, false); e != nil {
		return out, e
	}
	e := r.collectionArtworkWrite(ctx, actor, "post.collections.update", []*string{v.BackgroundID, v.IconID}, func(tx *sql.Tx, now time.Time, artwork map[string]string) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		if e = validateCollectionArtwork(ctx, tx, artwork, v.BackgroundID, v.IconID); e != nil {
			return e
		}
		// Source collection update replaces all nullable fields, unlike post patch.
		c.Name = v.Name
		c.Description = v.Description
		c.BackgroundID, c.IconID = v.BackgroundID, v.IconID
		c.Background, c.Icon = nil, nil
		c.UpdatedAt = now
		if _, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET name=$2,description=$3,background_file_id=$4,icon_file_id=$5,updated_at=$6 WHERE id=$1`, c.ID, c.Name, c.Description, c.BackgroundID, c.IconID, now); e != nil {
			return e
		}
		if e = hydrateCollectionArtwork(ctx, tx, &c); e != nil {
			return e
		}
		out = c
		return audit(ctx, tx, actor, "", c.ID, "collection.update", now)
	})
	if e != nil {
		return posts.Collection{}, e
	}
	return out, nil
}

func validateCollectionArtwork(ctx context.Context, tx *sql.Tx, expected map[string]string, ids ...*string) error {
	ordered := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if id != nil && !seen[*id] {
			seen[*id] = true
			ordered = append(ordered, *id)
		}
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		var held, owner string
		if e := tx.QueryRowContext(ctx, `SELECT f.id::text,f.account_id::text FROM owned_media_files f JOIN owned_authority_accounts a ON a.id=f.account_id WHERE f.id=$1::uuid AND f.state='ready' AND f.is_public AND f.mime_type LIKE 'image/%' AND NOT a.disabled FOR SHARE OF f`, id).Scan(&held, &owner); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return interactions.ErrInvalid
			}
			return e
		}
		if expected[id] != owner {
			return interactions.ErrCollectionConflict
		}
	}
	return nil
}

func observeCollectionArtworkOwners(ctx context.Context, tx *sql.Tx, ids ...*string) (map[string]string, error) {
	owners := map[string]string{}
	for _, id := range ids {
		if id == nil {
			continue
		}
		var owner string
		if e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_files WHERE id=$1::uuid`, *id).Scan(&owner); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return nil, interactions.ErrInvalid
			}
			return nil, e
		}
		owners[*id] = owner
	}
	return owners, nil
}

// collectionArtworkWrite observes every account dependency before taking any
// account lock. The sorted set prevents reciprocal A-uses-B/B-uses-A artwork
// writes from deadlocking and the file check rejects ownership drift.
func (r *Repository) collectionArtworkWrite(ctx context.Context, actor, permission string, ids []*string, fn func(*sql.Tx, time.Time, map[string]string) error) error {
	if r == nil || r.db == nil {
		return interactions.ErrUnavailable
	}
	if !authority.ValidID(actor) {
		return posts.ErrUnauthenticated
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if r.guard != nil {
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	artwork, e := observeCollectionArtworkOwners(ctx, tx, ids...)
	if e != nil {
		return e
	}
	accounts := map[string]bool{actor: true}
	for _, owner := range artwork {
		if _, ok := accounts[owner]; !ok {
			accounts[owner] = false
		}
	}
	ordered := make([]string, 0, len(accounts))
	for account := range accounts {
		ordered = append(ordered, account)
	}
	sort.Strings(ordered)
	for _, account := range ordered {
		var disabled bool
		lock := ` FOR SHARE`
		if account == actor {
			lock = ` FOR UPDATE`
		}
		if e = tx.QueryRowContext(ctx, `SELECT disabled FROM owned_authority_accounts WHERE id=$1::uuid`+lock, account).Scan(&disabled); e != nil {
			return missing(e)
		}
		if disabled {
			if account == actor {
				return posts.ErrForbidden
			}
			return interactions.ErrInvalid
		}
	}
	if r.guard != nil {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	if e = fn(tx, r.now().UTC(), artwork); e != nil {
		return e
	}
	if r.guard != nil {
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, permission); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func hydrateCollectionArtwork(ctx context.Context, tx *sql.Tx, c *posts.Collection) error {
	load := func(id *string) (*media.File, error) {
		if id == nil {
			return nil, nil
		}
		var f media.File
		e := tx.QueryRowContext(ctx, `SELECT f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage FROM owned_media_files f JOIN owned_authority_accounts owner ON owner.id=f.account_id WHERE f.id=$1::uuid AND f.state='ready' AND f.is_public AND f.mime_type LIKE 'image/%' AND NOT owner.disabled`, *id).Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		return &f, e
	}
	var e error
	c.Background, e = load(c.BackgroundID)
	if e != nil {
		return e
	}
	c.Icon, e = load(c.IconID)
	return e
}
func (r *Repository) DeleteCollection(ctx context.Context, actor, publisher, slug string) error {
	return r.write(ctx, actor, "post.collections.delete", func(tx *sql.Tx, now time.Time) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_post_collections SET deleted_at=$2,updated_at=$2 WHERE id=$1`, c.ID, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_post_collection_items WHERE collection_id=$1`, c.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_taxonomy.post_category_subscriptions SET deleted_at=$2,updated_at=$2 WHERE collection_id=$1 AND deleted_at IS NULL`, c.ID, now); e != nil {
			return e
		}
		return audit(ctx, tx, actor, "", c.ID, "collection.delete", now)
	})
}
func validIDs(ids []string) ([]string, error) {
	if len(ids) > 100 {
		return nil, interactions.ErrInvalid
	}
	set := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if !authority.ValidID(id) {
			return nil, interactions.ErrInvalid
		}
		if !set[id] {
			set[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (r *Repository) AddCollectionPosts(ctx context.Context, actor, publisher, slug string, ids []string, batch bool) error {
	ids, e := validIDs(ids)
	if e != nil {
		return e
	}
	if !batch && len(ids) != 1 {
		return interactions.ErrInvalid
	}
	return r.write(ctx, actor, "post.collections.posts.manage", func(tx *sql.Tx, now time.Time) error {
		pubID, e := r.publishers.ObservePostPublisherID(ctx, tx, actor, publisher)
		if e != nil {
			return e
		}
		observed, e := postpg.ObservePostScopes(ctx, tx, ids)
		if e != nil {
			return e
		}
		for _, s := range observed {
			if s.PublisherID != pubID {
				return interactions.ErrUnsupported
			}
		}
		scopes, e := postpg.PrelockPublishingScopes(ctx, tx, r.visibility, []string{pubID}, postpg.ObservedRealmIDs(observed), false)
		if e != nil {
			return e
		}
		var held string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_publishers WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, pubID).Scan(&held); e != nil {
			return missing(e)
		}
		if e = accountpg.VerifyPublisherRealms(ctx, tx, scopes); e != nil {
			return e
		}
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		if c.PublisherID != pubID {
			return posts.ErrConflict
		}
		if e = postpg.VerifyPostScopes(ctx, tx, observed); e != nil {
			return e
		}
		for _, id := range ids {
			// Bounded collection flow only includes posts from its canonical publisher.
			// Cross-publisher collection inserts require a separately reviewed policy.
			var actual string
			e = tx.QueryRowContext(ctx, `SELECT publisher_id::text FROM owned_text_posts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&actual)
			if e != nil {
				return missing(e)
			}
			if actual != c.PublisherID {
				return interactions.ErrUnsupported
			}
			p, e := postpg.ReadWithVisibility(ctx, tx, actor, id, r.visibility)
			if e != nil {
				return e
			}
			if p.LockedAt != nil {
				return posts.ErrLocked
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO owned_post_collection_items(collection_id,post_id,created_at)VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, id, now)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				if batch {
					continue
				}
				return interactions.ErrDuplicate
			}
			if e = audit(ctx, tx, actor, id, c.ID, "collection.add", now); e != nil {
				return e
			}
		}
		return nil
	})
}
func (r *Repository) RemoveCollectionPosts(ctx context.Context, actor, publisher, slug string, ids []string) error {
	ids, e := validIDs(ids)
	if e != nil {
		return e
	}
	return r.write(ctx, actor, "post.collections.posts.manage", func(tx *sql.Tx, now time.Time) error {
		c, e := r.lockedCollection(ctx, tx, actor, publisher, slug)
		if e != nil {
			return e
		}
		for _, id := range ids {
			res, e := tx.ExecContext(ctx, `DELETE FROM owned_post_collection_items WHERE collection_id=$1 AND post_id=$2`, c.ID, id)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n > 0 {
				if e = audit(ctx, tx, actor, id, c.ID, "collection.remove", now); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (r *Repository) CollectionPosts(ctx context.Context, actor, publisher, slug string, p interactions.Page) ([]posts.Post, int64, error) {
	if !interactions.ValidPage(p) {
		return nil, 0, interactions.ErrInvalid
	}
	tx, e := r.read(ctx, actor, false)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	c, e := getCollection(ctx, tx, publisher, slug, false)
	if e != nil {
		return nil, 0, e
	}
	allowed, e := postpg.CanViewPublisher(ctx, tx, actor, c.PublisherID, r.visibility)
	if e != nil {
		return nil, 0, e
	}
	if !allowed {
		return nil, 0, interactions.ErrCollectionNotFound
	}
	out, total, e := r.listPosts(ctx, tx, actor, ` AND EXISTS(SELECT 1 FROM owned_post_collection_items i WHERE i.post_id=p.id AND i.collection_id=$2)`, collectionPostOrder, []any{actor, c.ID}, p)
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(); e != nil {
		return nil, 0, e
	}
	return out, total, nil
}
