// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"sort"
	"strings"
	"time"
)

func (r *Repository) WithPools(stores map[string]media.ObjectStore) error {
	r.poolStores = map[string]media.ObjectStore{}
	for id, s := range stores {
		if !authority.ValidID(id) || s == nil {
			return media.ErrInvalid
		}
		value := reflect.ValueOf(s)
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				return media.ErrInvalid
			}
		}
		r.poolStores[id] = s
	}
	return nil
}
func (r *Repository) WithRemoteFetcher(f media.RemoteFetcher) *Repository {
	r.remoteFetcher = f
	return r
}
func (r *Repository) WithRenderer(renderer media.RasterRenderer) *Repository {
	r.renderer = renderer
	return r
}
func optionsBytes(v media.Options) ([]byte, string, error) {
	b, e := json.Marshal(v)
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), e
}
func (r *Repository) validateOptions(ctx context.Context, tx *sql.Tx, actor string, v media.Options, mime string, size int64) error {
	var poolOwner string
	var now time.Time
	if e := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return e
	}
	if e := media.ValidateOptions(v, now); e != nil {
		return e
	}
	if v.EncryptionScheme != "" && mime != "application/octet-stream" {
		return media.ErrInvalid
	}
	if v.ParentID != "" {
		var owner string
		e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_folders WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, v.ParentID).Scan(&owner)
		if errors.Is(e, sql.ErrNoRows) {
			return media.ErrNotFound
		}
		if e != nil {
			return e
		}
		if owner != actor {
			return media.ErrForbidden
		}
	}
	if v.PoolID != "" {
		if r.poolStores[v.PoolID] == nil {
			return media.ErrUnavailable
		}
		var owner string
		var public, encrypted bool
		var max int64
		e := tx.QueryRowContext(ctx, `SELECT account_id::text,is_public,allow_encryption,max_file_bytes FROM owned_media_pools WHERE id=$1 FOR SHARE`, v.PoolID).Scan(&owner, &public, &encrypted, &max)
		if errors.Is(e, sql.ErrNoRows) {
			return media.ErrNotFound
		}
		if e != nil {
			return e
		}
		poolOwner = owner
		if e = artworkOwnerActive(ctx, tx, owner); e != nil {
			return e
		}
		if owner != actor && !public {
			return media.ErrForbidden
		}
		if size > max {
			return media.ErrTooLarge
		}
		if v.EncryptionScheme != "" && !encrypted {
			return media.ErrForbidden
		}
	}
	if v.BundleID != "" {
		var owner string
		e := tx.QueryRowContext(ctx, `SELECT account_id::text FROM owned_media_bundles WHERE id=$1 AND deleted_at IS NULL AND (expired_at IS NULL OR expired_at>clock_timestamp()) FOR SHARE`, v.BundleID).Scan(&owner)
		if errors.Is(e, sql.ErrNoRows) {
			return media.ErrNotFound
		}
		if e != nil {
			return e
		}
		if owner != actor {
			return media.ErrForbidden
		}
	}
	if poolOwner != "" {
		if e := artworkOwnerActive(ctx, tx, poolOwner); e != nil {
			return e
		}
	}
	return validateFutureExpiry(ctx, tx, v.ExpiredAt)
}
func (r *Repository) insertOptions(ctx context.Context, tx *sql.Tx, id string, v media.Options) error {
	b, h, e := optionsBytes(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_media_details(file_id,options,options_sha256,parent_id,pool_id,object_key,expired_at,bundle_id) VALUES($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$1,$6,NULLIF($7,'')::uuid)`, id, b, h, v.ParentID, v.PoolID, v.ExpiredAt, v.BundleID)
	return e
}

// Upload workers must validate the persisted reservation, rather than trusting
// request options or a streaming adapter. The final check runs after object I/O,
// deduplication and database writes; scheduled punishments and expiry continue
// to advance even while the relevant policy rows are locked.
func (r *Repository) validatePersistedUpload(ctx context.Context, tx *sql.Tx, actor string, f media.File) error {
	var raw []byte
	var options media.Options
	e := tx.QueryRowContext(ctx, `SELECT options FROM owned_media_details WHERE file_id=$1`, f.ID).Scan(&raw)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if e == nil {
		if e = json.Unmarshal(raw, &options); e != nil {
			return media.ErrUnavailable
		}
		if e = r.sameOptions(ctx, tx, f.ID, options); e != nil {
			return e
		}
	}
	if e = r.validateOptions(ctx, tx, actor, options, f.MIMEType, f.Size); e != nil {
		return e
	}
	var live, ownerActive bool
	e = tx.QueryRowContext(ctx, `SELECT
 f.expires_at>clock_timestamp()
 AND (d.expired_at IS NULL OR d.expired_at>clock_timestamp())
 AND (d.parent_id IS NULL OR (folder.account_id=f.account_id AND folder.deleted_at IS NULL))
 AND (d.bundle_id IS NULL OR (b.account_id=f.account_id AND b.deleted_at IS NULL AND (b.expired_at IS NULL OR b.expired_at>clock_timestamp()))),
 d.pool_id IS NULL OR (a.id IS NOT NULL AND NOT a.disabled AND NOT EXISTS(
 SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account'
 AND (p.starts_at IS NULL OR p.starts_at<=clock_timestamp()) AND (p.expires_at IS NULL OR p.expires_at>clock_timestamp())))
 FROM owned_media_files f LEFT JOIN owned_media_details d ON d.file_id=f.id
 LEFT JOIN owned_media_folders folder ON folder.id=d.parent_id
 LEFT JOIN owned_media_bundles b ON b.id=d.bundle_id
 LEFT JOIN owned_media_pools pool ON pool.id=d.pool_id
 LEFT JOIN owned_authority_accounts a ON a.id=pool.account_id
 WHERE f.id=$1 AND f.account_id=$2`, f.ID, actor).Scan(&live, &ownerActive)
	if e != nil {
		return e
	}
	if !ownerActive {
		return media.ErrNotFound
	}
	if !live {
		return media.ErrGone
	}
	return nil
}
func (r *Repository) sameOptions(ctx context.Context, tx *sql.Tx, id string, v media.Options) error {
	_, want, e := optionsBytes(v)
	if e != nil {
		return e
	}
	var got string
	e = tx.QueryRowContext(ctx, `SELECT options_sha256 FROM owned_media_details WHERE file_id=$1`, id).Scan(&got)
	if errors.Is(e, sql.ErrNoRows) {
		_, zero, _ := optionsBytes(media.Options{})
		if want == zero {
			return nil
		}
	}
	if e != nil {
		return e
	}
	if want != got {
		return media.ErrConflict
	}
	return nil
}
func (r *Repository) objectStore(ctx context.Context, id string, transactions ...*sql.Tx) (media.ObjectStore, string, error) {
	var pool *string
	key := id
	var q interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	} = r.db
	if len(transactions) > 0 {
		q = transactions[0]
	}
	e := q.QueryRowContext(ctx, `SELECT pool_id::text,object_key::text FROM owned_media_details WHERE file_id=$1`, id).Scan(&pool, &key)
	if errors.Is(e, sql.ErrNoRows) {
		return r.store, id, nil
	}
	if e != nil {
		return nil, "", e
	}
	if pool == nil {
		return r.store, key, nil
	}
	s := r.poolStores[*pool]
	if s == nil {
		return nil, "", media.ErrUnavailable
	}
	return s, key, nil
}
func (r *Repository) fileRecordTx(ctx context.Context, tx *sql.Tx, actor, id string) (media.FileRecord, error) {
	var out media.FileRecord
	f, e := scan(tx.QueryRowContext(ctx, `SELECT `+fields+` FROM owned_media_files WHERE id=$1 AND account_id=$2 AND state='ready'`, id, actor))
	if e != nil {
		return out, e
	}
	out.File = f
	if e = r.protectFileRead(ctx, tx, actor, id); e != nil {
		return out, e
	}
	out.Indexed = true
	out.Meta = map[string]json.RawMessage{}
	out.Sensitive = []string{}
	var opts, meta, marks []byte
	var parent *string
	e = tx.QueryRowContext(ctx, `SELECT options,parent_id::text,indexed,user_meta,sensitive_marks FROM owned_media_details WHERE file_id=$1 AND (expired_at IS NULL OR expired_at>clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM owned_media_bundles b WHERE b.id=owned_media_details.bundle_id AND (b.deleted_at IS NOT NULL OR b.expired_at<=clock_timestamp()))`, id).Scan(&opts, &parent, &out.Indexed, &meta, &marks)
	if errors.Is(e, sql.ErrNoRows) {
		return out, media.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(opts, &out.Options); e != nil {
		return out, e
	}
	out.ParentID = ""
	if parent != nil {
		out.ParentID = *parent
	}
	if e = json.Unmarshal(meta, &out.Meta); e != nil {
		return out, e
	}
	e = json.Unmarshal(marks, &out.Sensitive)
	if e != nil {
		return out, e
	}
	return out, r.protectFileRead(ctx, tx, actor, id)
}
func (r *Repository) folderTx(ctx context.Context, tx *sql.Tx, actor, id string) (media.FileRecord, error) {
	var out media.FileRecord
	var parent *string
	e := tx.QueryRowContext(ctx, `SELECT id::text,account_id::text,name,parent_id::text,created_at,updated_at FROM owned_media_folders WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL`, id, actor).Scan(&out.ID, &out.AccountID, &out.Name, &parent, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, media.ErrNotFound
	}
	out.IsFolder = true
	out.Indexed = true
	out.State = "ready"
	out.MIMEType = "inode/directory"
	if parent != nil {
		out.ParentID = *parent
	}
	return out, e
}
func (r *Repository) FileRecord(ctx context.Context, actor, id string) (media.FileRecord, error) {
	if !authority.ValidID(id) {
		return media.FileRecord{}, media.ErrNotFound
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return media.FileRecord{}, e
	}
	defer tx.Rollback()
	out, e := r.fileRecordTx(ctx, tx, actor, id)
	if errors.Is(e, media.ErrNotFound) {
		out, e = r.folderTx(ctx, tx, actor, id)
	}
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) ListFiles(ctx context.Context, actor string, q media.FileQuery) ([]media.FileRecord, int64, error) {
	if q.Take < 1 || q.Take > 100 || q.Offset < 0 || q.Offset > 100000 || len(q.Query) > 255 || q.ParentID != "" && !authority.ValidID(q.ParentID) || q.PoolID != "" && !authority.ValidID(q.PoolID) {
		return nil, 0, media.ErrInvalid
	}
	order := map[string]string{"date": "created_at", "name": "name", "size": "size"}[q.Order]
	if order == "" {
		return nil, 0, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback()
	if q.ParentID != "" {
		if _, e = r.folderTx(ctx, tx, actor, q.ParentID); e != nil {
			return nil, 0, e
		}
	}
	direction := "ASC"
	if q.Descending {
		direction = "DESC"
	}
	// Substring matching treats %, _ and backslashes literally. All hierarchy
	// results are owned by the caller; public-file visibility never grants folders.
	base := ` FROM (SELECT f.id,f.name,f.size,f.created_at,COALESCE(d.indexed,true) indexed,d.parent_id,d.pool_id,false is_folder FROM owned_media_files f LEFT JOIN owned_media_details d ON d.file_id=f.id WHERE f.account_id=$1 AND f.state='ready' AND (d.expired_at IS NULL OR d.expired_at>clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM owned_media_bundles b WHERE b.id=d.bundle_id AND(b.deleted_at IS NOT NULL OR b.expired_at<=clock_timestamp())) UNION ALL SELECT id,name,0::bigint,created_at,true,parent_id,NULL::uuid,true FROM owned_media_folders WHERE account_id=$1 AND deleted_at IS NULL) x WHERE strpos(lower(name),lower($2))>0 AND (CASE WHEN $3 THEN NOT indexed ELSE indexed AND ($6 OR parent_id IS NOT DISTINCT FROM NULLIF($4,'')::uuid) END) AND ($5='' OR pool_id=NULLIF($5,'')::uuid)`
	var total int64
	rows, e := tx.QueryContext(ctx, `SELECT id::text,is_folder`+base+` ORDER BY `+order+` `+direction+`,id`, actor, q.Query, q.Unindexed, q.ParentID, q.PoolID, q.All)
	if e != nil {
		return nil, 0, e
	}
	type item struct {
		id     string
		folder bool
	}
	ids := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.folder); e != nil {
			rows.Close()
			return nil, 0, e
		}
		ids = append(ids, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	out := []media.FileRecord{}
	for _, v := range ids {
		var f media.FileRecord
		if v.folder {
			f, e = r.folderTx(ctx, tx, actor, v.id)
		} else {
			f, e = r.fileRecordTx(ctx, tx, actor, v.id)
		}
		if errors.Is(e, media.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, 0, e
		}
		if total >= int64(q.Offset) && len(out) < q.Take {
			out = append(out, f)
		}
		total++
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return nil, 0, e
	}
	return out, total, tx.Commit()
}
func nameOK(v string) bool {
	return validUpload(media.Upload{Name: v, MIMEType: "application/octet-stream", SHA256: strings.Repeat("0", 64), IdempotencyKey: "name", Size: 0}, 1) == nil
}
func (r *Repository) CreateFolder(ctx context.Context, actor, name string, parent *string) (media.FileRecord, error) {
	if !nameOK(name) || parent != nil && !authority.ValidID(*parent) {
		return media.FileRecord{}, media.ErrInvalid
	}
	tx, e := r.begin(ctx, actor, media.CreatePermission)
	if e != nil {
		return media.FileRecord{}, e
	}
	defer tx.Rollback()
	if parent != nil {
		if _, e = r.folderTx(ctx, tx, actor, *parent); e != nil {
			return media.FileRecord{}, e
		}
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM owned_media_folders WHERE account_id=$1 AND deleted_at IS NULL`, actor).Scan(&count); e != nil {
		return media.FileRecord{}, e
	}
	if count >= 1000 {
		return media.FileRecord{}, media.ErrQuota
	}
	id, e := uuid()
	if e != nil {
		return media.FileRecord{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_media_folders(id,account_id,name,parent_id,created_at,updated_at) VALUES($1,$2,$3,$4,clock_timestamp(),clock_timestamp())`, id, actor, name, parent); e != nil {
		return media.FileRecord{}, e
	}
	out, e := r.folderTx(ctx, tx, actor, id)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) PatchFile(ctx context.Context, actor, id string, p media.FilePatch) (media.FileRecord, error) {
	if !authority.ValidID(id) || p.Name != nil && !nameOK(*p.Name) || p.ParentID != nil && !authority.ValidID(*p.ParentID) {
		return media.FileRecord{}, media.ErrInvalid
	}
	meta, e := json.Marshal(p.Meta)
	if e != nil || len(meta) > 16384 || len(p.Sensitive) > 16 {
		return media.FileRecord{}, media.ErrInvalid
	}
	for _, mark := range p.Sensitive {
		if mark == "" || len(mark) > 64 || strings.ContainsRune(mark, 0) {
			return media.FileRecord{}, media.ErrInvalid
		}
	}
	tx, e := r.begin(ctx, actor, media.UpdatePermission)
	if e != nil {
		return media.FileRecord{}, e
	}
	defer tx.Rollback()
	var isFolder bool
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT account_id::text,false FROM owned_media_files WHERE id=$1 AND account_id=$2 AND state='ready' FOR UPDATE`, id, actor).Scan(&owner, &isFolder)
	if errors.Is(e, sql.ErrNoRows) {
		e = tx.QueryRowContext(ctx, `SELECT account_id::text,true FROM owned_media_folders WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL FOR UPDATE`, id, actor).Scan(&owner, &isFolder)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return media.FileRecord{}, media.ErrNotFound
	}
	if e != nil {
		return media.FileRecord{}, e
	}
	if owner != actor {
		return media.FileRecord{}, media.ErrNotFound
	}
	if p.ParentPresent && p.ParentID != nil {
		if *p.ParentID == id {
			return media.FileRecord{}, media.ErrConflict
		}
		if _, e = r.folderTx(ctx, tx, actor, *p.ParentID); e != nil {
			return media.FileRecord{}, e
		}
		if isFolder {
			var cycle bool
			e = tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors AS(SELECT id,parent_id,1 depth FROM owned_media_folders WHERE id=$1 AND account_id=$3 AND deleted_at IS NULL UNION ALL SELECT p.id,p.parent_id,a.depth+1 FROM owned_media_folders p JOIN ancestors a ON p.id=a.parent_id WHERE a.depth<64 AND p.account_id=$3 AND p.deleted_at IS NULL)SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=$2 OR depth=64)`, *p.ParentID, id, actor).Scan(&cycle)
			if e != nil {
				return media.FileRecord{}, e
			}
			if cycle {
				return media.FileRecord{}, media.ErrConflict
			}
		}
	}
	if isFolder {
		if p.Meta != nil || p.Sensitive != nil || p.Indexed != nil && !*p.Indexed {
			return media.FileRecord{}, media.ErrInvalid
		}
		_, e = tx.ExecContext(ctx, `UPDATE owned_media_folders SET name=COALESCE($2,name),parent_id=CASE WHEN $3 THEN $4::uuid ELSE parent_id END,updated_at=clock_timestamp() WHERE id=$1`, id, p.Name, p.ParentPresent, p.ParentID)
	} else {
		if p.Name != nil {
			_, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET name=$2,updated_at=clock_timestamp() WHERE id=$1`, id, *p.Name)
		}
		if e == nil {
			marks, _ := json.Marshal(p.Sensitive)
			_, e = tx.ExecContext(ctx, `UPDATE owned_media_details SET parent_id=CASE WHEN $2 THEN $3::uuid ELSE parent_id END,indexed=COALESCE($4,indexed),user_meta=CASE WHEN $5 THEN $6::jsonb ELSE user_meta END,sensitive_marks=CASE WHEN $7 THEN $8::jsonb ELSE sensitive_marks END WHERE file_id=$1`, id, p.ParentPresent, p.ParentID, p.Indexed, p.Meta != nil, string(meta), p.Sensitive != nil, string(marks))
		}
	}
	if e != nil {
		return media.FileRecord{}, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.UpdatePermission); e != nil {
		return media.FileRecord{}, e
	}
	var out media.FileRecord
	if isFolder {
		out, e = r.folderTx(ctx, tx, actor, id)
	} else {
		out, e = r.fileRecordTx(ctx, tx, actor, id)
	}
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.UpdatePermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) DeleteFiles(ctx context.Context, actor string, ids []string) (int, error) {
	if len(ids) < 1 || len(ids) > 100 {
		return 0, media.ErrInvalid
	}
	seen := map[string]bool{}
	ordered := []string{}
	for _, id := range ids {
		if !authority.ValidID(id) || seen[id] {
			return 0, media.ErrInvalid
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	tx, e := r.begin(ctx, actor, media.DeletePermission)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	cleanup := []string{}
	for _, id := range ordered {
		var owner, state string
		e = tx.QueryRowContext(ctx, `SELECT account_id::text,state FROM owned_media_files WHERE id=$1 AND account_id=$2 FOR UPDATE`, id, actor).Scan(&owner, &state)
		if errors.Is(e, sql.ErrNoRows) {
			var children bool
			if e = tx.QueryRowContext(ctx, `SELECT account_id::text,EXISTS(SELECT 1 FROM owned_media_folders c WHERE c.parent_id=f.id AND c.deleted_at IS NULL) OR EXISTS(SELECT 1 FROM owned_media_details d JOIN owned_media_files m ON m.id=d.file_id WHERE d.parent_id=f.id AND m.state NOT IN('deleting','deleted')) FROM owned_media_folders f WHERE f.id=$1 AND f.account_id=$2 AND f.deleted_at IS NULL FOR UPDATE`, id, actor).Scan(&owner, &children); e != nil {
				return 0, media.ErrNotFound
			}
			if owner != actor {
				return 0, media.ErrNotFound
			}
			if children {
				return 0, media.ErrConflict
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_media_folders SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
				return 0, e
			}
			continue
		}
		if e != nil {
			return 0, e
		}
		if owner != actor {
			return 0, media.ErrNotFound
		}
		if state != "deleted" {
			if _, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
				return 0, e
			}
			cleanup = append(cleanup, id)
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	for _, id := range cleanup {
		if _, e = r.cleanupOne(ctx, id, false); e != nil {
			return 0, e
		}
	}
	return len(ids), nil
}
func (r *Repository) Pools(ctx context.Context, actor string) ([]media.Pool, error) {
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT pool.id::text,pool.name,pool.description,pool.account_id::text,pool.is_public,pool.allow_encryption,pool.max_file_bytes FROM owned_media_pools pool JOIN owned_authority_accounts a ON a.id=pool.account_id WHERE (pool.account_id=$1 OR pool.is_public) AND NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments punishment WHERE punishment.account_id=a.id AND punishment.kind='disable_account' AND(punishment.starts_at IS NULL OR punishment.starts_at<=clock_timestamp()) AND(punishment.expires_at IS NULL OR punishment.expires_at>clock_timestamp())) ORDER BY pool.name,pool.id`, actor)
	if e != nil {
		return nil, e
	}
	out := []media.Pool{}
	for rows.Next() {
		var p media.Pool
		if e = rows.Scan(&p.ID, &p.Name, &p.Description, &p.AccountID, &p.Public, &p.AllowEncryption, &p.MaxFileBytes); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	filtered := out[:0]
	for _, p := range out {
		if e = artworkOwnerActive(ctx, tx, p.AccountID); errors.Is(e, media.ErrNotFound) {
			continue
		} else if e != nil {
			return nil, e
		}
		filtered = append(filtered, p)
	}
	out = filtered
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) CreatePool(ctx context.Context, actor string, p media.Pool) (media.Pool, error) {
	if !authority.ValidID(p.ID) || !nameOK(p.Name) || len(p.Description) > 4096 || p.MaxFileBytes < 1 || p.MaxFileBytes > r.cfg.MaxFileBytes {
		return p, media.ErrInvalid
	}
	s := r.poolStores[p.ID]
	if s == nil {
		return p, media.ErrUnavailable
	}
	tx, e := r.begin(ctx, actor, "media.pools.manage")
	if e != nil {
		return p, e
	}
	defer tx.Rollback()
	if e = s.Ready(ctx); e != nil {
		return p, media.ErrUnavailable
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, "media.pools.manage"); e != nil {
		return p, e
	}
	p.AccountID = actor
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_media_pools(id,name,description,account_id,is_public,allow_encryption,max_file_bytes) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.Name, p.Description, actor, p.Public, p.AllowEncryption, p.MaxFileBytes)
	if e != nil {
		return p, e
	}
	return p, tx.Commit()
}
func (r *Repository) UploadStatistics(ctx context.Context, actor string) (media.UploadStats, error) {
	var out media.UploadStats
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	e = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE f.state='pending' AND f.expires_at>clock_timestamp() AND t.status IN('InProgress','Processing')),count(*) FILTER(WHERE t.status='Completed' AND f.state='ready'),count(*) FILTER(WHERE t.status='Failed'),count(*) FILTER(WHERE f.state='pending' AND f.expires_at<=clock_timestamp()),COALESCE(sum(f.size) FILTER(WHERE t.status='Completed'),0),COALESCE(avg(CASE WHEN t.status='Completed' THEN 100.0 ELSE COALESCE(c.n,0)*100.0/t.chunks_count END),0),max(f.updated_at) FROM owned_media_upload_tasks t JOIN owned_media_files f ON f.id=t.id LEFT JOIN (SELECT task_id,count(*) n FROM owned_media_upload_chunks GROUP BY task_id)c ON c.task_id=t.id WHERE f.account_id=$1`, actor).Scan(&out.TotalTasks, &out.InProgressTasks, &out.CompletedTasks, &out.FailedTasks, &out.ExpiredTasks, &out.TotalUploadedBytes, &out.AverageProgress, &out.RecentActivity)
	if e != nil {
		return out, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) UploadTaskDetails(ctx context.Context, actor, id string) (media.TaskDetails, error) {
	var out media.TaskDetails
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	out.Task, e = r.taskTx(ctx, tx, actor, id)
	if e != nil {
		return out, e
	}
	var opts []byte
	var created time.Time
	e = tx.QueryRowContext(ctx, `SELECT d.options,f.created_at,COALESCE((SELECT sum(size) FROM owned_media_upload_chunks WHERE task_id=f.id),0) FROM owned_media_files f JOIN owned_media_details d ON d.file_id=f.id WHERE f.id=$1 AND f.account_id=$2`, id, actor).Scan(&opts, &created, &out.UploadedBytes)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(opts, &out.Options); e != nil {
		return out, e
	}
	if out.Options.PoolID != "" {
		var pool media.Pool
		e = tx.QueryRowContext(ctx, `SELECT id::text,name,description,account_id::text,is_public,allow_encryption,max_file_bytes FROM owned_media_pools WHERE id=$1 AND(account_id=$2 OR is_public)`, out.Options.PoolID, actor).Scan(&pool.ID, &pool.Name, &pool.Description, &pool.AccountID, &pool.Public, &pool.AllowEncryption, &pool.MaxFileBytes)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		if e == nil {
			out.Pool = &pool
		}
	}
	if out.Options.BundleID != "" {
		bundle, e := scanBundle(tx.QueryRowContext(ctx, `SELECT `+bundleFields+` FROM owned_media_bundles WHERE id=$1 AND account_id=$2 AND deleted_at IS NULL AND(expired_at IS NULL OR expired_at>clock_timestamp())`, out.Options.BundleID, actor))
		if e != nil && !errors.Is(e, media.ErrNotFound) {
			return out, e
		}
		if e == nil {
			out.Bundle = &bundle
		}
	}
	elapsed := out.Task.LastActivity.Sub(created).Seconds()
	if elapsed > 0 && out.UploadedBytes > 0 {
		out.UploadSpeedBytes = float64(out.UploadedBytes) / elapsed
		seconds := float64(out.Task.FileSize-out.UploadedBytes) / out.UploadSpeedBytes
		out.EstimatedSeconds = &seconds
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (r *Repository) CleanupUploadTasks(ctx context.Context, actor string) (int, error) {
	tx, e := r.begin(ctx, actor, media.DeletePermission)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT f.id::text FROM owned_media_files f JOIN owned_media_upload_tasks t ON t.id=f.id WHERE f.account_id=$1 AND ((t.status='Failed' AND f.state<>'deleted') OR (f.state='pending' AND f.expires_at<=clock_timestamp()) OR f.state='deleting') ORDER BY f.id LIMIT 100 FOR UPDATE OF f`, actor)
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
	for _, id := range ids {
		if _, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='deleting',updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
			return 0, e
		}
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.DeletePermission); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	for _, id := range ids {
		if _, e = r.cleanupOne(ctx, id, false); e != nil {
			return 0, e
		}
	}
	return len(ids), nil
}
func (r *Repository) FileReferences(ctx context.Context, actor, id string) ([]media.File, error) {
	tx, e := r.begin(ctx, actor, media.ReadPermission)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = r.protectFileRead(ctx, tx, actor, id); e != nil {
		return nil, e
	}
	var key string
	var pool *string
	e = tx.QueryRowContext(ctx, `SELECT d.object_key::text,d.pool_id::text FROM owned_media_details d JOIN owned_media_files f ON f.id=d.file_id WHERE f.id=$1 AND f.account_id=$2 AND f.state='ready'`, id, actor).Scan(&key, &pool)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, media.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+`f.id::text,f.account_id::text,f.name,f.mime_type,f.size,f.sha256,f.is_public,f.state,f.created_at,f.updated_at,f.usage`+` FROM owned_media_files f JOIN owned_media_details d ON d.file_id=f.id WHERE f.account_id=$1 AND f.state='ready' AND d.object_key=$2 AND d.pool_id IS NOT DISTINCT FROM $3::uuid AND (d.expired_at IS NULL OR d.expired_at>clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM owned_media_bundles b WHERE b.id=d.bundle_id AND(b.deleted_at IS NOT NULL OR b.expired_at<=clock_timestamp())) ORDER BY f.id`, actor, key, pool)
	if e != nil {
		return nil, e
	}
	out := []media.File{}
	for rows.Next() {
		f, e := scan(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	filtered := out[:0]
	for _, f := range out {
		if e = r.protectFileRead(ctx, tx, actor, f.ID); errors.Is(e, media.ErrNotFound) {
			continue
		} else if e != nil {
			return nil, e
		}
		filtered = append(filtered, f)
	}
	out = filtered
	if e = r.protectFileRead(ctx, tx, actor, id); e != nil {
		return nil, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.ReadPermission); e != nil {
		return nil, e
	}
	return out, tx.Commit()
}
func (r *Repository) UploadRemote(ctx context.Context, actor string, v media.RemoteUploadRequest) (media.File, bool, error) {
	if r.remoteFetcher == nil {
		return media.File{}, false, media.ErrUnavailable
	}
	u := media.Upload{Name: v.Name, MIMEType: v.ContentType, SHA256: v.SHA256, Size: v.Size, IdempotencyKey: v.IdempotencyKey, Options: v.Options}
	if e := validUpload(u, r.cfg.MaxFileBytes); e != nil {
		return media.File{}, false, e
	}
	tx, e := r.begin(ctx, actor, media.CreatePermission)
	if e != nil {
		return media.File{}, false, e
	}
	if e = r.validateOptions(ctx, tx, actor, u.Options, u.MIMEType, u.Size); e != nil {
		tx.Rollback()
		return media.File{}, false, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, media.CreatePermission); e != nil {
		tx.Rollback()
		return media.File{}, false, e
	}
	tx.Rollback()
	body, e := r.remoteFetcher.Fetch(ctx, v.URL, u.Size+1)
	if e != nil {
		return media.File{}, false, media.ErrUnavailable
	}
	defer body.Close()
	return r.Upload(ctx, actor, u, io.LimitReader(body, u.Size+1))
}

var _ media.ManagementRepository = (*Repository)(nil)
