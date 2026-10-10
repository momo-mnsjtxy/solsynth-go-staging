// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"database/sql"
	"errors"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"strings"
)

const effectiveQuota = `COALESCE(q.base_limit_bytes,q.limit_bytes)+COALESCE((SELECT sum(quota_bytes) FROM owned_media_quota_records b WHERE b.account_id=q.account_id AND (b.expired_at IS NULL OR b.expired_at>clock_timestamp())),0)`

func (r *Repository) StorageUsage(ctx context.Context, actor, pool string) (media.StorageUsage, error) {
	out := media.StorageUsage{PoolUsages: []media.UsageDetails{}}
	if pool != "" && !authority.ValidID(pool) {
		return out, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `SELECT `+effectiveQuota+`,used_bytes,reserved_bytes FROM owned_media_quotas q WHERE q.account_id=$1`, actor).Scan(&out.TotalQuota, &out.UsedQuota, &out.ReservedQuota); e != nil {
		return out, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT COALESCE(d.pool_id::text,''),COALESCE(p.name,'configured default storage'),sum(f.size),count(*) FROM owned_media_files f LEFT JOIN owned_media_details d ON d.file_id=f.id LEFT JOIN owned_media_pools p ON p.id=d.pool_id WHERE f.account_id=$1 AND f.charge='used' AND ($2='' OR d.pool_id=NULLIF($2,'')::uuid) GROUP BY d.pool_id,p.name ORDER BY d.pool_id`, actor, pool)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v media.UsageDetails
		if e = rows.Scan(&v.PoolID, &v.PoolName, &v.UsageBytes, &v.FileCount); e != nil {
			rows.Close()
			return out, e
		}
		out.PoolUsages = append(out.PoolUsages, v)
		out.TotalUsageBytes += v.UsageBytes
		out.TotalFileCount += v.FileCount
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}

const quotaRecordFields = `id::text,account_id::text,source_key,name,description,quota_bytes,expired_at,created_at`

func scanQuotaRecord(row interface{ Scan(...any) error }) (v media.QuotaRecord, e error) {
	e = row.Scan(&v.ID, &v.AccountID, &v.SourceKey, &v.Name, &v.Description, &v.QuotaBytes, &v.ExpiredAt, &v.CreatedAt)
	return v, e
}
func (r *Repository) QuotaRecords(ctx context.Context, actor string, expired bool, take, offset int) ([]media.QuotaRecord, int64, error) {
	if take < 1 || take > 100 || offset < 0 || offset > 100000 {
		return nil, 0, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	var n int64
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_quota_records WHERE account_id=$1 AND ($2 OR expired_at IS NULL OR expired_at>clock_timestamp())`, actor, expired).Scan(&n)
	if e != nil {
		return nil, 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+quotaRecordFields+` FROM owned_media_quota_records WHERE account_id=$1 AND ($2 OR expired_at IS NULL OR expired_at>clock_timestamp()) ORDER BY created_at DESC,id LIMIT $3 OFFSET $4`, actor, expired, take, offset)
	if e != nil {
		return nil, 0, e
	}
	out := []media.QuotaRecord{}
	for rows.Next() {
		v, e := scanQuotaRecord(rows)
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
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return nil, 0, e
	}
	return out, n, tx.Commit()
}
func (r *Repository) AddQuotaRecord(ctx context.Context, actor string, v media.QuotaRecord) (media.QuotaRecord, error) {
	if !authority.ValidID(actor) || !authority.ValidID(v.AccountID) || !nameOK(v.Name) || len(v.Description) > 4096 || len(v.SourceKey) < 1 || len(v.SourceKey) > 128 || strings.ContainsRune(v.SourceKey, 0) || v.QuotaBytes < 1 || v.QuotaBytes > 1<<40 {
		return v, media.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id IN($1,$2) ORDER BY id FOR UPDATE`, actor, v.AccountID)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return v, e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "media.quotas.manage"); e != nil {
		return v, e
	}
	if e = artworkOwnerActive(ctx, tx, v.AccountID); e != nil {
		return v, e
	}
	if e = validateFutureExpiry(ctx, tx, v.ExpiredAt); e != nil {
		return v, e
	}
	var prior media.QuotaRecord
	prior, e = scanQuotaRecord(tx.QueryRowContext(ctx, `SELECT `+quotaRecordFields+` FROM owned_media_quota_records WHERE account_id=$1 AND source_key=$2`, v.AccountID, v.SourceKey))
	if e == nil {
		sameExpiry := prior.ExpiredAt == nil && v.ExpiredAt == nil || prior.ExpiredAt != nil && v.ExpiredAt != nil && prior.ExpiredAt.Equal(*v.ExpiredAt)
		if prior.Name != v.Name || prior.Description != v.Description || prior.QuotaBytes != v.QuotaBytes || !sameExpiry {
			return v, media.ErrConflict
		}
		if e = artworkOwnerActive(ctx, tx, v.AccountID); e != nil {
			return v, e
		}
		if e = validateFutureExpiry(ctx, tx, v.ExpiredAt); e != nil {
			return v, e
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "media.quotas.manage"); e != nil {
			return v, e
		}
		return prior, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return v, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_quota_records WHERE account_id=$1`, v.AccountID).Scan(&count); e != nil {
		return v, e
	}
	if count >= 1000 {
		return v, media.ErrQuota
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_media_quotas SET base_limit_bytes=COALESCE(base_limit_bytes,limit_bytes),limit_bytes=limit_bytes+$2 WHERE account_id=$1`, v.AccountID, v.QuotaBytes)
	if e != nil {
		return v, e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return v, e
	}
	if n == 0 {
		return v, media.ErrQuotaMissing
	}
	v.ID, e = uuid()
	if e != nil {
		return v, e
	}
	v, e = scanQuotaRecord(tx.QueryRowContext(ctx, `INSERT INTO owned_media_quota_records(id,account_id,source_key,name,description,quota_bytes,expired_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()) RETURNING `+quotaRecordFields, v.ID, v.AccountID, v.SourceKey, v.Name, v.Description, v.QuotaBytes, v.ExpiredAt))
	if e != nil {
		return v, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "media.quotas.manage"); e != nil {
		return v, e
	}
	if e = artworkOwnerActive(ctx, tx, v.AccountID); e != nil {
		return v, e
	}
	if e = validateFutureExpiry(ctx, tx, v.ExpiredAt); e != nil {
		return v, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "media.quotas.manage"); e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *Repository) CleanupRecycled(ctx context.Context, actor, pool string) (int, error) {
	if pool == "" {
		return r.CleanupOwnedDeletions(ctx, actor, 100)
	}
	if pool != "" && !authority.ValidID(pool) {
		return 0, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.DeletePermission)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if pool != "" {
		var owner string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_pools WHERE id=$1`, pool).Scan(&owner)
		if errors.Is(e, sql.ErrNoRows) {
			return 0, media.ErrNotFound
		}
		if e != nil {
			return 0, e
		}
		p, e := r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission)
		if e != nil {
			return 0, e
		}
		if owner != actor && !p.Account.Superuser {
			return 0, media.ErrForbidden
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT f.id::text FROM owned_media_files f LEFT JOIN owned_media_details d ON d.file_id=f.id WHERE f.state='deleting' AND (($2='' AND f.account_id=$1) OR d.pool_id=NULLIF($2,'')::uuid) ORDER BY f.id LIMIT 100`, actor, pool)
	if e != nil {
		return 0, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	n := 0
	for _, id := range ids {
		done, e := r.cleanupOne(ctx, id, false)
		if e != nil {
			return n, e
		}
		if done {
			n++
		}
	}
	return n, nil
}
