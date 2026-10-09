// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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
	return readPackageManifestAt(bytes.NewReader(data), int64(len(data)))
}
func readPackageManifestAt(source io.ReaderAt, size int64) (bots.MiniManifest, error) {
	var out bots.MiniManifest
	if size <= 0 || size > maxPackageBytes {
		return out, bots.ErrInvalid
	}
	if _, ok := validatedZipDirectory(source, size); !ok {
		return out, bots.ErrInvalid
	}
	archive, e := zip.NewReader(source, size)
	if e != nil || len(archive.File) == 0 || len(archive.File) > 1024 {
		return out, bots.ErrInvalid
	}
	var manifest *zip.File
	var total uint64
	names := []string{}
	for _, f := range archive.File {
		name := strings.TrimSuffix(f.Name, "/")
		duplicate := false
		for _, previous := range names {
			if strings.EqualFold(previous, name) {
				duplicate = true
				break
			}
		}
		if !utf8.ValidString(name) || name == "" || len(name) > 1024 || strings.ContainsAny(name, "\\\x00:") || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") || f.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 || duplicate {
			return out, bots.ErrInvalid
		}
		names = append(names, name)
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
	entryFound := false
	for _, f := range archive.File {
		if f.Name == out.Entry && !f.FileInfo().IsDir() {
			entryFound = true
			break
		}
	}
	if !entryFound {
		return bots.MiniManifest{}, bots.ErrInvalid
	}
	return out, nil
}

// Check central-directory bounds before archive/zip allocates one File per
// entry. Untrusted declared counts alone are insufficient: count every record
// with constant memory and reject ZIP64/multi-volume forms outside our limits.
func boundedZipDirectory(source io.ReaderAt, size int64) bool {
	_, ok := validatedZipDirectory(source, size)
	return ok
}

type zipDirectoryEntry struct {
	name        string
	localOffset int64
}

func validatedZipDirectory(source io.ReaderAt, size int64) ([]zipDirectoryEntry, bool) {
	if size < 22 {
		return nil, false
	}
	tail := make([]byte, min(size, 65535+22))
	if _, err := source.ReadAt(tail, size-int64(len(tail))); err != nil {
		return nil, false
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:i+4]) != 0x06054b50 {
			continue
		}
		// archive/zip selects the last end record even when bytes follow its
		// comment. Reject that ambiguity instead of validating an earlier record.
		if i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) != len(tail) {
			return nil, false
		}
		end := tail[i:]
		count := int(binary.LittleEndian.Uint16(end[10:12]))
		if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 || int(binary.LittleEndian.Uint16(end[8:10])) != count || count < 1 || count > 1024 {
			return nil, false
		}
		length := int64(binary.LittleEndian.Uint32(end[12:16]))
		offset := int64(binary.LittleEndian.Uint32(end[16:20]))
		limit := size - int64(len(tail)) + int64(i)
		if offset < 0 || length < 0 || offset+length != limit {
			return nil, false
		}
		var header [46]byte
		actual := 0
		entries := make([]zipDirectoryEntry, 0, count)
		for offset < limit {
			actual++
			if actual > count || limit-offset < int64(len(header)) {
				return nil, false
			}
			if _, err := source.ReadAt(header[:], offset); err != nil || binary.LittleEndian.Uint32(header[:4]) != 0x02014b50 {
				return nil, false
			}
			nameLength := int64(binary.LittleEndian.Uint16(header[28:30]))
			extraLength := int64(binary.LittleEndian.Uint16(header[30:32]))
			commentLength := int64(binary.LittleEndian.Uint16(header[32:34]))
			if nameLength < 1 || nameLength > 1025 || binary.LittleEndian.Uint16(header[34:36]) != 0 || offset+46+nameLength+extraLength+commentLength > limit {
				return nil, false
			}
			nameBytes := make([]byte, nameLength)
			if _, err := source.ReadAt(nameBytes, offset+46); err != nil || !utf8.Valid(nameBytes) {
				return nil, false
			}
			name := string(nameBytes)
			for _, previous := range entries {
				if strings.EqualFold(previous.name, name) {
					return nil, false
				}
			}
			localOffset := int64(binary.LittleEndian.Uint32(header[42:46]))
			if localOffset < 0 || localOffset > limit-30 {
				return nil, false
			}
			var local [30]byte
			if _, err := source.ReadAt(local[:], localOffset); err != nil || binary.LittleEndian.Uint32(local[:4]) != 0x04034b50 {
				return nil, false
			}
			localNameLength := int64(binary.LittleEndian.Uint16(local[26:28]))
			localExtraLength := int64(binary.LittleEndian.Uint16(local[28:30]))
			if localNameLength != nameLength || localOffset+30+localNameLength+localExtraLength > limit {
				return nil, false
			}
			localName := make([]byte, localNameLength)
			if _, err := source.ReadAt(localName, localOffset+30); err != nil || !bytes.Equal(localName, nameBytes) {
				return nil, false
			}
			entries = append(entries, zipDirectoryEntry{name: name, localOffset: localOffset})
			offset += 46 + nameLength + extraLength + commentLength
		}
		return entries, actual == count
	}
	return nil, false
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
		timeout := r.packageUploadTimeout
		if timeout == 0 {
			timeout = 2 * time.Minute
		}
		bounded, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		current, reader, e := r.media.Open(bounded, "", fileID)
		if e != nil {
			return mediaError(e)
		}
		defer reader.Close()
		if !current.Public || current.ID != file.ID || current.Size != file.Size || current.SHA256 != file.SHA256 {
			return bots.ErrConflict
		}
		data, e := io.ReadAll(io.LimitReader(packageContextReader{ctx: bounded, body: reader}, maxPackageBytes+1))
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
		if e = bounded.Err(); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET manifest=$2,plugin_id=$3,package_file_id=$4,package_fingerprint=$5,package_owner_id=NULL,updated_at=$6 WHERE id=$1`, id, encoded(manifest), manifest.ID, file.ID, file.SHA256, now); e != nil {
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
	locked, e := scanMiniMedia(tx.QueryRowContext(ctx, `SELECT `+miniMediaFields+` FROM owned_media_files f JOIN owned_authority_accounts owner ON owner.id=f.account_id JOIN owned_bot_miniapps m ON m.package_file_id=f.id WHERE m.id=$1 AND f.id=$2 AND f.state='ready' AND (f.is_public OR m.package_owner_id=f.account_id) AND `+miniMediaActiveOwner+` FOR SHARE OF f`, id, fileID))
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
	if file.State != "ready" || file.SHA256 != hash || file.Size != locked.Size || file.ID != locked.ID {
		reader.Close()
		return media.File{}, nil, bots.ErrNotFound
	}
	if e = tx.QueryRowContext(ctx, `SELECT m.id::text`+miniPublicJoin+` AND m.id=$1 AND m.package_file_id=$2 AND m.package_fingerprint=$3`, id, fileID, hash).Scan(&found); e != nil {
		reader.Close()
		return media.File{}, nil, mapped(e)
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
