// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
	"unicode"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

//go:embed schema.sql
var Schema string

type Config struct {
	MaxFileBytes int64
	PendingTTL   time.Duration
}
type Repository struct {
	db             *sql.DB
	store          media.ObjectStore
	guard          *authoritypg.TransactionGuard
	cfg            Config
	completion     *completionQueue
	readVisibility ReadVisibility
}

func New(db *sql.DB, store media.ObjectStore, guard *authoritypg.TransactionGuard, cfg Config) (*Repository, error) {
	if db == nil || store == nil || guard == nil || cfg.MaxFileBytes <= 0 || cfg.MaxFileBytes > 1<<40 || cfg.PendingTTL < time.Minute || cfg.PendingTTL > 24*time.Hour {
		return nil, media.ErrInvalid
	}
	return &Repository{db: db, store: store, guard: guard, cfg: cfg, completion: newCompletionQueue()}, nil
}
func (r *Repository) Ready(ctx context.Context) error {
	if err := r.store.Ready(ctx); err != nil {
		return err
	}
	if policy, ok := r.readVisibility.(interface{ Ready(context.Context) error }); ok {
		if err := policy.Ready(ctx); err != nil {
			return err
		}
	}
	_, err := r.db.ExecContext(ctx, `SELECT id,charge,expires_at,usage FROM owned_media_files LIMIT 0; SELECT account_id,limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas LIMIT 0`)
	if err == nil {
		if _, ok := r.store.(media.ChunkObjectStore); ok {
			return r.ResumableReady(ctx)
		}
	}
	return err
}
func uuid() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func validUpload(u media.Upload, max int64) error {
	if e := media.ValidateUsage(u.Usage); e != nil {
		return e
	}
	if u.Size < 0 || u.Size > max {
		return media.ErrTooLarge
	}
	if u.Name == "" || len(u.Name) > 255 || strings.ContainsAny(u.Name, "/\\") || strings.IndexFunc(u.Name, unicode.IsControl) >= 0 || u.Name == "." || u.Name == ".." {
		return media.ErrInvalid
	}
	if len(u.IdempotencyKey) < 1 || len(u.IdempotencyKey) > 128 {
		return media.ErrInvalid
	}
	for _, c := range u.IdempotencyKey {
		if c < 33 || c > 126 {
			return media.ErrInvalid
		}
	}
	if len(u.SHA256) != 64 || u.SHA256 != strings.ToLower(u.SHA256) {
		return media.ErrInvalid
	}
	if _, err := hex.DecodeString(u.SHA256); err != nil {
		return media.ErrInvalid
	}
	if len(u.MIMEType) > 255 {
		return media.ErrInvalid
	}
	typ, params, err := mime.ParseMediaType(u.MIMEType)
	if err != nil || typ != u.MIMEType || len(params) != 0 {
		return media.ErrInvalid
	}
	return nil
}
func (r *Repository) begin(ctx context.Context, actor, permission string) (*sql.Tx, error) {
	if !authority.ValidID(actor) {
		return nil, authority.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	_, err = r.guard.Authorize(ctx, r.db, tx, actor, permission)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

const fields = `id::text,account_id::text,name,mime_type,size,sha256,is_public,state,created_at,updated_at,usage`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (f media.File, err error) {
	err = row.Scan(&f.ID, &f.AccountID, &f.Name, &f.MIMEType, &f.Size, &f.SHA256, &f.Public, &f.State, &f.CreatedAt, &f.UpdatedAt, &f.Usage)
	if errors.Is(err, sql.ErrNoRows) {
		err = media.ErrNotFound
	}
	return
}
func same(f media.File, u media.Upload) bool {
	return f.Name == u.Name && f.MIMEType == u.MIMEType && f.Size == u.Size && f.SHA256 == u.SHA256 && f.Public == u.Public && f.Usage == u.Usage
}

// Reserve before any filesystem work. Every operation takes canonical account,
// then quota, then file locks; this order also serializes policy revocation.
func (r *Repository) reserve(ctx context.Context, actor string, u media.Upload) (media.File, bool, error) {
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return media.File{}, false, err
	}
	defer tx.Rollback()
	f, replayed, err := r.reserveTx(ctx, tx, actor, u)
	if err != nil {
		return f, replayed, err
	}
	return f, replayed, tx.Commit()
}

// reserveTx shares the quota/file reservation with multipart and resumable uploads.
// The caller has already acquired the canonical account lock and owns the commit.
func (r *Repository) reserveTx(ctx context.Context, tx *sql.Tx, actor string, u media.Upload) (media.File, bool, error) {
	f, err := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE account_id=$1 AND idempotency_key=$2`, actor, u.IdempotencyKey))
	if err == nil {
		if !same(f, u) {
			return f, false, media.ErrConflict
		}
		if f.State == "ready" {
			body, e := r.openFile(ctx, f)
			if e != nil {
				return f, false, e
			}
			body.Close()
			return f, true, nil
		}
		if f.State == "deleted" || f.State == "deleting" {
			return f, false, media.ErrGone
		}
		return f, false, media.ErrPending
	}
	if !errors.Is(err, media.ErrNotFound) {
		return f, false, err
	}
	var q media.Quota
	err = tx.QueryRowContext(ctx, `SELECT limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor).Scan(&q.LimitBytes, &q.UsedBytes, &q.ReservedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, media.ErrQuotaMissing
	}
	if err != nil {
		return f, false, err
	}
	if u.Size > q.LimitBytes-q.UsedBytes-q.ReservedBytes {
		return f, false, media.ErrQuota
	}
	id, err := uuid()
	if err != nil {
		return f, false, err
	}
	f, err = scan(tx.QueryRowContext(ctx, `INSERT INTO owned_media_files(id,account_id,idempotency_key,name,mime_type,size,sha256,is_public,state,charge,expires_at,created_at,updated_at,usage) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending','reserved',now()+($9::bigint*interval '1 millisecond'),now(),now(),$10) RETURNING `+fields, id, actor, u.IdempotencyKey, u.Name, u.MIMEType, u.Size, u.SHA256, u.Public, r.cfg.PendingTTL.Milliseconds(), u.Usage))
	if err != nil {
		return f, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes+$2 WHERE account_id=$1`, actor, u.Size); err != nil {
		return f, false, err
	}
	return f, false, nil
}
func (r *Repository) Upload(ctx context.Context, actor string, u media.Upload, reader io.Reader) (media.File, bool, error) {
	return r.upload(ctx, actor, u, reader, false)
}
func (r *Repository) UploadDirect(ctx context.Context, actor string, u media.Upload, reader io.Reader) (media.File, error) {
	if _, ok := r.store.(media.MeasuredObjectStore); !ok {
		return media.File{}, media.ErrUnsupported
	}
	id, err := uuid()
	if err != nil {
		return media.File{}, err
	}
	u.IdempotencyKey = "direct:" + id
	u.SHA256 = strings.Repeat("0", 64)
	f, _, err := r.upload(ctx, actor, u, reader, true)
	return f, err
}
func (r *Repository) upload(ctx context.Context, actor string, u media.Upload, reader io.Reader, measured bool) (media.File, bool, error) {
	if err := validUpload(u, r.cfg.MaxFileBytes); err != nil {
		return media.File{}, false, err
	}
	if reader == nil {
		return media.File{}, false, media.ErrInvalid
	}
	f, replayed, err := r.reserve(ctx, actor, u)
	if err != nil || replayed {
		return f, replayed, err
	}
	f, err = r.finish(ctx, actor, f, reader, measured)
	if err != nil {
		// Only a still-pending row may be canceled after failure. A successful but
		// uncertain final commit is ready and survives this conditional cleanup.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, e := r.db.ExecContext(cleanupCtx, `UPDATE owned_media_files SET state='deleting',updated_at=now() WHERE id=$1 AND state='pending'`, f.ID); e == nil {
			_, _ = r.cleanupOne(cleanupCtx, f.ID, false)
		}
		return media.File{}, false, err
	}
	return f, false, nil
}
func (r *Repository) finish(ctx context.Context, actor string, f media.File, reader io.Reader, measured bool) (media.File, error) {
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); err != nil {
		return f, err
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT state='pending' AND expires_at>clock_timestamp() FROM owned_media_files WHERE id=$1 AND account_id=$2 FOR UPDATE`, f.ID, actor).Scan(&live); err != nil {
		return f, err
	}
	if !live {
		return f, media.ErrGone
	}
	// The row lock excludes cleanup until the bounded stream and commit finish.
	reserved := f.Size
	if measured {
		f.Size, f.SHA256, err = r.store.(media.MeasuredObjectStore).PutMeasured(ctx, f.ID, reader, reserved)
	} else {
		err = r.store.Put(ctx, f.ID, reader, f.Size, f.SHA256)
	}
	if err != nil {
		return f, err
	}
	if measured {
		if source, ok := reader.(media.UploadUsageSource); ok {
			f.Usage, err = source.UploadUsage()
			if err != nil {
				return f, err
			}
			if err = media.ValidateUsage(f.Usage); err != nil {
				return f, err
			}
		}
	}
	// Wall-clock credential expiry can change while streaming even though the
	// canonical account lock excludes revocation and policy changes.
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return f, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$3 WHERE account_id=$1`, actor, reserved, f.Size); err != nil {
		return f, err
	}
	updated, err := scan(tx.QueryRowContext(ctx, `UPDATE owned_media_files SET state='ready',charge='used',size=$2,sha256=$3,usage=$4,updated_at=clock_timestamp() WHERE id=$1 RETURNING `+fields, f.ID, f.Size, f.SHA256, f.Usage))
	if err != nil {
		return f, err
	}
	return updated, tx.Commit()
}
func (r *Repository) read(ctx context.Context, actor, id string, open bool) (media.File, io.ReadSeekCloser, error) {
	if !authority.ValidID(id) {
		return media.File{}, nil, media.ErrNotFound
	}
	var tx *sql.Tx
	var err error
	if actor != "" {
		tx, err = r.begin(ctx, actor, media.ReadPermission)
	} else {
		tx, err = r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	}
	if err != nil {
		return media.File{}, nil, err
	}
	defer tx.Rollback()
	f, err := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND state='ready' FOR SHARE`, id))
	if err != nil {
		return f, nil, err
	}
	// Recheck owner state after the file lock, so a lock waiter observes a
	// disable that committed while it waited. The optional policy never changes
	// the file's public flag or its canonical ownership.
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=statement_timestamp())AND(p.expires_at IS NULL OR p.expires_at>statement_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1`, f.AccountID).Scan(&active); err != nil {
		return f, nil, err
	}
	if !active {
		return media.File{}, nil, media.ErrNotFound
	}
	if !f.Public && f.AccountID != actor {
		allowed := false
		if r.readVisibility != nil {
			allowed, err = r.readVisibility.CanReadInTransaction(ctx, tx, actor, id)
		}
		if err != nil {
			return f, nil, err
		}
		if !allowed {
			return media.File{}, nil, media.ErrNotFound
		}
	}
	var body io.ReadSeekCloser
	if open {
		body, err = r.openFile(ctx, f)
		if err != nil {
			return f, nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		if body != nil {
			body.Close()
		}
		return f, nil, err
	}
	return f, body, nil
}
func (r *Repository) openFile(ctx context.Context, f media.File) (io.ReadSeekCloser, error) {
	body, err := r.store.Open(ctx, f.ID)
	if err != nil {
		return nil, media.ErrUnavailable
	}
	size, err := body.Seek(0, io.SeekEnd)
	if err != nil || size != f.Size {
		body.Close()
		return nil, media.ErrUnavailable
	}
	if _, err = body.Seek(0, io.SeekStart); err != nil {
		body.Close()
		return nil, media.ErrUnavailable
	}
	return body, nil
}
func (r *Repository) Info(ctx context.Context, actor, id string) (media.File, error) {
	f, _, err := r.read(ctx, actor, id, false)
	return f, err
}
func (r *Repository) Open(ctx context.Context, actor, id string) (media.File, io.ReadSeekCloser, error) {
	return r.read(ctx, actor, id, true)
}
func (r *Repository) Quota(ctx context.Context, actor string) (q media.Quota, err error) {
	tx, err := r.begin(ctx, actor, media.ReadPermission)
	if err != nil {
		return q, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas WHERE account_id=$1`, actor).Scan(&q.LimitBytes, &q.UsedBytes, &q.ReservedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return q, media.ErrQuotaMissing
	}
	if err != nil {
		return q, err
	}
	return q, tx.Commit()
}
func (r *Repository) Delete(ctx context.Context, actor, id string) (media.File, error) {
	if !authority.ValidID(id) {
		return media.File{}, media.ErrNotFound
	}
	tx, err := r.begin(ctx, actor, media.DeletePermission)
	if err != nil {
		return media.File{}, err
	}
	defer tx.Rollback()
	f, err := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, actor))
	if err != nil {
		return f, err
	}
	if f.State == "deleted" {
		return f, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=now() WHERE id=$1`, id); err != nil {
		return f, err
	}
	if err = tx.Commit(); err != nil {
		return f, err
	}
	if _, err = r.cleanupOne(ctx, id, false); err != nil {
		return f, err
	}
	f.State = "deleted"
	return f, nil
}

// Cleanup retries durable deletions and abandoned pending uploads. No scheduler
// or network process is started here. The host invokes this bounded worker.
func (r *Repository) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, media.ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id::text FROM owned_media_files WHERE state='deleting' OR (state='pending' AND expires_at<=now()) ORDER BY expires_at,id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		cleaned, e := r.cleanupOne(ctx, id, true)
		if e != nil {
			return done, e
		}
		if cleaned {
			done++
		}
	}
	return done, nil
}
func (r *Repository) cleanupOne(ctx context.Context, id string, expiredOnly bool) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var actor string
	err = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_files WHERE id=$1`, id).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Same lock order as request writers, including the canonical account row.
	if _, err = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); err != nil {
		return false, err
	}
	var state, charge string
	var size int64
	var expired bool
	if err = tx.QueryRowContext(ctx, `SELECT state,charge,size,expires_at<=now() FROM owned_media_files WHERE id=$1 FOR UPDATE`, id).Scan(&state, &charge, &size, &expired); err != nil {
		return false, err
	}
	if state == "deleted" || state == "ready" || state == "pending" && (!expiredOnly || !expired) {
		return false, tx.Commit()
	}
	// Delete bytes first. A crash/uncertain commit leaves a charge and retryable
	// row, never released quota with live bytes or an exposed missing ready file.
	if err = r.store.Remove(ctx, id); err != nil {
		return false, err
	}
	column := "reserved_bytes"
	if charge == "used" {
		column = "used_bytes"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET `+column+`=`+column+`-$2 WHERE account_id=$1`, actor, size); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleted',charge='none',updated_at=now() WHERE id=$1`, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
