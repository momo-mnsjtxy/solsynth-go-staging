// SPDX-License-Identifier: AGPL-3.0-only
package accounthistorypg

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"math"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/history"
	"sort"
	"strings"
)

//go:embed schema.sql
var Schema string

type Guard interface {
	Preflight(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
	Authenticate(context.Context, *sql.DB, *sql.Tx, string) (authority.Principal, error)
	Authorize(context.Context, *sql.DB, *sql.Tx, string, ...string) (authority.Principal, error)
}
type Repository struct {
	db       *sql.DB
	guard    Guard
	timeline history.TimelineRepository
}

func New(db *sql.DB, g Guard, t history.TimelineRepository) *Repository { return &Repository{db, g, t} }

var _ history.Repository = (*Repository)(nil)

func (r *Repository) Ready(ctx context.Context) error {
	if r == nil || r.db == nil || r.guard == nil || r.timeline == nil {
		return history.ErrUnavailable
	}
	_, e := r.db.ExecContext(ctx, `SELECT id,source_id FROM owned_account_action_history LIMIT 0; SELECT id,source_kind,source_ref,source_record FROM owned_account_credit_records LIMIT 0; SELECT id,verified_at,source_ref FROM owned_account_connections LIMIT 0`)
	return e
}
func (r *Repository) Timeline(ctx context.Context, actor, ref string, f history.Filter) ([]history.TimelineItem, int, error) {
	if r == nil || r.timeline == nil {
		return nil, 0, history.ErrUnavailable
	}
	return r.timeline.Timeline(ctx, actor, ref, f)
}
func (r *Repository) check(ctx context.Context, tx *sql.Tx, actor, permission string) error {
	if actor == "" {
		return nil
	}
	if r.guard == nil {
		return authority.ErrUnavailable
	}
	var e error
	if permission == "" {
		_, e = r.guard.Authenticate(ctx, r.db, tx, actor)
	} else {
		_, e = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	}
	return e
}
func (r *Repository) begin(ctx context.Context, actor, ref, permission string) (tx *sql.Tx, target string, e error) {
	if r == nil || r.db == nil || r.guard == nil {
		return nil, "", history.ErrUnavailable
	}
	if actor != "" && !authority.ValidID(actor) {
		return nil, "", authority.ErrInvalid
	}
	if ref == "" || len(ref) > 256 {
		return nil, "", history.ErrNotFound
	}
	tx, e = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return
	}
	ok := false
	defer func() {
		if !ok {
			_ = tx.Rollback()
		}
	}()
	if actor != "" {
		var permissions []string
		if permission != "" {
			permissions = []string{permission}
		}
		if _, e = r.guard.Preflight(ctx, r.db, tx, actor, permissions...); e != nil {
			return
		}
	}
	match := `lower(name)=lower($1)`
	if authority.ValidID(ref) {
		match = `id=$1::uuid`
	}
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE `+match, ref).Scan(&target)
	if errors.Is(e, sql.ErrNoRows) {
		e = history.ErrNotFound
	}
	if e != nil {
		return
	}
	ids := []string{target}
	if actor != "" && actor != target {
		ids = append(ids, actor)
	}
	sort.Strings(ids)
	for _, id := range ids {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, id).Scan(&found); e != nil {
			return
		}
	}
	if e = r.check(ctx, tx, actor, permission); e != nil {
		return
	}
	var current string
	e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE `+match, ref).Scan(&current)
	if errors.Is(e, sql.ErrNoRows) || e == nil && current != target {
		e = history.ErrNotFound
	}
	if e != nil {
		return
	}
	if e = eligible(ctx, tx, target); e != nil {
		return
	}
	ok = true
	return
}
func eligible(ctx context.Context, tx *sql.Tx, id string) error {
	var active bool
	e := tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid`, id).Scan(&active)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !active {
		return history.ErrNotFound
	}
	return e
}

// Credits use the existing account presence privacy policy, including both
// directions of blocking. A private projection is never represented as zero.
func visible(ctx context.Context, tx *sql.Tx, actor, target string) error {
	if e := eligible(ctx, tx, target); e != nil {
		return e
	}
	if actor == target {
		return nil
	}
	var allow bool
	e := tx.QueryRowContext(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE status=-100 AND ((account_id=$1::uuid AND related_id=NULLIF($2::text,'')::uuid)OR(related_id=$1::uuid AND account_id=NULLIF($2::text,'')::uuid)))
 AND COALESCE((SELECT type<>3 FROM owned_presence_statuses WHERE account_id=$1::uuid AND(cleared_at IS NULL OR cleared_at>clock_timestamp())ORDER BY created_at DESC,id LIMIT 1),true)
 AND(COALESCE((SELECT status_visibility FROM owned_presence_preferences WHERE account_id=$1::uuid),'public')='public'
 OR COALESCE((SELECT status_visibility FROM owned_presence_preferences WHERE account_id=$1::uuid),'public')='friends'
 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE account_id=$1::uuid AND related_id=NULLIF($2::text,'')::uuid AND status>=100)
 AND EXISTS(SELECT 1 FROM owned_social_effective_relationships WHERE related_id=$1::uuid AND account_id=NULLIF($2::text,'')::uuid AND status>=100))`, target, actor).Scan(&allow)
	if e == nil && !allow {
		return history.ErrNotFound
	}
	return e
}

const bounds = ` AND($2::timestamptz IS NULL OR created_at >= $2::timestamptz) AND($3::timestamptz IS NULL OR created_at < $3::timestamptz)`

func (r *Repository) Actions(ctx context.Context, actor string, f history.Filter) (out []history.ActionLog, total int, e error) {
	out = []history.ActionLog{}
	if actor == "" {
		return nil, 0, authority.ErrInvalid
	}
	if e = history.Validate(f); e != nil {
		return
	}
	tx, _, e := r.begin(ctx, actor, actor, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	// Count and page share one SQL snapshot, including an empty/out-of-range page.
	rows, e := tx.QueryContext(ctx, `WITH filtered AS MATERIALIZED(SELECT * FROM owned_account_action_history WHERE account_id=$1::uuid`+bounds+` AND position(lower($4::text) in lower(action))>0),page AS(SELECT * FROM filtered ORDER BY created_at DESC,id LIMIT $5::integer OFFSET $6::integer) SELECT totals.n,page.id::text,page.action,page.target,page.source,page.source_id,page.created_at FROM(SELECT count(*) AS n FROM filtered)totals LEFT JOIN page ON true ORDER BY page.created_at DESC,page.id`, actor, f.From, f.To, f.Action, f.Take, f.Offset)
	if e != nil {
		return
	}
	for rows.Next() {
		var id, action, target, source, sourceID sql.NullString
		var created sql.NullTime
		if e = rows.Scan(&total, &id, &action, &target, &source, &sourceID, &created); e != nil {
			break
		}
		if id.Valid {
			out = append(out, history.ActionLog{ID: id.String, AccountID: actor, Action: action.String, Meta: map[string]any{"target_id": target.String, "source": source.String, "source_id": sourceID.String}, CreatedAt: created.Time, UpdatedAt: created.Time})
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	e = tx.Commit()
	return
}
func (r *Repository) CreditHistory(ctx context.Context, actor string, f history.Filter) (out []history.CreditRecord, total int, e error) {
	out = []history.CreditRecord{}
	if actor == "" {
		return nil, 0, authority.ErrInvalid
	}
	if e = history.Validate(f); e != nil {
		return
	}
	tx, _, e := r.begin(ctx, actor, actor, "")
	if e != nil {
		return
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `WITH filtered AS MATERIALIZED(SELECT * FROM owned_account_credit_records WHERE account_id=$1::uuid`+bounds+`),page AS(SELECT * FROM filtered ORDER BY created_at DESC,id LIMIT $4::integer OFFSET $5::integer) SELECT totals.n,page.id::text,page.reason_type,page.reason,page.delta,page.created_at,page.expired_at FROM(SELECT count(*) AS n FROM filtered)totals LEFT JOIN page ON true ORDER BY page.created_at DESC,page.id`, actor, f.From, f.To, f.Take, f.Offset)
	if e != nil {
		return
	}
	for rows.Next() {
		var id, kind, reason sql.NullString
		var delta sql.NullFloat64
		var created, expired sql.NullTime
		if e = rows.Scan(&total, &id, &kind, &reason, &delta, &created, &expired); e != nil {
			break
		}
		if id.Valid {
			v := history.CreditRecord{ID: id.String, AccountID: actor, ReasonType: kind.String, Reason: reason.String, Delta: delta.Float64, CreatedAt: created.Time, UpdatedAt: created.Time}
			if expired.Valid {
				v.ExpiredAt = &expired.Time
			}
			out = append(out, v)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if e = r.check(ctx, tx, actor, ""); e != nil {
		return nil, 0, e
	}
	e = tx.Commit()
	return
}
func (r *Repository) Credits(ctx context.Context, actor, ref string) (float64, error) {
	tx, target, e := r.begin(ctx, actor, ref, "")
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if e = visible(ctx, tx, actor, target); e != nil {
		return 0, e
	}
	// PostgreSQL performs the unbounded immutable ledger aggregation. Source
	// precision is double; explicit ordered summation makes repeat reads stable.
	var total float64
	e = tx.QueryRowContext(ctx, `WITH instant AS MATERIALIZED(SELECT clock_timestamp() AS now),deltas AS(SELECT id,created_at,CASE WHEN expired_at<=instant.now THEN 0::double precision WHEN expired_at IS NULL OR expired_at=created_at THEN delta ELSE delta*GREATEST(0::double precision,1-extract(epoch FROM(instant.now-created_at))::double precision/NULLIF(extract(epoch FROM(expired_at-created_at))::double precision,0)) END AS effective FROM owned_account_credit_records CROSS JOIN instant WHERE account_id=$1::uuid) SELECT 100::double precision+COALESCE(sum(effective ORDER BY created_at,id),0::double precision) FROM deltas`, target).Scan(&total)
	if e != nil {
		return 0, e
	}
	if math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, history.ErrUnavailable
	}
	if e = r.check(ctx, tx, actor, ""); e != nil {
		return 0, e
	}
	if e = visible(ctx, tx, actor, target); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	return total, nil
}
func (r *Repository) Connections(ctx context.Context, actor, provider string) (out []history.Connection, e error) {
	out = []history.Connection{}
	if actor == "" {
		return nil, authority.ErrInvalid
	}
	if len(provider) > 128 || strings.ContainsRune(provider, 0) {
		return nil, history.ErrInvalid
	}
	tx, _, e := r.begin(ctx, actor, actor, "account.connections")
	if e != nil {
		return
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id::text,provider,provided_identifier,email,username,display_name,is_public,last_used_at,created_at,updated_at FROM owned_account_connections WHERE account_id=$1::uuid AND revoked_at IS NULL AND verified_at<=clock_timestamp() AND($2::text='' OR provider=$2::text) ORDER BY provider,id LIMIT 2001`, actor, provider)
	if e != nil {
		return
	}
	for rows.Next() {
		var v history.Connection
		var email, username, name sql.NullString
		if e = rows.Scan(&v.ID, &v.Provider, &v.ProvidedIdentifier, &email, &username, &name, &v.IsPublic, &v.LastUsedAt, &v.CreatedAt, &v.UpdatedAt); e != nil {
			break
		}
		v.AccountID = actor
		v.Meta = map[string]string{}
		for k, p := range map[string]sql.NullString{"email": email, "username": username, "display_name": name} {
			if p.Valid {
				v.Meta[k] = p.String
			}
		}
		out = append(out, v)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(out) > 2000 {
		return nil, history.ErrUnavailable
	}
	if e = r.check(ctx, tx, actor, "account.connections"); e != nil {
		return nil, e
	}
	e = tx.Commit()
	return
}
