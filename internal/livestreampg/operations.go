// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

const operationsInspectPermission = "livestreams.operations.inspect"
const operationsRedrivePermission = "livestreams.operations.redrive"

func (r *Repository) operationAuthority(ctx context.Context, actor, permission string) (*sql.Tx, authority.Principal, error) {
	var zero authority.Principal
	if !authority.ValidID(actor) {
		return nil, zero, live.ErrUnauthorized
	}
	tx, e := r.begin(ctx)
	if e != nil {
		return nil, zero, e
	}
	fail := func(e error) (*sql.Tx, authority.Principal, error) { tx.Rollback(); return nil, zero, e }
	if _, e = r.guard.Preflight(ctx, r.db, tx, actor, permission); e != nil {
		return fail(e)
	}
	var held string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_authority_accounts WHERE id=$1::uuid FOR UPDATE`, actor).Scan(&held); e != nil {
		return fail(e)
	}
	p, e := r.guard.Authorize(ctx, r.db, tx, actor, permission)
	if e != nil {
		return fail(e)
	}
	if !p.Account.Superuser || p.Session.Type != 0 || p.Session.ParentID != "" {
		return fail(live.ErrForbidden)
	}
	return tx, p, nil
}

func operationKinds(r *Repository, kind string) ([]string, error) {
	if kind != "" && kind != "output" && kind != "award" && kind != "archive" {
		return nil, live.ErrInvalid
	}
	out := []string{}
	if r.outputs != nil && (kind == "" || kind == "output") {
		out = append(out, `SELECT 'output',id::text,stream_id::text,phase,reason,attempts,next_attempt_at FROM owned_livestream_outputs WHERE phase<>'done'`)
	}
	if r.awards != nil && (kind == "" || kind == "award") {
		out = append(out, `SELECT 'award',r.purchase_id::text,p.stream_id::text,r.payout_state,COALESCE(r.reason,''),r.attempts,r.next_attempt_at FROM owned_livestream_award_receipts r JOIN owned_livestream_award_purchases p ON p.id=r.purchase_id WHERE r.payout_state IN('held','refund_pending','refund_ready')`)
	}
	if r.outputs != nil && (kind == "" || kind == "archive") {
		out = append(out, `SELECT 'archive',id::text,stream_id::text,state,reason,attempts,next_attempt_at FROM owned_livestream_archive_imports WHERE state IN('pending','processing','failed')`)
	}
	return out, nil
}

func (r *Repository) InspectOperations(ctx context.Context, actor, kind string, limit, offset int) (out []live.Operation, total int, e error) {
	if !authority.ValidID(actor) || limit < 1 || limit > 100 || offset < 0 || offset > 100000 {
		return nil, 0, live.ErrInvalid
	}
	queries, e := operationKinds(r, kind)
	if e != nil {
		return nil, 0, e
	}
	tx, p, e := r.operationAuthority(ctx, actor, operationsInspectPermission)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if p.Account.ID != actor {
		return nil, 0, live.ErrForbidden
	}
	if len(queries) == 0 {
		return []live.Operation{}, 0, tx.Commit()
	}
	union := strings.Join(queries, ` UNION ALL `)
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM (`+union+`) operations`).Scan(&total); e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT * FROM (`+union+`) operations ORDER BY next_attempt_at,kind,id LIMIT $1::integer OFFSET $2::integer`, limit, offset)
	if e != nil {
		return nil, 0, e
	}
	out = []live.Operation{}
	for rows.Next() {
		var v live.Operation
		if e = rows.Scan(&v.Kind, &v.ID, &v.StreamID, &v.State, &v.Reason, &v.Attempts, &v.NextAttemptAt); e != nil {
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
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, operationsInspectPermission); e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}

func (r *Repository) RedriveOperation(ctx context.Context, actor, kind, id string) (out live.Operation, e error) {
	if !authority.ValidID(actor) || !authority.ValidID(id) || kind != "output" && kind != "award" && kind != "archive" {
		return out, live.ErrInvalid
	}
	tx, p, e := r.operationAuthority(ctx, actor, operationsRedrivePermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if p.Account.ID != actor {
		return out, live.ErrForbidden
	}
	var query string
	switch kind {
	case "output":
		if r.outputs == nil {
			return out, live.ErrOutputUnavailable
		}
		query = `UPDATE owned_livestream_outputs SET attempts=0,next_attempt_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1::uuid AND phase<>'done' RETURNING 'output',id::text,stream_id::text,phase,reason,attempts,next_attempt_at`
	case "award":
		if r.awards == nil {
			return out, live.ErrAwardUnavailable
		}
		query = `UPDATE owned_livestream_award_receipts r SET attempts=0,next_attempt_at=clock_timestamp(),reason=NULL FROM owned_livestream_award_purchases p WHERE r.purchase_id=$1::uuid AND p.id=r.purchase_id AND r.payout_state IN('held','refund_pending','refund_ready') RETURNING 'award',r.purchase_id::text,p.stream_id::text,r.payout_state,COALESCE(r.reason,''),r.attempts,r.next_attempt_at`
	case "archive":
		if r.outputs == nil || r.outputs.ArchiveImporter == nil {
			return out, live.ErrOutputUnavailable
		}
		query = `UPDATE owned_livestream_archive_imports SET state='pending',attempts=0,next_attempt_at=clock_timestamp(),updated_at=clock_timestamp(),reason='',lease_token=NULL,lease_expires_at=NULL WHERE id=$1::uuid AND state IN('pending','processing','failed') RETURNING 'archive',id::text,stream_id::text,state,reason,attempts,next_attempt_at`
	}
	e = tx.QueryRowContext(ctx, query, id).Scan(&out.Kind, &out.ID, &out.StreamID, &out.State, &out.Reason, &out.Attempts, &out.NextAttemptAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, live.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, out.StreamID, actor, "livestreams.operations."+kind+".redrive"); e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, operationsRedrivePermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
