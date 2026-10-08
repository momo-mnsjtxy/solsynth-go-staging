// SPDX-License-Identifier: AGPL-3.0-only
// PostgreSQL adapter against an explicitly isolated synthetic schema only.
package taxonomypg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"solsynth.local/go-migration/ports/sphere/taxonomy"
)

type Repository struct {
	db               *sql.DB
	synthetic        bool
	owned            bool
	publicVisibility int32
	authorityGuard   TransactionGuard
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }
func NewSyntheticRepository(db *sql.DB, publicVisibility int32) *Repository {
	return &Repository{db: db, synthetic: true, publicVisibility: publicVisibility}
}

var _ taxonomy.Repository = (*Repository)(nil)

func (r *Repository) guard(ctx context.Context) error {
	if r == nil || !r.synthetic {
		return taxonomy.ErrPolicyUnverified
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.db == nil {
		return taxonomy.ErrDatabaseRequired
	}
	return nil
}
func table(k taxonomy.Kind) (string, error) {
	switch k {
	case taxonomy.Tag:
		return "taxonomy_candidate.post_tags", nil
	case taxonomy.Category:
		return "taxonomy_candidate.post_categories", nil
	}
	return "", taxonomy.ErrInvalid
}
func links(k taxonomy.Kind) (string, string) {
	if k == taxonomy.Tag {
		return "taxonomy_candidate.post_tag_links", "tags_id"
	}
	return "taxonomy_candidate.post_category_links", "categories_id"
}
func fields(k taxonomy.Kind) string {
	if k == taxonomy.Category {
		return "t.id::text,t.slug,t.name,NULL::text,NULL::text,FALSE,FALSE,NULL::timestamptz,t.created_at,t.updated_at"
	}
	return "t.id::text,t.slug,t.name,t.description,t.owner_publisher_id::text,t.is_protected,t.is_event,t.event_ends_at,t.created_at,t.updated_at"
}
func usage(k taxonomy.Kind) string {
	link, key := links(k)
	return "(SELECT count(*) FROM " + link + " l JOIN taxonomy_candidate.posts p ON p.id=l.posts_id WHERE l." + key + "=t.id AND p.deleted_at IS NULL)"
}

type scanner interface{ Scan(...any) error }

func scan(s scanner, k taxonomy.Kind) (taxonomy.Record, error) {
	v := taxonomy.Record{Kind: k}
	err := s.Scan(&v.ID, &v.Slug, &v.Name, &v.Description, &v.OwnerPublisherID, &v.IsProtected, &v.IsEvent, &v.EventEndsAt, &v.CreatedAt, &v.UpdatedAt, &v.Usage)
	return v, err
}
func get(ctx context.Context, tx *policyTx, k taxonomy.Kind, slug string, count, lock bool) (taxonomy.Record, error) {
	tbl, err := table(k)
	if err != nil {
		return taxonomy.Record{}, err
	}
	probe := taxonomy.LowerInvariant(slug)
	if k == taxonomy.Tag {
		probe = taxonomy.NormalizeSlug(slug)
	}
	u := "0::bigint"
	if count {
		u = usage(k)
	}
	q := "SELECT " + fields(k) + "," + u + " FROM " + tbl + " t WHERE lower(t.slug)=$1 AND t.deleted_at IS NULL"
	if tx.owned && k == taxonomy.Tag {
		q += " AND " + ownedTagPublisherScope
	}
	q += " LIMIT 1"
	if lock {
		q += " FOR UPDATE"
	}
	row, err := scan(tx.QueryRowContext(ctx, q, probe), k)
	if errors.Is(err, sql.ErrNoRows) {
		err = taxonomy.ErrNotFound
	}
	return row, err
}
func (r *Repository) Get(ctx context.Context, k taxonomy.Kind, slug string, admin bool) (taxonomy.Record, error) {
	if err := r.guard(ctx); err != nil {
		return taxonomy.Record{}, err
	}
	tx, err := r.begin(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return taxonomy.Record{}, err
	}
	defer tx.Rollback()
	row, err := get(ctx, tx, k, slug, admin, false)
	if err != nil {
		return taxonomy.Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return taxonomy.Record{}, err
	}
	return row, nil
}
func buildList(q taxonomy.Query, visibility int32) (string, string, []any, error) {
	q, err := q.Normalize()
	if err != nil {
		return "", "", nil, err
	}
	tbl, _ := table(q.Kind)
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := "t.deleted_at IS NULL"
	if q.Search != "" {
		p := arg("%" + q.Search + "%")
		where += " AND (t.slug ILIKE " + p
		if q.Admin {
			where += " OR t.name ILIKE " + p
			if q.Kind == taxonomy.Tag {
				where += " OR t.description ILIKE " + p
			}
		}
		where += ")"
	}
	if q.Admin && q.Kind == taxonomy.Tag {
		if q.OwnerPublisherID != nil {
			where += " AND t.owner_publisher_id=" + arg(*q.OwnerPublisherID)
		}
		if q.Unowned != nil && *q.Unowned {
			where += " AND t.owner_publisher_id IS NULL"
		}
		if q.IsProtected != nil {
			where += " AND t.is_protected=" + arg(*q.IsProtected)
		}
		if q.IsEvent != nil {
			where += " AND t.is_event=" + arg(*q.IsEvent)
		}
	}
	order := "t.name ASC"
	if q.Admin {
		switch q.Order {
		case "usage":
			order = "usage DESC,t.slug ASC"
		case "name":
			order = "coalesce(t.name,t.slug) ASC"
		case "created":
			order = "t.created_at DESC"
		default:
			order = "t.updated_at DESC"
		}
	} else {
		switch q.Order {
		case "":
		case "usage":
			order = "usage DESC"
		case "popularity":
			link, key := links(q.Kind)
			order = "(SELECT count(*) FROM " + link + " l JOIN taxonomy_candidate.posts p ON p.id=l.posts_id WHERE l." + key + "=t.id AND p.deleted_at IS NULL AND p.drafted_at IS NULL AND p.visibility=" + arg(visibility) + " AND p.created_at>=" + arg(q.Now.Add(-7*24*time.Hour)) + " AND p.created_at<=" + arg(q.Now) + ") DESC"
		default:
			order = "t.created_at DESC"
		}
	}
	// Count arguments are a prefix of row arguments. The caller counts only
	// placeholders in count SQL rather than reusing popularity/order parameters.
	countSQL := "SELECT count(*) FROM " + tbl + " t WHERE " + where
	rowsSQL := "SELECT " + fields(q.Kind) + "," + usage(q.Kind) + " AS usage FROM " + tbl + " t WHERE " + where + " ORDER BY " + order + " OFFSET " + arg(q.Offset) + " LIMIT " + arg(q.Take)
	return countSQL, rowsSQL, args, nil
}
func argumentCount(query string) int {
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '$' {
			continue
		}
		v := 0
		for i++; i < len(query) && query[i] >= '0' && query[i] <= '9'; i++ {
			v = v*10 + int(query[i]-'0')
		}
		if v > n {
			n = v
		}
	}
	return n
}
func (r *Repository) List(ctx context.Context, q taxonomy.Query) (taxonomy.Page, error) {
	if err := r.guard(ctx); err != nil {
		return taxonomy.Page{}, err
	}
	countSQL, rowsSQL, args, err := buildList(q, r.publicVisibility)
	if err != nil {
		return taxonomy.Page{}, err
	}
	if r.owned && q.Kind == taxonomy.Tag {
		countSQL = strings.Replace(countSQL, "WHERE t.deleted_at IS NULL", "WHERE t.deleted_at IS NULL AND "+ownedTagPublisherScope, 1)
		rowsSQL = strings.Replace(rowsSQL, "WHERE t.deleted_at IS NULL", "WHERE t.deleted_at IS NULL AND "+ownedTagPublisherScope, 1)
	}
	tx, err := r.begin(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return taxonomy.Page{}, err
	}
	defer tx.Rollback()
	result := taxonomy.Page{Rows: []taxonomy.Record{}}
	if err = tx.QueryRowContext(ctx, countSQL, args[:argumentCount(countSQL)]...).Scan(&result.Total); err != nil {
		return taxonomy.Page{}, err
	}
	rows, err := tx.QueryContext(ctx, rowsSQL, args...)
	if err != nil {
		return taxonomy.Page{}, err
	}
	for rows.Next() {
		row, e := scan(rows, q.Kind)
		if e != nil {
			rows.Close()
			return taxonomy.Page{}, e
		}
		result.Rows = append(result.Rows, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return taxonomy.Page{}, err
	}
	if err = tx.Commit(); err != nil {
		return taxonomy.Page{}, err
	}
	return result, nil
}
func publisherExists(ctx context.Context, tx *policyTx, id *string) (bool, error) {
	if id == nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM taxonomy_candidate.publishers WHERE id=$1 AND deleted_at IS NULL)", *id).Scan(&ok)
	return ok, err
}
func duplicate(ctx context.Context, tx *policyTx, k taxonomy.Kind, slug, id string) error {
	tbl, err := table(k)
	if err != nil {
		return err
	}
	var exists bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+tbl+" WHERE lower(slug)=$1 AND id<>$2 AND deleted_at IS NULL)", slug, id).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		if k == taxonomy.Category {
			return taxonomy.Problem("CATEGORY_SLUG_EXISTS", "A category with this slug already exists.", 400)
		}
		return taxonomy.Problem("TAG_CREATE_FAILED", "A tag with this slug already exists.", 400)
	}
	return nil
}
func (r *Repository) Create(ctx context.Context, row taxonomy.Record) (taxonomy.Record, error) {
	if err := r.guard(ctx); err != nil {
		return taxonomy.Record{}, err
	}
	tbl, err := table(row.Kind)
	if err != nil {
		return taxonomy.Record{}, err
	}
	if !taxonomy.ValidID(row.ID) || !taxonomy.ValidID(row.AuditActorID) || row.Slug == "" || taxonomy.UTF16Length(row.Slug) > 128 {
		return taxonomy.Record{}, taxonomy.ErrInvalid
	}
	tx, err := r.begin(ctx, nil)
	if err != nil {
		return taxonomy.Record{}, err
	}
	defer tx.Rollback()
	if r.owned {
		if err = r.authorizeRecord(ctx, tx, row, "create"); err != nil {
			return taxonomy.Record{}, err
		}
		if err = lockPublishers(ctx, tx, row.OwnerPublisherID); err != nil {
			return taxonomy.Record{}, err
		}
	}
	// Snapshot has no unique slug index. Serializing fixture mutations protects
	// the source-visible case-insensitive duplicate check under concurrency.
	if _, err = tx.ExecContext(ctx, "LOCK TABLE "+tbl+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return taxonomy.Record{}, err
	}
	if err = duplicate(ctx, tx, row.Kind, row.Slug, row.ID); err != nil {
		return taxonomy.Record{}, err
	}
	if row.Kind == taxonomy.Tag && row.OwnerPublisherID != nil {
		ok, e := publisherExists(ctx, tx, row.OwnerPublisherID)
		if e != nil {
			return taxonomy.Record{}, e
		}
		if !ok {
			return taxonomy.Record{}, taxonomy.Problem("TAG_OWNER_PUBLISHER_NOT_FOUND", "Owner publisher not found.", 400)
		}
	}
	if r.owned {
		if err = r.publisherPolicy(ctx, tx, row, row, "create"); err != nil {
			return taxonomy.Record{}, err
		}
	}
	if row.Kind == taxonomy.Category {
		_, err = tx.ExecContext(ctx, "INSERT INTO "+tbl+" (id,slug,name,created_at,updated_at) VALUES($1,$2,$3,$4,$5)", row.ID, row.Slug, row.Name, row.CreatedAt, row.UpdatedAt)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO "+tbl+" (id,slug,name,description,owner_publisher_id,is_protected,is_event,event_ends_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", row.ID, row.Slug, row.Name, row.Description, row.OwnerPublisherID, row.IsProtected, row.IsEvent, row.EventEndsAt, row.CreatedAt, row.UpdatedAt)
	}
	if err != nil {
		return taxonomy.Record{}, err
	}
	if err = appendAudit(ctx, tx, row, "create"); err != nil {
		return taxonomy.Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return taxonomy.Record{}, err
	}
	return row, nil
}
func (r *Repository) Mutate(ctx context.Context, k taxonomy.Kind, slug string, op taxonomy.Mutation, c taxonomy.Change, apply func(taxonomy.Record, taxonomy.MutationState) (taxonomy.Record, error)) (taxonomy.Record, error) {
	if err := r.guard(ctx); err != nil {
		return taxonomy.Record{}, err
	}
	if apply == nil {
		return taxonomy.Record{}, taxonomy.ErrInvalid
	}
	tbl, err := table(k)
	if err != nil {
		return taxonomy.Record{}, err
	}
	tx, err := r.begin(ctx, nil)
	if err != nil {
		return taxonomy.Record{}, err
	}
	defer tx.Rollback()
	var observed taxonomy.Record
	if r.owned {
		if err = r.authorize(ctx, tx, "", ""); err != nil {
			return taxonomy.Record{}, err
		}
		observed, err = get(ctx, tx, k, slug, false, false)
		if err != nil {
			return taxonomy.Record{}, err
		}
		// Publisher-before-taxonomy order matches canonical post association
		// writes. Detect any owner change after these locks rather than acquiring
		// a new publisher lock in the opposite order while holding the table.
		if err = lockPublishers(ctx, tx, observed.OwnerPublisherID, c.PublisherID); err != nil {
			return taxonomy.Record{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "LOCK TABLE "+tbl+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return taxonomy.Record{}, err
	}
	row, err := get(ctx, tx, k, slug, false, true)
	if err != nil {
		return taxonomy.Record{}, err
	}
	if r.owned && (row.ID != observed.ID || !sameOwner(row.OwnerPublisherID, observed.OwnerPublisherID)) {
		return taxonomy.Record{}, taxonomy.Problem("TAXONOMY_CHANGED", "The taxonomy owner changed. Try again.", 409)
	}
	state := taxonomy.MutationState{}
	owner := c.PublisherID
	if op == taxonomy.Protect {
		owner = row.OwnerPublisherID
	}
	state.PublisherExists, err = publisherExists(ctx, tx, owner)
	if err != nil {
		return taxonomy.Record{}, err
	}
	if op == taxonomy.Protect && owner != nil {
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM taxonomy_candidate.post_tags WHERE owner_publisher_id=$1 AND is_protected AND deleted_at IS NULL", *owner).Scan(&state.ProtectedUsed)
		if err != nil {
			return taxonomy.Record{}, err
		}
	}
	next, err := apply(row, state)
	if err != nil {
		return taxonomy.Record{}, err
	}
	if next.ID != row.ID || next.Kind != row.Kind || !taxonomy.ValidID(next.AuditActorID) {
		return taxonomy.Record{}, taxonomy.ErrInvalid
	}
	if r.owned {
		if next.OwnerPublisherID != nil && !sameOwner(next.OwnerPublisherID, observed.OwnerPublisherID) && !sameOwner(next.OwnerPublisherID, c.PublisherID) {
			return taxonomy.Record{}, taxonomy.ErrInvalid
		}
		if err = r.authorizeRecord(ctx, tx, next, op); err != nil {
			return taxonomy.Record{}, err
		}
		if err = r.publisherPolicy(ctx, tx, row, next, op); err != nil {
			return taxonomy.Record{}, err
		}
	}
	if op == taxonomy.Delete {
		link, key := links(k)
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+link+" WHERE "+key+"=$1", row.ID); err != nil {
			return taxonomy.Record{}, err
		}
		// Independently designed internal policy: tombstone the taxonomy row
		// and its active subscriptions atomically. The source removes entities,
		// but omitted shared save hooks do not establish physical-delete behavior.
		subscriptionKey := "category_id"
		if k == taxonomy.Tag {
			subscriptionKey = "tag_id"
		}
		if _, err = tx.ExecContext(ctx, "UPDATE taxonomy_candidate.post_category_subscriptions SET deleted_at=$2,updated_at=$2 WHERE "+subscriptionKey+"=$1 AND deleted_at IS NULL", row.ID, next.UpdatedAt); err != nil {
			return taxonomy.Record{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE "+tbl+" SET deleted_at=$2,updated_at=$2 WHERE id=$1", row.ID, next.UpdatedAt)
	} else if k == taxonomy.Category {
		if next.Slug != row.Slug {
			if err = duplicate(ctx, tx, k, next.Slug, row.ID); err != nil {
				return taxonomy.Record{}, err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE "+tbl+" SET slug=$2,name=$3,updated_at=$4 WHERE id=$1", row.ID, next.Slug, next.Name, next.UpdatedAt)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE "+tbl+" SET name=$2,description=$3,owner_publisher_id=$4,is_protected=$5,is_event=$6,event_ends_at=$7,updated_at=$8 WHERE id=$1", row.ID, next.Name, next.Description, next.OwnerPublisherID, next.IsProtected, next.IsEvent, next.EventEndsAt, next.UpdatedAt)
	}
	if err != nil {
		return taxonomy.Record{}, err
	}
	if err = appendAudit(ctx, tx, next, string(op)); err != nil {
		return taxonomy.Record{}, err
	}
	if err = tx.Commit(); err != nil {
		return taxonomy.Record{}, err
	}
	return next, nil
}
func (r *Repository) Quota(ctx context.Context, id string) ([]taxonomy.QuotaRecord, error) {
	if err := r.guard(ctx); err != nil {
		return nil, err
	}
	if !taxonomy.ValidID(id) {
		return nil, taxonomy.ErrInvalid
	}
	q := `SELECT id::text,slug,name,description,is_protected,is_event,event_ends_at FROM taxonomy_candidate.post_tags WHERE owner_publisher_id=$1 AND deleted_at IS NULL`
	if r.owned {
		q += ` AND EXISTS(SELECT 1 FROM owned_publishers owner WHERE owner.id=$1 AND owner.type=0)`
	}
	rows, err := r.db.QueryContext(ctx, policySQL(q+` ORDER BY is_protected DESC,updated_at DESC`, r.owned), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []taxonomy.QuotaRecord{}
	for rows.Next() {
		var v taxonomy.QuotaRecord
		if err = rows.Scan(&v.ID, &v.Slug, &v.Name, &v.Description, &v.IsProtected, &v.IsEvent, &v.EventEndsAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (r *Repository) Subscriptions(ctx context.Context, account string, offset, take int) ([]taxonomy.Subscription, int64, error) {
	if err := r.guard(ctx); err != nil {
		return nil, 0, err
	}
	if !taxonomy.ValidID(account) || offset < 0 || take < 0 {
		return nil, 0, taxonomy.ErrInvalid
	}
	tx, err := r.begin(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var total int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM taxonomy_candidate.post_category_subscriptions WHERE account_id=$1 AND deleted_at IS NULL", account).Scan(&total); err != nil {
		return nil, 0, err
	}
	// The pinned source calls Take(take).Skip(offset), not Skip().Take().
	rows, err := tx.QueryContext(ctx, `SELECT id::text,account_id::text,category_id::text,tag_id::text,collection_id::text FROM taxonomy_candidate.post_category_subscriptions WHERE account_id=$1 AND deleted_at IS NULL ORDER BY created_at DESC OFFSET $2 LIMIT $3`, account, offset, max(0, take-offset))
	if err != nil {
		return nil, 0, err
	}
	result := []taxonomy.Subscription{}
	for rows.Next() {
		v, e := scanSubscription(rows)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		result = append(result, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}
func scanSubscription(row scanner) (taxonomy.Subscription, error) {
	var v taxonomy.Subscription
	err := row.Scan(&v.ID, &v.AccountID, &v.CategoryID, &v.TagID, &v.CollectionID)
	return v, err
}
func (r *Repository) Subscription(ctx context.Context, k taxonomy.Kind, slug, account, action string, now time.Time) (taxonomy.Subscription, bool, error) {
	if err := r.guard(ctx); err != nil {
		return taxonomy.Subscription{}, false, err
	}
	if !taxonomy.ValidID(account) || action != "subscription" && action != "subscribe" && action != "unsubscribe" {
		return taxonomy.Subscription{}, false, taxonomy.ErrInvalid
	}
	tbl, err := table(k)
	if err != nil {
		return taxonomy.Subscription{}, false, err
	}
	tx, err := r.begin(ctx, nil)
	if err != nil {
		return taxonomy.Subscription{}, false, err
	}
	defer tx.Rollback()
	if r.owned {
		permission := ""
		if action != "subscription" {
			permission = "post.categories.subscribe"
		}
		if err = r.authorize(ctx, tx, account, permission); err != nil {
			return taxonomy.Subscription{}, false, err
		}
	}
	// Lock taxonomy before subscriptions, same order as category/tag deletion.
	// A shared table lock protects the target from concurrent fixture deletion.
	if _, err = tx.ExecContext(ctx, "LOCK TABLE "+tbl+" IN SHARE MODE"); err != nil {
		return taxonomy.Subscription{}, false, err
	}
	if action != "subscription" {
		if _, err = tx.ExecContext(ctx, "LOCK TABLE taxonomy_candidate.post_category_subscriptions IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return taxonomy.Subscription{}, false, err
		}
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id::text FROM "+tbl+" WHERE lower(slug)=$1 AND deleted_at IS NULL LIMIT 1", taxonomy.LowerInvariant(slug)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		code, message := "CATEGORY_NOT_FOUND", "Category not found."
		if k == taxonomy.Tag {
			code, message = "POST_TAG_NOT_FOUND", "Tag not found."
		}
		return taxonomy.Subscription{}, false, taxonomy.Problem(code, message, 404)
	}
	if err != nil {
		return taxonomy.Subscription{}, false, err
	}
	key := "category_id"
	if k == taxonomy.Tag {
		key = "tag_id"
	}
	row, err := scanSubscription(tx.QueryRowContext(ctx, "SELECT id::text,account_id::text,category_id::text,tag_id::text,collection_id::text FROM taxonomy_candidate.post_category_subscriptions WHERE "+key+"=$1 AND account_id=$2 AND deleted_at IS NULL LIMIT 1", id, account))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return taxonomy.Subscription{}, false, err
	}
	created := false
	switch action {
	case "subscription":
		if !exists {
			code := "CATEGORY_SUBSCRIPTION_NOT_FOUND"
			if k == taxonomy.Tag {
				code = "TAG_SUBSCRIPTION_NOT_FOUND"
			}
			return taxonomy.Subscription{}, false, taxonomy.Problem(code, "Subscription not found.", 404)
		}
	case "subscribe":
		if !exists {
			// PostgreSQL UUID generation is an explicit fixture ID policy.
			row = taxonomy.Subscription{AccountID: account}
			if k == taxonomy.Tag {
				row.TagID = &id
			} else {
				row.CategoryID = &id
			}
			err = tx.QueryRowContext(ctx, "INSERT INTO taxonomy_candidate.post_category_subscriptions (id,account_id,"+key+",created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,$3,$3) RETURNING id::text", account, id, now).Scan(&row.ID)
			created = true
		}
	case "unsubscribe":
		if exists {
			_, err = tx.ExecContext(ctx, "UPDATE taxonomy_candidate.post_category_subscriptions SET deleted_at=$2,updated_at=$2 WHERE id=$1", row.ID, now)
		} else {
			err = nil
		}
	}
	if err != nil {
		return taxonomy.Subscription{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return taxonomy.Subscription{}, false, err
	}
	return row, created, nil
}

// This local audit ledger is an independent transactional implementation. It
// does not claim to invoke the external RemoteActionLogService wire contract.
func appendAudit(ctx context.Context, tx *policyTx, row taxonomy.Record, operation string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO taxonomy_candidate.actions(actor_id,is_admin,resource_kind,resource_id,operation,occurred_at) VALUES($1,$2,$3,$4,$5,$6)`, row.AuditActorID, row.AuditAdmin, string(row.Kind), row.ID, operation, row.UpdatedAt)
	return err
}

// Owned organization tag ownership has no public realm policy yet. Retained or
// imported rows cannot reveal a private publisher through global taxonomy.
const ownedTagPublisherScope = `(t.owner_publisher_id IS NULL OR EXISTS(SELECT 1 FROM owned_publishers owner WHERE owner.id=t.owner_publisher_id AND owner.type=0))`
