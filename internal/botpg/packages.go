// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPackageBytes = 5 * 1024 * 1024

// ReadPackageManifest validates a bounded ZIP and decodes its manifest. Other
// entries are decompressed to io.Discard to validate their sizes and CRCs.
// It never extracts files, evaluates scripts, fetches URLs, or provisions storage.
func ReadPackageManifest(data []byte) (bots.MiniManifest, error) {
	var out bots.MiniManifest
	if len(data) == 0 || len(data) > maxPackageBytes {
		return out, bots.ErrInvalid
	}
	archive, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || len(archive.File) == 0 || len(archive.File) > 1024 {
		return out, bots.ErrInvalid
	}
	var manifest *zip.File
	var total uint64
	names := map[string]bool{}
	for _, f := range archive.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !utf8.ValidString(name) || name == "" || len(name) > 1024 || strings.ContainsAny(name, "\\\x00:") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || f.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 || names[name] {
			return out, bots.ErrInvalid
		}
		names[name] = true
		if f.UncompressedSize64 > 32<<20 || total > 32<<20-f.UncompressedSize64 {
			return out, bots.ErrInvalid
		}
		total += f.UncompressedSize64
		if strings.EqualFold(path.Base(name), "manifest.json") {
			if manifest != nil || f.FileInfo().IsDir() || f.UncompressedSize64 > 16384 {
				return out, bots.ErrInvalid
			}
			manifest = f
		}
	}
	var expanded int64
	for _, entry := range archive.File {
		stream, x := entry.Open()
		if x != nil {
			return out, bots.ErrInvalid
		}
		n, x := io.Copy(io.Discard, io.LimitReader(stream, (32<<20)-expanded+1))
		closeErr := stream.Close()
		expanded += n
		if x != nil || closeErr != nil || expanded > 32<<20 {
			return out, bots.ErrInvalid
		}
	}
	if manifest == nil {
		return out, bots.ErrInvalid
	}
	reader, e := manifest.Open()
	if e != nil {
		return out, bots.ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(reader, 16385))
	closeErr := reader.Close()
	if e != nil || closeErr != nil || len(raw) > 16384 {
		return out, bots.ErrInvalid
	}
	out, e = bots.DecodePackageManifest(raw)
	if e != nil {
		return out, e
	}
	return out, nil
}
func mediaError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, media.ErrNotFound) || errors.Is(e, sql.ErrNoRows) {
		return bots.ErrNotFound
	}
	if errors.Is(e, media.ErrForbidden) {
		return bots.ErrForbidden
	}
	return e
}
func (r *Repository) LinkPackage(ctx context.Context, s apps.Scope, id, fileID string) (out bots.PackageResult, e error) {
	if r == nil || r.media == nil {
		return out, bots.ErrUnsupported
	}
	if !authority.ValidID(id) || !authority.ValidID(fileID) {
		return out, bots.ErrInvalid
	}
	e = r.within(ctx, s, "", "developers.miniapps.package.upload", 50, func(tx *sql.Tx, _ appScope, now time.Time) error {
		m, e := loadMini(ctx, tx, s.App, id)
		if e != nil {
			return e
		}
		if m.DeletedAt != nil {
			return bots.ErrNotFound
		}
		file, e := r.media.ResolvePublicInTransaction(ctx, tx, s.Actor, fileID)
		if e != nil {
			return mediaError(e)
		}
		if file.AccountID != s.Actor {
			return bots.ErrForbidden
		}
		if file.Size <= 0 || file.Size > maxPackageBytes || !file.Public || file.State != "ready" {
			return bots.ErrInvalid
		}
		if file.MIMEType != "application/zip" && file.MIMEType != "application/x-zip-compressed" && !strings.EqualFold(path.Ext(file.Name), ".zip") {
			return bots.ErrInvalid
		}
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		current, reader, e := r.media.Open(bounded, "", fileID)
		if e != nil {
			return mediaError(e)
		}
		defer reader.Close()
		if !current.Public || current.ID != file.ID || current.Size != file.Size || current.SHA256 != file.SHA256 {
			return bots.ErrConflict
		}
		data, e := io.ReadAll(io.LimitReader(reader, maxPackageBytes+1))
		if e != nil || int64(len(data)) != file.Size {
			return bots.ErrInvalid
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return bots.ErrConflict
		}
		manifest, e := ReadPackageManifest(data)
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET manifest=$2,plugin_id=$3,package_file_id=$4,package_fingerprint=$5,updated_at=$6 WHERE id=$1`, id, encoded(manifest), manifest.ID, file.ID, file.SHA256, now); e != nil {
			return e
		}
		out = bots.PackageResult{Key: file.ID, URL: "/api/miniapps/" + m.Slug + "/package", FileName: file.Name, ContentType: "application/zip", Size: file.Size, SHA256: file.SHA256}
		return audit(ctx, tx, s, "miniapp.package.link", id, now)
	})
	return
}
func (r *Repository) OpenPackage(ctx context.Context, slug string) (file media.File, reader io.ReadSeekCloser, err error) {
	if r == nil || r.db == nil || r.media == nil {
		return file, nil, bots.ErrUnsupported
	}
	if len(slug) < 2 || len(slug) > 256 {
		return file, nil, bots.ErrInvalid
	}
	tx, e := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return file, nil, e
	}
	defer tx.Rollback()
	var id, fileID, hash, publisher, project, app string
	e = tx.QueryRowContext(ctx, `SELECT m.id::text,m.package_file_id::text,m.package_fingerprint,pub.id::text,project.id::text,a.id::text`+miniPublicJoin+` AND lower(m.slug)=lower($1) AND m.package_file_id IS NOT NULL`, slug).Scan(&id, &fileID, &hash, &publisher, &project, &app)
	if e != nil {
		return file, nil, mapped(e)
	}
	// Match private app-write ordering; lock the counter row exclusively to
	// avoid two readers deadlocking while upgrading SHARE to UPDATE.
	for _, v := range []struct{ table, id string }{{"owned_publishers", publisher}, {"owned_dev_projects", project}, {"owned_custom_apps", app}} {
		var found string
		if e = tx.QueryRowContext(ctx, `SELECT id::text FROM `+v.table+` WHERE id=$1 FOR SHARE`, v.id).Scan(&found); e != nil {
			return file, nil, mapped(e)
		}
	}
	var found string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM owned_bot_miniapps WHERE id=$1 FOR UPDATE`, id).Scan(&found); e != nil {
		return file, nil, mapped(e)
	}
	e = tx.QueryRowContext(ctx, `SELECT m.package_file_id::text,m.package_fingerprint`+miniPublicJoin+` AND m.id=$1 AND lower(m.slug)=lower($2) AND m.package_file_id IS NOT NULL`, id, slug).Scan(&fileID, &hash)
	if e != nil {
		return file, nil, mapped(e)
	}
	locked, e := r.media.ResolvePublicInTransaction(ctx, tx, "", fileID)
	if e != nil {
		return file, nil, mediaError(e)
	}
	if locked.SHA256 != hash {
		return file, nil, bots.ErrNotFound
	}
	file, reader, e = r.media.Open(ctx, "", fileID)
	if e != nil {
		return file, nil, mediaError(e)
	}
	if !file.Public || file.State != "ready" || file.SHA256 != hash || file.Size != locked.Size {
		reader.Close()
		return media.File{}, nil, bots.ErrNotFound
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET download_count=download_count+1 WHERE id=$1`, id); e != nil {
		reader.Close()
		return media.File{}, nil, e
	}
	if e = tx.Commit(); e != nil {
		reader.Close()
		return media.File{}, nil, e
	}
	return file, reader, nil
}
