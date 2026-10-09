// SPDX-License-Identifier: AGPL-3.0-only
package botpg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/media"
	"time"
)

type transactionalMediaUploader interface {
	ReserveParentUpload(context.Context, string, media.Upload) (media.File, func(context.Context) error, error)
	FinishParentUpload(context.Context, *sql.Tx, string, media.File, io.Reader) (media.File, error)
}

func (r *Repository) UploadPackage(ctx context.Context, s apps.Scope, id, name string, input io.Reader) (out bots.PackageResult, err error) {
	if r == nil || r.media == nil {
		return out, bots.ErrUnsupported
	}
	uploader, ok := r.media.(transactionalMediaUploader)
	if !ok {
		return out, bots.ErrUnsupported
	}
	if !authority.ValidID(id) || input == nil {
		return out, bots.ErrInvalid
	}
	timeout := r.packageUploadTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Check the original app permission before reading or reserving any bytes.
	err = r.within(ctx, s, "", "developers.miniapps.package.upload", 50, func(tx *sql.Tx, _ appScope, _ time.Time) error {
		current, e := loadMini(ctx, tx, s.App, id)
		if e != nil {
			return e
		}
		if current.DeletedAt != nil {
			return bots.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	spool, err := os.CreateTemp("", "solsynth-miniapp-*.zip")
	if err != nil {
		return out, err
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(spool, hash), io.LimitReader(packageContextReader{ctx, input}, bots.MaxPackageBytes+1))
	if err != nil || size <= 0 || size > bots.MaxPackageBytes {
		return out, bots.ErrInvalid
	}
	manifest, err := readPackageManifestAt(spool, size)
	if err != nil {
		return out, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		return out, err
	}
	// A durable canonical pending row survives process failure until TTL cleanup.
	reserved, cleanup, err := uploader.ReserveParentUpload(ctx, s.Actor, media.Upload{Name: name, MIMEType: "application/zip", Size: size, SHA256: digest})
	if err != nil {
		return out, mediaError(err)
	}
	err = r.within(ctx, s, "", "developers.miniapps.package.upload", 50, func(tx *sql.Tx, _ appScope, now time.Time) error {
		current, e := loadMini(ctx, tx, s.App, id)
		if e != nil {
			return e
		}
		if current.DeletedAt != nil {
			return bots.ErrNotFound
		}
		file, e := uploader.FinishParentUpload(ctx, tx, s.Actor, reserved, spool)
		if e != nil {
			return mediaError(e)
		}
		if file.AccountID != s.Actor || file.Public || file.Size != size || file.SHA256 != digest || file.State != "ready" {
			return bots.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE owned_bot_miniapps SET manifest=$2,plugin_id=$3,package_file_id=$4,package_fingerprint=$5,package_owner_id=$6,updated_at=$7 WHERE id=$1`, id, encoded(manifest), manifest.ID, file.ID, digest, s.Actor, now); e != nil {
			return e
		}
		if e = audit(ctx, tx, s, "miniapp.package.upload", id, now); e != nil {
			return e
		}
		if _, e = r.guard.Authorize(ctx, r.db, tx, s.Actor, media.CreatePermission); e != nil {
			return e
		}
		out = bots.PackageResult{Key: file.ID, URL: "/api/miniapps/" + current.Slug + "/package", FileName: file.Name, ContentType: "application/zip", Size: size, SHA256: digest}
		return nil
	})
	if cleanup != nil && err != nil {
		cleanCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		err = errors.Join(err, cleanup(cleanCtx))
	}
	return out, err
}

type packageContextReader struct {
	ctx  context.Context
	body io.Reader
}

func (r packageContextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.body.Read(p)
}

func (r *Repository) ResolveMiniScope(ctx context.Context, s apps.Scope, id string) (apps.Scope, error) {
	if r == nil || r.db == nil || r.guard == nil {
		return s, bots.ErrUnavailable
	}
	if !authority.ValidID(s.Actor) || !authority.ValidID(s.Project) || s.Developer == "" || id != "" && !authority.ValidID(id) {
		return s, bots.ErrInvalid
	}

	// A current viewer membership gates even association/cardinality discovery.
	// This short transaction ends before within takes its sorted owner lock set.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	if _, err = r.guard.Authenticate(ctx, r.db, tx, s.Actor); err != nil {
		return s, err
	}
	where := ancestry + ` JOIN owned_publisher_members member ON member.publisher_id=pub.id WHERE project.id=$1 AND ` + accountpg.PublisherNamePredicate("pub", "$2") + ` AND ` + liveAncestry + ` AND member.account_id=$3 AND member.joined_at IS NOT NULL AND member.revoked_at IS NULL AND member.role>=0`
	if id != "" {
		err = tx.QueryRowContext(ctx, `SELECT a.id::text`+where+` AND EXISTS(SELECT 1 FROM owned_bot_miniapps m WHERE m.id=$4 AND m.app_id=a.id AND m.project_id=project.id AND m.deleted_at IS NULL)`, s.Project, s.Developer, s.Actor, id).Scan(&s.App)
		if err != nil {
			return s, mapped(err)
		}
	} else {
		rows, e := tx.QueryContext(ctx, `SELECT a.id::text`+where+` ORDER BY a.id LIMIT 2`, s.Project, s.Developer, s.Actor)
		if e != nil {
			return s, e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return s, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return s, e
		}
		if len(ids) == 0 {
			return s, bots.ErrNotFound
		}
		if len(ids) != 1 {
			return s, bots.ErrConflict
		}
		s.App = ids[0]
	}
	if _, err = r.guard.Authenticate(ctx, r.db, tx, s.Actor); err != nil {
		return s, err
	}
	return s, tx.Commit()
}
