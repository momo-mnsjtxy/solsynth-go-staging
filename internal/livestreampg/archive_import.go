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
	"solsynth.local/go-migration/ports/media"
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
	return activeAccount(ctx, tx, s.owner)
}

func archiveOutputValid(v outputRecord, sid string) bool {
	return v.Stream == sid && v.Kind == "hls" && v.Phase == "done" && v.Finished != nil && len(v.Destinations) == 1 && v.Playlist != ""
}

func archiveOutputHash(v outputRecord) []byte {
	return hash(struct {
		ID, Stream, Kind, Provider, Endpoint, Room, Actor, Session, Owner, Permission, Playlist, Layout string
		Generation, Version, Epoch, OwnerVersion                                                        int64
		Destinations                                                                                    []string
		PolicyHash, RequestHash                                                                         []byte
		SegmentDuration                                                                                 int
		Finished                                                                                        time.Time
	}{v.ID, v.Stream, v.Kind, v.Provider, v.Endpoint, v.Room, v.Actor, v.Session, v.Owner, v.Permission, v.Playlist, v.Layout, v.Generation, v.Version, v.Epoch, v.OwnerVersion, v.Destinations, v.PolicyHash, v.RequestHash, v.SegmentDuration, v.Finished.UTC()})
}

func archiveRequestHash(v outputRecord, playlistSHA string) []byte {
	return hash(struct {
		OutputHash, PlaylistSHA string
	}{hex.EncodeToString(archiveOutputHash(v)), playlistSHA})
}

func (r *Repository) lockArchiveRequest(ctx context.Context, actor, sid, outputID string) (*sql.Tx, authority.Principal, live.Stream, outputRecord, error) {
	tx, principal, stream, e := r.locked(ctx, actor, sid, "", archiveImportPermission, true, true)
	if e != nil {
		return nil, principal, stream, outputRecord{}, e
	}
	v, e := loadOutput(ctx, tx, outputID)
	if e != nil || !archiveOutputValid(v, sid) || !r.outputNamespace(v.Destinations[0]) {
		if e == nil {
			e = live.ErrNotFound
		}
		tx.Rollback()
		return nil, principal, stream, outputRecord{}, e
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		tx.Rollback()
		return nil, principal, stream, outputRecord{}, e
	}
	if _, e = r.guard.Authorize(ctx, r.db, tx, actor, archiveImportPermission, media.CreatePermission); e != nil {
		tx.Rollback()
		return nil, principal, stream, outputRecord{}, e
	}
	return tx, principal, stream, v, nil
}

func (r *Repository) RequestArchiveImport(ctx context.Context, actor, sid, outputID, key string) (out live.ArchiveImport, e error) {
	if _, e = r.archiveImporter(); e != nil {
		return
	}
	if !authority.ValidID(actor) || !authority.ValidID(sid) || !authority.ValidID(outputID) || key == "" || len(key) > 128 {
		return out, live.ErrInvalid
	}
	first, _, _, observed, e := r.lockArchiveRequest(ctx, actor, sid, outputID)
	if e != nil {
		return out, e
	}
	observedHash := archiveOutputHash(observed)
	if e = r.commit(ctx, first, actor, archiveImportPermission); e != nil {
		first.Rollback()
		return out, e
	}
	playlist, _, e := r.artifact(ctx, observed, observed.Playlist, r.outputs.MaxPlaylistBytes)
	if e != nil {
		return out, e
	}
	if _, _, e = live.ProjectHLS(playlist, observed.Stream, observed.ID, r.outputs.MaxSegments); e != nil {
		return out, e
	}
	digest := sha256.Sum256(playlist)
	playlistSHA := hex.EncodeToString(digest[:])
	tx, principal, _, current, e := r.lockArchiveRequest(ctx, actor, sid, outputID)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if !bytes.Equal(observedHash, archiveOutputHash(current)) {
		return out, live.ErrConflict
	}
	request := archiveRequestHash(current, playlistSHA)
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
	_, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_archive_imports(id,stream_id,output_id,actor_id,session_id,account_version,authorization_epoch,key_hash,request_hash,playlist_sha256,state,next_attempt_at,created_at,updated_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::bigint,$7::bigint,$8::bytea,$9::bytea,$10::text,'pending',$11::timestamptz,$11::timestamptz,$11::timestamptz)`, id, sid, outputID, actor, principal.Session.ID, principal.Account.Version, principal.Session.AuthorizationEpoch, keyHash, request, playlistSHA, at)
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

type archiveClaim struct {
	ID, Actor, Stream, Output, Session, Lease, Namespace, Playlist, PlaylistSHA string
	Version, Epoch                                                              int64
}

var errArchiveNoWork = errors.New("livestream archive import is not claimable")

func failArchiveClaim(ctx context.Context, tx *sql.Tx, id, reason string, cause error) error {
	_, e := tx.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='failed',attempts=LEAST(attempts+1,30),reason=$2::text,updated_at=clock_timestamp(),next_attempt_at=clock_timestamp(),lease_token=NULL,lease_expires_at=NULL WHERE id=$1::uuid AND state IN('pending','processing')`, id, reason)
	if e != nil {
		return errors.Join(cause, e)
	}
	return errors.Join(cause, tx.Commit())
}

func (r *Repository) claimArchiveImport(ctx context.Context, id string) (archiveClaim, error) {
	var out archiveClaim
	if e := r.db.QueryRowContext(ctx, `SELECT actor_id::text,stream_id::text,output_id::text FROM owned_livestream_archive_imports WHERE id=$1::uuid`, id).Scan(&out.Actor, &out.Stream, &out.Output); e != nil {
		return out, e
	}
	tx, stream, e := r.outputSystemLock(ctx, out.Stream, out.Actor)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, out.Output)
	if e != nil {
		return out, e
	}
	var state string
	var leaseExpires *time.Time
	var request []byte
	e = tx.QueryRowContext(ctx, `SELECT session_id::text,account_version,authorization_epoch,state,COALESCE(lease_expires_at,clock_timestamp()),request_hash,playlist_sha256 FROM owned_livestream_archive_imports WHERE id=$1::uuid FOR UPDATE NOWAIT`, id).Scan(&out.Session, &out.Version, &out.Epoch, &state, &leaseExpires, &request, &out.PlaylistSHA)
	if e != nil {
		return out, e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return out, e
	}
	if state != "pending" && !(state == "processing" && leaseExpires != nil && !leaseExpires.After(at)) {
		return out, errArchiveNoWork
	}
	if !archiveOutputValid(v, out.Stream) || !r.outputNamespace(v.Destinations[0]) {
		return out, failArchiveClaim(ctx, tx, id, "source_changed", live.ErrConflict)
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		if errors.Is(e, live.ErrForbidden) {
			return out, failArchiveClaim(ctx, tx, id, "owner_unavailable", e)
		}
		return out, e
	}
	ok, e := r.eligible(ctx, tx, out.Actor, out.Session, out.Version, out.Epoch, archiveImportPermission)
	if e != nil || !ok {
		if e != nil {
			return out, e
		}
		return out, failArchiveClaim(ctx, tx, id, "authority_revoked", live.ErrForbidden)
	}
	if ok, e = r.eligible(ctx, tx, out.Actor, out.Session, out.Version, out.Epoch, media.CreatePermission); e != nil || !ok {
		if e != nil {
			return out, e
		}
		return out, failArchiveClaim(ctx, tx, id, "authority_revoked", live.ErrForbidden)
	}
	if ok, e = r.publishers.CanEditPublisherInTransaction(ctx, tx, out.Actor, stream.PublisherID); e != nil || !ok {
		if e != nil {
			return out, e
		}
		return out, failArchiveClaim(ctx, tx, id, "publisher_access_revoked", live.ErrForbidden)
	}
	if !bytes.Equal(request, archiveRequestHash(v, out.PlaylistSHA)) {
		return out, failArchiveClaim(ctx, tx, id, "source_changed", live.ErrConflict)
	}
	out.Lease, e = uid()
	if e != nil {
		return out, e
	}
	out.ID, out.Namespace, out.Playlist = id, v.Destinations[0], v.Playlist
	result, e := tx.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='processing',lease_token=$2::uuid,lease_expires_at=$3::timestamptz,updated_at=$4::timestamptz,reason='import_pending' WHERE id=$1::uuid AND(state='pending' OR(state='processing' AND lease_expires_at<=clock_timestamp()))`, id, out.Lease, at.Add(6*time.Minute), at)
	if e != nil {
		return out, e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return out, e
		}
		return out, errArchiveNoWork
	}
	return out, tx.Commit()
}

func (r *Repository) finalizeArchiveImport(ctx context.Context, claim archiveClaim, receipt live.ArchiveImportReceipt) error {
	if !authority.ValidID(receipt.MediaID) || len(receipt.SourceSHA256) != 64 {
		return live.ErrProvider
	}
	tx, stream, e := r.outputSystemLock(ctx, claim.Stream, claim.Actor)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v, e := loadOutput(ctx, tx, claim.Output)
	if e != nil || !archiveOutputValid(v, claim.Stream) || !r.outputNamespace(v.Destinations[0]) {
		if e == nil {
			e = live.ErrConflict
		}
		return e
	}
	var state, lease, session, playlistSHA string
	var version, epoch int64
	var request []byte
	e = tx.QueryRowContext(ctx, `SELECT state,COALESCE(lease_token::text,''),session_id::text,account_version,authorization_epoch,request_hash,playlist_sha256 FROM owned_livestream_archive_imports WHERE id=$1::uuid FOR UPDATE NOWAIT`, claim.ID).Scan(&state, &lease, &session, &version, &epoch, &request, &playlistSHA)
	if e != nil {
		return e
	}
	if state == "complete" {
		return tx.Commit()
	}
	if state != "processing" || lease != claim.Lease || session != claim.Session || version != claim.Version || epoch != claim.Epoch || playlistSHA != claim.PlaylistSHA || !bytes.Equal(request, archiveRequestHash(v, playlistSHA)) {
		return live.ErrConflict
	}
	if e = r.archiveOwnerCurrent(ctx, tx, v, stream); e != nil {
		return e
	}
	ok, e := r.eligible(ctx, tx, claim.Actor, session, version, epoch, archiveImportPermission)
	if e != nil || !ok {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	if ok, e = r.eligible(ctx, tx, claim.Actor, session, version, epoch, media.CreatePermission); e != nil || !ok {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	if ok, e = r.publishers.CanEditPublisherInTransaction(ctx, tx, claim.Actor, stream.PublisherID); e != nil || !ok {
		if e != nil {
			return e
		}
		return live.ErrForbidden
	}
	// The adapter leaves a private pending object and a durable source receipt.
	// Promote it only inside this final authority/lease transaction so revoked
	// work never becomes owner-readable or permanently charged.
	var mediaSize int64
	e = tx.QueryRowContext(ctx, `SELECT media.size FROM owned_livestream_archive_media receipt JOIN owned_media_files media ON media.id=receipt.media_id JOIN owned_media_quotas quota ON quota.account_id=media.account_id WHERE receipt.job_id=$1::uuid AND receipt.media_id=$2::uuid AND receipt.actor_id=$3::uuid AND receipt.source_sha256=$4::text AND media.account_id=receipt.actor_id AND media.state='pending' AND media.mime_type='video/mp4' AND NOT media.is_public AND media.charge='reserved' AND media.expires_at>clock_timestamp() AND media.idempotency_key='livestream-archive:'||$1::text FOR UPDATE OF receipt,media,quota`, claim.ID, receipt.MediaID, claim.Actor, receipt.SourceSHA256).Scan(&mediaSize)
	if errors.Is(e, sql.ErrNoRows) {
		return live.ErrProvider
	}
	if e != nil {
		return e
	}
	at, e := now(ctx, tx)
	if e != nil {
		return e
	}
	result, e := tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes-$2::bigint,used_bytes=used_bytes+$2::bigint WHERE account_id=$1::uuid AND reserved_bytes>=$2::bigint`, claim.Actor, mediaSize)
	if e != nil {
		return e
	}
	if n, x := result.RowsAffected(); x != nil || n != 1 {
		if x != nil {
			return x
		}
		return live.ErrProvider
	}
	result, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET state='ready',charge='used',updated_at=$3::timestamptz WHERE id=$1::uuid AND account_id=$2::uuid AND state='pending' AND charge='reserved'`, receipt.MediaID, claim.Actor, at)
	if e != nil {
		return e
	}
	if n, x := result.RowsAffected(); x != nil || n != 1 {
		if x != nil {
			return x
		}
		return live.ErrProvider
	}
	result, e = tx.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state='complete',media_id=$2::uuid,source_sha256=$3::text,reason='',updated_at=$4::timestamptz,lease_token=NULL,lease_expires_at=NULL WHERE id=$1::uuid AND state='processing' AND lease_token=$5::uuid`, claim.ID, receipt.MediaID, receipt.SourceSHA256, at, claim.Lease)
	if e != nil {
		return e
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return e
		}
		return live.ErrConflict
	}
	if e = audit(ctx, tx, claim.Stream, claim.Actor, "livestreams.hls.import.completed"); e != nil {
		return e
	}
	return tx.Commit()
}

func (r *Repository) releaseArchiveLease(ctx context.Context, claim archiveClaim) error {
	_, e := r.db.ExecContext(ctx, `UPDATE owned_livestream_archive_imports SET state=CASE WHEN attempts+1>=30 THEN 'failed' ELSE 'pending' END,attempts=LEAST(attempts+1,30),reason='import_pending',updated_at=clock_timestamp(),next_attempt_at=clock_timestamp()+LEAST(3600,1<<LEAST(attempts,12))*interval '1 second',lease_token=NULL,lease_expires_at=NULL WHERE id=$1::uuid AND state='processing' AND lease_token=$2::uuid`, claim.ID, claim.Lease)
	return e
}

func (r *Repository) processArchiveImport(ctx context.Context, id string) error {
	importer, e := r.archiveImporter()
	if e != nil {
		return e
	}
	claim, e := r.claimArchiveImport(ctx, id)
	if e != nil {
		return e
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Minute)
	receipt, e := importer.ImportHLSArchive(op, live.ArchiveImportSource{JobID: claim.ID, LeaseToken: claim.Lease, ActorID: claim.Actor, StreamID: claim.Stream, OutputID: claim.Output, Namespace: claim.Namespace, Playlist: claim.Playlist, PlaylistSHA256: claim.PlaylistSHA})
	cancel()
	if e != nil {
		x := r.releaseArchiveLease(context.WithoutCancel(ctx), claim)
		return errors.Join(e, x)
	}
	if e = r.finalizeArchiveImport(ctx, claim, receipt); e != nil {
		x := r.releaseArchiveLease(context.WithoutCancel(ctx), claim)
		return errors.Join(e, x)
	}
	return nil
}

func (r *Repository) ProcessArchiveImportsDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, live.ErrInvalid
	}
	if _, e := r.archiveImporter(); e != nil {
		return 0, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT id::text FROM owned_livestream_archive_imports WHERE (state='pending' AND next_attempt_at<=clock_timestamp())OR(state='processing' AND lease_expires_at<=clock_timestamp()) ORDER BY COALESCE(lease_expires_at,next_attempt_at),id LIMIT $1::integer`, limit)
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
		if errors.Is(e, errArchiveNoWork) {
			continue
		}
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "55P03" {
			continue
		}
		result = errors.Join(result, e)
	}
	return n, result
}
