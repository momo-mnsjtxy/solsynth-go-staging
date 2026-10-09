// SPDX-License-Identifier: AGPL-3.0-only
package livestreampg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

const archiveImportPermission = "livestreams.hls.import"

func scanArchiveImport(row scanner) (v live.ArchiveImport, session string, version, epoch int64, request []byte, e error) {
	e = row.Scan(&v.ID, &v.StreamID, &v.OutputID, &v.State, &v.MediaID, &v.Reason, &v.CreatedAt, &v.UpdatedAt, &session, &version, &epoch, &request)
	if errors.Is(e, sql.ErrNoRows) {
		e = live.ErrNotFound
	}
	return
}

const archiveImportFields = `id::text,stream_id::text,output_id::text,state,media_id::text,reason,created_at,updated_at,session_id::text,account_version,authorization_epoch,request_hash`

func (r *Repository) archiveImporter() (live.ArchiveImporter, error) {
	if r == nil || r.outputs == nil || r.outputs.ArchiveImporter == nil || r.outputs.ArchiveImporter.ArchiveImportContract() != live.ArchiveImportContract {
		return nil, live.ErrOutputUnavailable
	}
	return r.outputs.ArchiveImporter, nil
}

func (r *Repository) archiveOwnerCurrent(ctx context.Context, tx *sql.Tx, v outputRecord, stream live.Stream) error {
	s, e := observe(ctx, tx, stream.PublisherID, "")
	if e != nil || s.owner != v.Owner {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	var version int64
	if e = tx.QueryRowContext(ctx, `SELECT version FROM owned_authority_accounts WHERE id=$1::uuid`, s.owner).Scan(&version); e != nil {
		return e
	}
	if version != v.OwnerVersion {
		return live.ErrForbidden
	}
	return nil
}

func (r *Repository) RequestArchiveImport(ctx context.Context, actor, sid, outputID, key string) (out live.ArchiveImport, e error) {
	if _, e = r.archiveImporter(); e != nil {
		return
	}
	if !authority.ValidID(actor) || !authority.ValidID(sid) || !authority.ValidID(outputID) || key == "" || len(key) > 128 {
		return out, live.ErrInvalid
	}
	tx, principal, stream, e := r.locked(ctx, actor, sid, "", archiveImportPermission, true, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, outputID)
	if e != nil {
		return out, e
	}
	if v.Stream != sid || v.Kind != "hls" || v.Phase != "done" || v.Finished == nil || len(v.Destinations) != 1 || !r.outputNamespace(v.Destinations[0]) {
		return out, live.ErrNotFound
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		return out, e
	}
	playlist, _, e := r.artifact(ctx, v, v.Playlist, r.outputs.MaxPlaylistBytes)
	if e != nil {
		return out, e
	}
	if _, _, e = live.ProjectHLS(playlist, v.Stream, v.ID, r.outputs.MaxSegments); e != nil {
		return out, e
	}
	digest := sha256.Sum256(playlist)
	request := hash(struct {
		Stream, Output, Playlist, Namespace string
		Finished                            time.Time
		PlaylistSHA256                      [32]byte
	}{sid, outputID, v.Playlist, v.Destinations[0], v.Finished.UTC(), digest})
	keyHash := hash(key)
	prior, _, _, _, stored, x := scanArchiveImport(tx.QueryRowContext(ctx, `SELECT `+archiveImportFields+` FROM owned_livestream_archive_imports WHERE stream_id=$1::uuid AND actor_id=$2::uuid AND key_hash=$3::bytea FOR UPDATE`, sid, actor, keyHash))
	if x == nil {
		if prior.OutputID != outputID || !bytes.Equal(stored, request) {
			return out, live.ErrConflict
		}
		return prior, r.commit(ctx, tx, actor, archiveImportPermission)
	}
	if !errors.Is(x, live.ErrNotFound) {
		return out, x
	}
	id, e := uid()
	if e != nil {
		return out, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_archive_imports(id,stream_id,output_id,actor_id,session_id,account_version,authorization_epoch,key_hash,request_hash,state,next_attempt_at,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::bigint,$7::bigint,$8::bytea,$9::bytea,'pending',$10::timestamptz,$10::timestamptz,$10::timestamptz)`, id, sid, outputID, actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, keyHash, request, at)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.hls.import.requested"); e != nil {
		return out, e
	}
	out = live.ArchiveImport{ID: id, StreamID: sid, OutputID: outputID, State: "pending", CreatedAt: at, UpdatedAt: at}
	return out, r.commit(ctx, tx, actor, archiveImportPermission)
}

func (r *Repository) GetArchiveImport(ctx context.Context, actor, sid, outputID, id string) (out live.ArchiveImport, e error) {
	if !authority.ValidID(actor) || !authority.ValidID(sid) || !authority.ValidID(outputID) || !authority.ValidID(id) {
		return out, live.ErrInvalid
	}
	tx, _, stream, e := r.locked(ctx, actor, sid, "", archiveImportPermission, true, true)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, outputID)
	if e != nil || v.Stream != sid || v.Kind != "hls" {
		if e != nil {
			return out, e
		}
		return out, live.ErrNotFound
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		return out, e
	}
	out, _, _, _, _, e = scanArchiveImport(tx.QueryRowContext(ctx, `SELECT `+archiveImportFields+` FROM owned_livestream_archive_imports WHERE id=$1::uuid AND stream_id=$2::uuid AND output_id=$3::uuid`, id, sid, outputID))
	if e != nil {
		return out, e
	}
	return out, r.commit(ctx, tx, actor, archiveImportPermission)
}

func (r *Repository) processArchiveImport(ctx context.Context, id string) error {
	importer, e := r.archiveImporter()
	if e != nil {
		return e
	}
	var actor, sid, outputID string
	if e = r.db.QueryRowContext(ctx, `SELECT actor_id::text,stream_id::text,output_id::text FROM owned_livestream_archive_imports WHERE id=$1::uuid`, id).Scan(&actor, &sid, &outputID); e != nil {
		return e
	}
	tx, stream, e := r.outputSystemLock(ctx, sid, actor)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, outputID)
	if e != nil || v.Stream != sid || v.Kind != "hls" || v.Phase != "done" || v.Finished == nil || len(v.Destinations) != 1 || !r.outputNamespace(v.Destinations[0]) {
		if e != nil {
			return e
		}
		return live.ErrConflict
	}
	job, session, version, epoch, storedRequest, e := scanArchiveImport(tx.QueryRowContext(ctx, `SELECT `+archiveImportFields+` FROM owned_livestream_archive_imports WHERE id=$1::uuid FOR UPDATE NOWAIT`, id))
	if e != nil || job.State == "complete" {
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	if job.State != "pending" && job.State != "processing" {
		return live.ErrConflict
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		return e
	}
	ok, e := r.eligible(ctx, tx, actor, session, version, epoch, archiveImportPermission)
	if e != nil || !ok {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	if ok, e = r.publishers.CanEditPublisherInTransaction(ctx, tx, actor, stream.PublisherID); e != nil || !ok {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	playlist, _, e := r.artifact(ctx, v, v.Playlist, r.outputs.MaxPlaylistBytes)
	if e != nil {
		return e
	}
	if _, _, e = live.ProjectHLS(playlist, v.Stream, v.ID, r.outputs.MaxSegments); e != nil {
		return e
	}
	digest := sha256.Sum256(playlist)
	currentRequest := hash(struct {
		Stream, Output, Playlist, Namespace string
		Finished                            time.Time
		PlaylistSHA256                      [32]byte
	}{sid, outputID, v.Playlist, v.Destinations[0], v.Finished.UTC(), digest})
	if !bytes.Equal(storedRequest, currentRequest) {
		return live.ErrConflict
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='processing',updated_at=clock_timestamp() WHERE id=$1::uuid`, id); e != nil {
		return e
	}
	op, cancel := context.WithTimeout(ctx, r.outputs.OperationTimeout)
	mediaID, e := importer.ImportHLSArchive(op, live.ArchiveImportSource{JobID: id, ActorID: actor, StreamID: sid, OutputID: outputID, Namespace: v.Destinations[0], Playlist: v.Playlist, PlaylistSHA256: hex.EncodeToString(digest[:])})
	cancel()
	if e != nil {
		return e
	}
	if !authority.ValidID(mediaID) {
		return live.ErrProvider
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='complete',media_id=$2::uuid,reason='',updated_at=clock_timestamp() WHERE id=$1::uuid AND state='processing'`, id, mediaID)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return e
		}
		return live.ErrConflict
	}
	if e = audit(ctx, tx, sid, actor, "livestreams.hls.import.completed"); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) ProcessArchiveImportsDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if _, e := r.archiveImporter(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id::text FROM owned_livestream_archive_imports WHERE state IN('pending','processing') AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,id LIMIT $1::integer`, limit)
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
	n := 0
	var result error
	for _, id := range ids {
		if e = r.processArchiveImport(ctx, id); e == nil {
			n++
			continue
		}
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "55P03" {
			continue
		}
		result = errors.Join(result, e)
		_, x := r.db.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='pending',attempts=LEAST(attempts+1,30),reason='import_pending',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp()+LEAST(3600,1<<LEAST(attempts,12))*interval '1 second' WHERE id=$1::uuid AND state<>'complete'`, id)
		result = errors.Join(result, x)
	}
	return n, result
}
