// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
)

//go:embed resumable_schema.sql
var ResumableSchema string

type completionQueue struct {
	mu     sync.Mutex
	active map[string]bool
	slots  chan struct{}
}

func newCompletionQueue() *completionQueue {
	return &completionQueue{active: map[string]bool{}, slots: make(chan struct{}, 4)}
}

func (r *Repository) ResumableReady(ctx context.Context) error {
	if _, ok := r.store.(media.ChunkObjectStore); !ok {
		return media.ErrUnsupported
	}
	_, err := r.db.ExecContext(ctx, `SELECT id,request_sha256,expected_md5,chunk_size,chunks_count,status,failure_code FROM owned_media_upload_tasks LIMIT 0; SELECT task_id,chunk_index,size,sha256 FROM owned_media_upload_chunks LIMIT 0`)
	return err
}

func normalizeTask(request media.CreateUploadRequest, max int64) (media.CreateUploadRequest, string, error) {
	if request.PoolID != "" || request.BundleID != "" || request.ParentID != "" || request.EncryptionScheme != "" || request.EncryptionHeader != "" || request.EncryptionSignature != "" || request.ExpiredAt != nil || request.Usage != "" || request.ApplicationType != "" {
		return request, "", media.ErrUnsupported
	}
	request.Hash = strings.ToLower(request.Hash)
	if len(request.Hash) != 32 {
		return request, "", media.ErrInvalid
	}
	if _, err := hex.DecodeString(request.Hash); err != nil {
		return request, "", media.ErrInvalid
	}
	if request.FileSize < 1 || request.FileSize > max {
		return request, "", media.ErrTooLarge
	}
	if request.ChunkSize == 0 {
		request.ChunkSize = media.DefaultChunkBytes
	}
	if request.ChunkSize < 1 || request.ChunkSize > media.MaxChunkBytes || (request.FileSize-1)/request.ChunkSize+1 > media.MaxUploadChunks {
		return request, "", media.ErrInvalid
	}
	u := media.Upload{Name: request.FileName, MIMEType: request.ContentType, Size: request.FileSize, SHA256: strings.Repeat("0", 64), IdempotencyKey: "validation"}
	if err := validUpload(u, max); err != nil {
		return request, "", err
	}
	b, err := json.Marshal(request)
	if err != nil {
		return request, "", err
	}
	digest := sha256.Sum256(b)
	return request, hex.EncodeToString(digest[:]), nil
}

func taskKey(key string) (string, error) {
	if key == "" {
		id, err := uuid()
		return "resumable:auto:" + id, err
	}
	if len(key) > 128 {
		return "", media.ErrInvalid
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return "", media.ErrInvalid
		}
	}
	digest := sha256.Sum256([]byte(key))
	return "resumable:key:" + hex.EncodeToString(digest[:]), nil
}

func (r *Repository) CreateResumable(ctx context.Context, actor string, request media.CreateUploadRequest, key string) (media.UploadTask, error) {
	if _, ok := r.store.(media.ChunkObjectStore); !ok {
		return media.UploadTask{}, media.ErrUnsupported
	}
	request, fingerprint, err := normalizeTask(request, r.cfg.MaxFileBytes)
	if err != nil {
		return media.UploadTask{}, err
	}
	key, err = taskKey(key)
	if err != nil {
		return media.UploadTask{}, err
	}
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return media.UploadTask{}, err
	}
	defer tx.Rollback()
	var id, prior string
	err = tx.QueryRowContext(ctx, `SELECT t.id::text,t.request_sha256 FROM owned_media_upload_tasks t JOIN owned_media_files f ON f.id=t.id WHERE f.account_id=$1 AND f.idempotency_key=$2`, actor, key).Scan(&id, &prior)
	if err == nil {
		if prior != fingerprint {
			return media.UploadTask{}, media.ErrConflict
		}
		task, err := r.taskTx(ctx, tx, actor, id)
		if err != nil {
			return task, err
		}
		if task.Status == "Cancelled" || task.Status == "Expired" || task.Status == "Failed" {
			return task, media.ErrGone
		}
		return task, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return media.UploadTask{}, err
	}
	// A separate task-count bound prevents tiny-file reservations from creating
	// unbounded live upload metadata and staging directories within a byte quota.
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_upload_tasks t JOIN owned_media_files f ON f.id=t.id WHERE f.account_id=$1 AND f.state='pending'`, actor).Scan(&active); err != nil {
		return media.UploadTask{}, err
	}
	if active >= 100 {
		return media.UploadTask{}, media.ErrPending
	}
	u := media.Upload{Name: request.FileName, MIMEType: request.ContentType, Size: request.FileSize, SHA256: fingerprint, IdempotencyKey: key}
	f, replayed, err := r.reserveTx(ctx, tx, actor, u)
	if err != nil {
		return media.UploadTask{}, err
	}
	if replayed {
		return media.UploadTask{}, media.ErrConflict
	}
	count := int((request.FileSize-1)/request.ChunkSize + 1)
	if _, err = tx.ExecContext(ctx, `INSERT INTO owned_media_upload_tasks(id,request_sha256,expected_md5,chunk_size,chunks_count,status) VALUES($1,$2,$3,$4,$5,'InProgress')`, f.ID, fingerprint, request.Hash, request.ChunkSize, count); err != nil {
		return media.UploadTask{}, err
	}
	task, err := r.taskTx(ctx, tx, actor, f.ID)
	if err != nil {
		return task, err
	}
	return task, tx.Commit()
}

type taskRecord struct {
	file        media.File
	task        media.UploadTask
	expectedMD5 string
	expired     bool
}

func (r *Repository) lockTask(ctx context.Context, tx *sql.Tx, actor, id string) (rec taskRecord, err error) {
	if !authority.ValidID(id) {
		return rec, media.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); err != nil {
		return rec, err
	}
	rec.file, err = scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, actor))
	if err != nil {
		return rec, err
	}
	err = tx.QueryRowContext(ctx, `SELECT expected_md5,chunk_size,chunks_count,status,failure_code FROM owned_media_upload_tasks WHERE id=$1 FOR UPDATE`, id).Scan(&rec.expectedMD5, &rec.task.ChunkSize, &rec.task.ChunksCount, &rec.task.Status, &rec.task.FailureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, media.ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	err = tx.QueryRowContext(ctx, `SELECT expires_at<=clock_timestamp() FROM owned_media_files WHERE id=$1`, id).Scan(&rec.expired)
	return rec, err
}

func (r *Repository) UploadChunk(ctx context.Context, actor, id string, index int, reader io.Reader) (bool, error) {
	store, ok := r.store.(media.ChunkObjectStore)
	if !ok {
		return false, media.ErrUnsupported
	}
	if reader == nil || index < 0 || index >= media.MaxUploadChunks {
		return false, media.ErrInvalid
	}
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rec, err := r.lockTask(ctx, tx, actor, id)
	if err != nil {
		return false, err
	}
	if rec.file.State != "pending" && rec.file.State != "ready" || rec.file.State == "pending" && rec.expired || rec.task.Status == "Failed" {
		return false, media.ErrGone
	}
	if index >= rec.task.ChunksCount {
		return false, media.ErrInvalid
	}
	size := min(rec.task.ChunkSize, rec.file.Size-int64(index)*rec.task.ChunkSize)
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT sha256 FROM owned_media_upload_chunks WHERE task_id=$1 AND chunk_index=$2`, id, index).Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if rec.task.Status != "InProgress" {
		if prior == "" {
			return false, media.ErrPending
		}
		digest := sha256.New()
		n, e := io.CopyBuffer(digest, io.LimitReader(&resumableContextReader{ctx, reader}, size+1), make([]byte, 64<<10))
		if e != nil {
			return false, e
		}
		if n > size {
			return false, media.ErrTooLarge
		}
		if n != size {
			return false, media.ErrDigest
		}
		if hex.EncodeToString(digest.Sum(nil)) != prior {
			return false, media.ErrConflict
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); e != nil {
			return false, e
		}
		return true, tx.Commit()
	}
	sha, err := store.PutChunk(ctx, id, index, reader, size, prior)
	if err != nil {
		return false, err
	}
	if prior != "" && prior != sha {
		return false, media.ErrDigest
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return false, err
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT expires_at>clock_timestamp() FROM owned_media_files WHERE id=$1`, id).Scan(&live); err != nil {
		return false, err
	}
	if !live {
		return false, media.ErrGone
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owned_media_upload_chunks(task_id,chunk_index,size,sha256) VALUES($1,$2,$3,$4) ON CONFLICT(task_id,chunk_index) DO NOTHING`, id, index, size, sha); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET expires_at=clock_timestamp()+($2::bigint*interval '1 millisecond'),updated_at=clock_timestamp() WHERE id=$1`, id, r.cfg.PendingTTL.Milliseconds()); err != nil {
		return false, err
	}
	return prior != "", tx.Commit()
}

func (r *Repository) CompleteResumable(ctx context.Context, actor, id string) error {
	if _, ok := r.store.(media.ChunkObjectStore); !ok {
		return media.ErrUnsupported
	}
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rec, err := r.lockTask(ctx, tx, actor, id)
	if err != nil {
		return err
	}
	if rec.file.State == "ready" && rec.task.Status == "Completed" {
		return tx.Commit()
	}
	if rec.file.State != "pending" || rec.expired || rec.task.Status == "Failed" {
		return media.ErrGone
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_upload_chunks WHERE task_id=$1`, id).Scan(&count); err != nil {
		return err
	}
	if count != rec.task.ChunksCount {
		return media.ErrPending
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_upload_tasks SET status='Processing',failure_code='' WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET expires_at=clock_timestamp()+($2::bigint*interval '1 millisecond'),updated_at=clock_timestamp() WHERE id=$1`, id, r.cfg.PendingTTL.Milliseconds()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.scheduleCompletion(ctx, actor, id)
	return nil
}

// The queue is process-local and bounded. Persisted Processing tasks restart
// only from a fresh authorized complete/progress/resume request. No bearer or
// opaque proof is serialized, and request cancellation does not kill accepted
// work. Each worker still revalidates the retained proof under canonical locks.
func (r *Repository) scheduleCompletion(ctx context.Context, actor, id string) {
	q := r.completion
	q.mu.Lock()
	if q.active[id] {
		q.mu.Unlock()
		return
	}
	select {
	case q.slots <- struct{}{}:
	default:
		q.mu.Unlock()
		return
	}
	q.active[id] = true
	q.mu.Unlock()
	go func() {
		defer func() { q.mu.Lock(); delete(q.active, id); q.mu.Unlock(); <-q.slots }()
		worker, cancel := context.WithTimeout(context.WithoutCancel(ctx), min(r.cfg.PendingTTL, 10*time.Minute))
		defer cancel()
		err := r.processResumable(worker, actor, id)
		if errors.Is(err, media.ErrDigest) || errors.Is(err, media.ErrTooLarge) || errors.Is(err, authority.ErrForbidden) || errors.Is(err, authority.ErrInvalid) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.failResumable(cleanup, id, err)
		}
	}()
}

func (r *Repository) processResumable(ctx context.Context, actor, id string) error {
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rec, err := r.lockTask(ctx, tx, actor, id)
	if err != nil {
		return err
	}
	if rec.file.State == "ready" && rec.task.Status == "Completed" {
		return tx.Commit()
	}
	if rec.file.State != "pending" || rec.expired || rec.task.Status != "Processing" {
		return media.ErrGone
	}
	rows, err := tx.QueryContext(ctx, `SELECT chunk_index,size,sha256 FROM owned_media_upload_chunks WHERE task_id=$1 ORDER BY chunk_index`, id)
	if err != nil {
		return err
	}
	chunks := []media.StoredChunk{}
	for rows.Next() {
		var c media.StoredChunk
		if err = rows.Scan(&c.Index, &c.Size, &c.SHA256); err != nil {
			rows.Close()
			return err
		}
		chunks = append(chunks, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(chunks) != rec.task.ChunksCount {
		return media.ErrPending
	}
	sha, err := r.store.(media.ChunkObjectStore).AssembleChunks(ctx, id, chunks, rec.file.Size, rec.expectedMD5)
	if err != nil {
		return err
	}
	if _, err = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); err != nil {
		return err
	}
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT expires_at>clock_timestamp() FROM owned_media_files WHERE id=$1`, id).Scan(&live); err != nil {
		return err
	}
	if !live {
		return media.ErrGone
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$2 WHERE account_id=$1`, actor, rec.file.Size); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='ready',charge='used',sha256=$2,updated_at=clock_timestamp() WHERE id=$1`, id, sha); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_upload_tasks SET status='Completed',failure_code='' WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// This conditional failure path can hide/clean only a still-pending owned task;
// an uncertain successful commit is never deleted. It stores no error details.
func (r *Repository) failResumable(ctx context.Context, id string, cause error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actor string
	if err = tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_files WHERE id=$1`, id).Scan(&actor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT id FROM owned_authority_accounts WHERE id=$1 FOR UPDATE`, actor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT account_id FROM owned_media_quotas WHERE account_id=$1 FOR UPDATE`, actor); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1 AND state='pending'`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return tx.Commit()
	}
	code := "MEDIA_CONTENT_MISMATCH"
	if errors.Is(cause, authority.ErrForbidden) || errors.Is(cause, authority.ErrInvalid) {
		code = "MEDIA_FORBIDDEN"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_upload_tasks SET status='Failed',failure_code=$2 WHERE id=$1`, id, code); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = r.cleanupOne(ctx, id, false)
	return err
}

const taskStatusSQL = `CASE WHEN t.status='Failed' THEN 'Failed' WHEN f.state IN ('deleting','deleted') THEN CASE WHEN f.expires_at<=clock_timestamp() THEN 'Expired' ELSE 'Cancelled' END WHEN f.state='pending' AND f.expires_at<=clock_timestamp() THEN 'Expired' ELSE t.status END`

func (r *Repository) taskTx(ctx context.Context, tx *sql.Tx, actor, id string) (task media.UploadTask, err error) {
	err = tx.QueryRowContext(ctx, `SELECT f.id::text,f.name,f.size,f.mime_type,t.chunk_size,t.chunks_count,`+taskStatusSQL+`,f.updated_at,t.failure_code FROM owned_media_upload_tasks t JOIN owned_media_files f ON f.id=t.id WHERE f.id=$1 AND f.account_id=$2`, id, actor).Scan(&task.TaskID, &task.FileName, &task.FileSize, &task.ContentType, &task.ChunkSize, &task.ChunksCount, &task.Status, &task.LastActivity, &task.FailureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return task, media.ErrNotFound
	}
	if err != nil {
		return task, err
	}
	task.UploadedChunks = []int{}
	rows, err := tx.QueryContext(ctx, `SELECT chunk_index FROM owned_media_upload_chunks WHERE task_id=$1 ORDER BY chunk_index`, id)
	if err != nil {
		return task, err
	}
	for rows.Next() {
		var index int
		if err = rows.Scan(&index); err != nil {
			rows.Close()
			return task, err
		}
		task.UploadedChunks = append(task.UploadedChunks, index)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return task, err
	}
	task.ChunksUploaded = len(task.UploadedChunks)
	task.Progress = 50 * float64(task.ChunksUploaded) / float64(task.ChunksCount)
	if task.Status == "Completed" {
		task.Progress = 100
		task.FileID = &task.TaskID
	}
	return task, nil
}

func (r *Repository) ResumableProgress(ctx context.Context, actor, id string) (media.UploadTask, error) {
	if !authority.ValidID(id) {
		return media.UploadTask{}, media.ErrNotFound
	}
	tx, err := r.begin(ctx, actor, media.ReadPermission)
	if err != nil {
		return media.UploadTask{}, err
	}
	defer tx.Rollback()
	task, err := r.taskTx(ctx, tx, actor, id)
	if err != nil {
		return task, err
	}
	canResume := false
	if task.Status == "Processing" {
		// Read-only clients can inspect progress without authorizing or canceling
		// a worker. Resuming processing requires the original create permission.
		_, authErr := r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission)
		canResume = authErr == nil
	}
	if err = tx.Commit(); err != nil {
		return task, err
	}
	if canResume {
		r.scheduleCompletion(ctx, actor, id)
	}
	return task, nil
}

func (r *Repository) ListResumable(ctx context.Context, actor string, q media.UploadTaskQuery) ([]media.UploadTask, error) {
	if q.Offset < 0 || q.Offset > 100000 || q.Limit < 1 || q.Limit > 100 {
		return nil, media.ErrInvalid
	}
	columns := map[string]string{"lastActivity": "f.updated_at", "fileName": "f.name", "fileSize": "f.size", "createdAt": "f.created_at"}
	column, ok := columns[q.SortBy]
	if !ok {
		return nil, media.ErrInvalid
	}
	if q.Status != "" && q.Status != "InProgress" && q.Status != "Processing" && q.Status != "Completed" && q.Status != "Failed" && q.Status != "Cancelled" && q.Status != "Expired" {
		return nil, media.ErrInvalid
	}
	direction := " ASC"
	if q.Descending {
		direction = " DESC"
	}
	tx, err := r.begin(ctx, actor, media.ReadPermission)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT f.id::text FROM owned_media_upload_tasks t JOIN owned_media_files f ON f.id=t.id WHERE f.account_id=$1 AND ($2='' OR (`+taskStatusSQL+`)=$2) ORDER BY `+column+direction+`,f.id`+direction+` LIMIT $3 OFFSET $4`, actor, q.Status, q.Limit, q.Offset)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	tasks := make([]media.UploadTask, 0, len(ids))
	for _, id := range ids {
		task, err := r.taskTx(ctx, tx, actor, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, tx.Commit()
}

func (r *Repository) CancelResumable(ctx context.Context, actor, id string) error {
	tx, err := r.begin(ctx, actor, media.CreatePermission)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rec, err := r.lockTask(ctx, tx, actor, id)
	if err != nil {
		return err
	}
	if rec.file.State == "ready" {
		return media.ErrConflict
	}
	if rec.file.State == "deleted" {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = r.cleanupOne(ctx, id, false)
	return err
}

// A replay still consumes bounded request bytes and checks cancellation.
type resumableContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *resumableContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
