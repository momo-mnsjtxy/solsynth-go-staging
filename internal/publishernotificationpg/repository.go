// SPDX-License-Identifier: AGPL-3.0-only
package publishernotificationpg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/sphere/posts"
)

//go:embed schema.sql
var Schema string

type Sink interface {
	EnqueueInTransaction(context.Context, *sql.DB, *sql.Tx, notifications.Command) (string, error)
}

type Repository struct {
	db         *sql.DB
	visibility postpg.VisibilityProvider
	sink       Sink
	app        string
}

func New(db *sql.DB, visibility postpg.VisibilityProvider, sink Sink, app string) *Repository {
	return &Repository{db: db, visibility: visibility, sink: sink, app: app}
}

func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.visibility == nil || r.sink == nil || !notifications.ValidApp(r.app) {
		return notifications.ErrUnavailable
	}
	var ok bool
	e := r.db.QueryRowContext(ctx, `WITH expected(relation,name,kind,columns,foreign_relation,foreign_columns,fragments) AS (VALUES
	 ('public.owned_publisher_follows','owned_publisher_follow_incarnation','c',NULL::text,'',NULL::text,ARRAY['incarnation','> 0']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_key','p','id','',NULL::text,ARRAY['PRIMARY KEY (id)']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_kind','c',NULL::text,'',NULL::text,ARRAY['kind','post.published','livestream.started']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_shape','c',NULL::text,'',NULL::text,ARRAY['kind','source_generation','source_id','post_id','livestream_id','post.published','livestream.started']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_publisher','f','publisher_id','public.owned_publishers','id',ARRAY['FOREIGN KEY (publisher_id)']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_post','f','post_id','public.owned_text_posts','id',ARRAY['FOREIGN KEY (post_id)']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_livestream','f','livestream_id','public.owned_livestreams','id',ARRAY['FOREIGN KEY (livestream_id)']),
	 ('public.owned_publisher_content_notification_intents','owned_publisher_content_intent_source','u','kind,source_id,source_generation','',NULL::text,ARRAY['UNIQUE (kind, source_id, source_generation)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_intent','f','intent_id','public.owned_publisher_content_notification_intents','id',ARRAY['FOREIGN KEY (intent_id)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_subscription','f','subscription_id','public.owned_publisher_follows','id',ARRAY['FOREIGN KEY (subscription_id)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_account','f','account_id','public.owned_authority_accounts','id',ARRAY['FOREIGN KEY (account_id)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_outbox','f','outbox_id','notification_v1.outbox','id',ARRAY['FOREIGN KEY (outbox_id)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_key','p','intent_id,subscription_id,subscription_incarnation','',NULL::text,ARRAY['PRIMARY KEY (intent_id, subscription_id, subscription_incarnation)']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_incarnation','c',NULL::text,'',NULL::text,ARRAY['subscription_incarnation','> 0']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_kind','c',NULL::text,'',NULL::text,ARRAY['outcome','enqueued','suppressed']),
	 ('public.owned_publisher_content_notification_receipts','owned_publisher_content_receipt_outcome','c',NULL::text,'',NULL::text,ARRAY['outcome','enqueued','outbox_id IS NOT NULL']))
	 SELECT count(*)=16 AND bool_and(c.oid IS NOT NULL AND c.convalidated AND c.contype::text=expected.kind
	  AND (expected.foreign_relation='' OR c.confrelid=to_regclass(expected.foreign_relation))
	  AND (expected.columns IS NULL OR expected.columns=(SELECT string_agg(a.attname,',' ORDER BY key_col.ordinality) FROM unnest(c.conkey) WITH ORDINALITY key_col(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key_col.attnum))
	  AND (expected.foreign_columns IS NULL OR expected.foreign_columns=(SELECT string_agg(a.attname,',' ORDER BY key_col.ordinality) FROM unnest(c.confkey) WITH ORDINALITY key_col(attnum,ordinality) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=key_col.attnum))
	  AND NOT EXISTS(SELECT 1 FROM unnest(expected.fragments) fragment WHERE position(fragment in pg_get_constraintdef(c.oid))=0))
	 FROM expected LEFT JOIN pg_constraint c ON c.conrelid=to_regclass(expected.relation) AND c.conname=expected.name`).Scan(&ok)
	if e != nil || !ok {
		return errors.New("publisher content notification constraints unavailable")
	}
	e = r.db.QueryRowContext(ctx, `WITH expected(relation,column_name,type_name,not_null) AS (VALUES
	 ('public.owned_publisher_follows','incarnation','bigint',true),('public.owned_publisher_follows','incarnation_started_at','timestamp with time zone',true),
	 ('public.owned_publisher_content_notification_intents','id','uuid',true),('public.owned_publisher_content_notification_intents','kind','text',true),('public.owned_publisher_content_notification_intents','source_id','uuid',true),('public.owned_publisher_content_notification_intents','source_generation','bigint',true),('public.owned_publisher_content_notification_intents','post_id','uuid',false),('public.owned_publisher_content_notification_intents','livestream_id','uuid',false),('public.owned_publisher_content_notification_intents','publisher_id','uuid',true),('public.owned_publisher_content_notification_intents','occurred_at','timestamp with time zone',true),('public.owned_publisher_content_notification_intents','completed_at','timestamp with time zone',false),
	 ('public.owned_publisher_content_notification_receipts','intent_id','uuid',true),('public.owned_publisher_content_notification_receipts','subscription_id','uuid',true),('public.owned_publisher_content_notification_receipts','subscription_incarnation','bigint',true),('public.owned_publisher_content_notification_receipts','account_id','uuid',true),('public.owned_publisher_content_notification_receipts','outbox_id','uuid',false))
	 SELECT count(*)=16 AND bool_and(a.attname=expected.column_name AND format_type(a.atttypid,a.atttypmod)=expected.type_name AND a.attnotnull=expected.not_null AND NOT a.attisdropped)
	 FROM expected LEFT JOIN pg_attribute a ON a.attrelid=to_regclass(expected.relation) AND a.attname=expected.column_name AND a.attnum>0`).Scan(&ok)
	if e != nil || !ok {
		return errors.New("publisher content notification columns unavailable")
	}
	e = r.db.QueryRowContext(ctx, `SELECT i.indisvalid AND i.indisready AND NOT i.indisunique
	 AND pg_get_indexdef(i.indexrelid) LIKE '%USING btree (created_at, id)%'
	 AND pg_get_expr(i.indpred,i.indrelid)='(completed_at IS NULL)'
	 FROM pg_index i WHERE i.indexrelid=to_regclass('public.owned_publisher_content_notification_due')
	 AND i.indrelid=to_regclass('public.owned_publisher_content_notification_intents')`).Scan(&ok)
	if e != nil || !ok {
		return errors.New("publisher content notification index unavailable")
	}
	e = r.db.QueryRowContext(ctx, `SELECT count(*)=1 AND bool_and(NOT t.tgisinternal AND t.tgenabled='O' AND t.tgtype=27 AND p.proname='owned_publisher_content_intent_guard' AND n.nspname='public')
	 FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace n ON n.oid=p.pronamespace
	 WHERE t.tgrelid=to_regclass('public.owned_publisher_content_notification_intents') AND t.tgname='owned_publisher_content_intent_immutable'`).Scan(&ok)
	if e != nil || !ok {
		return errors.New("publisher content notification trigger unavailable")
	}
	return nil
}

func (r *Repository) record(ctx context.Context, db *sql.DB, tx *sql.Tx, kind, source string, generation int64, publisher string, at time.Time) error {
	if r == nil || db == nil || db != r.db || tx == nil || at.IsZero() {
		return notifications.ErrUnavailable
	}
	at = at.UTC().Truncate(time.Microsecond)
	id, e := uuid()
	if e != nil {
		return e
	}
	var gotPublisher string
	var gotAt time.Time
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_publisher_content_notification_intents(id,kind,source_id,source_generation,post_id,livestream_id,publisher_id,occurred_at)
	 VALUES($1,$2,$3,$4,CASE WHEN $2='post.published' THEN $3::uuid END,CASE WHEN $2='livestream.started' THEN $3::uuid END,$5,$6) ON CONFLICT(kind,source_id,source_generation) DO NOTHING`, id, kind, source, generation, publisher, at)
	if e != nil {
		return e
	}
	e = tx.QueryRowContext(ctx, `SELECT publisher_id::text,occurred_at FROM owned_publisher_content_notification_intents WHERE kind=$1 AND source_id=$2 AND source_generation=$3`, kind, source, generation).Scan(&gotPublisher, &gotAt)
	if e != nil {
		return e
	}
	if gotPublisher != publisher || !gotAt.Equal(at) {
		return notifications.ErrConflict
	}
	return nil
}

func (r *Repository) RecordPublication(ctx context.Context, db *sql.DB, tx *sql.Tx, p posts.Post) error {
	if p.PublishedAt == nil {
		return nil
	}
	return r.record(ctx, db, tx, "post.published", p.ID, 0, p.PublisherID, *p.PublishedAt)
}

func (r *Repository) RecordLivestreamActivation(ctx context.Context, db *sql.DB, tx *sql.Tx, streamID, publisherID string, generation int64, at time.Time) error {
	return r.record(ctx, db, tx, "livestream.started", streamID, generation, publisherID, at)
}
