// SPDX-License-Identifier: AGPL-3.0-only
package mediapg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type ArchiveImportConfig struct {
	FFmpeg, Limiter                   string
	MaxSegments                       int
	MaxPlaylistBytes, MaxSegmentBytes int64
	MaxInputBytes, MaxOutputBytes     int64
	Timeout                           time.Duration
}

type archiveImporter struct {
	repo      *Repository
	artifacts live.ArtifactStore
	cfg       ArchiveImportConfig
	slots     chan struct{}
}

func NewArchiveImporter(repo *Repository, artifacts live.ArtifactStore, cfg ArchiveImportConfig) (live.ArchiveImporter, error) {
	if repo == nil || repo.db == nil || repo.store == nil || artifacts == nil || runtime.GOOS != "linux" || cfg.MaxSegments < 1 || cfg.MaxSegments > 2000 || cfg.MaxPlaylistBytes < 1024 || cfg.MaxPlaylistBytes > 256<<10 || cfg.MaxSegmentBytes < 188 || cfg.MaxSegmentBytes > 32<<20 || cfg.MaxInputBytes < cfg.MaxSegmentBytes || cfg.MaxInputBytes > 2<<30 || cfg.MaxOutputBytes < 1024 || cfg.MaxOutputBytes > repo.cfg.MaxFileBytes || cfg.Timeout < 5*time.Second || cfg.Timeout > 5*time.Minute {
		return nil, media.ErrInvalid
	}
	for _, name := range []string{cfg.FFmpeg, cfg.Limiter} {
		if !filepath.IsAbs(name) || filepath.Clean(name) != name {
			return nil, media.ErrInvalid
		}
		real, e := filepath.EvalSymlinks(name)
		if e != nil || real != name {
			return nil, media.ErrInvalid
		}
		info, e := os.Stat(name)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
			return nil, media.ErrInvalid
		}
	}
	return &archiveImporter{repo: repo, artifacts: artifacts, cfg: cfg, slots: make(chan struct{}, 2)}, nil
}

func (*archiveImporter) ArchiveImportContract() string { return live.ArchiveImportContract }
func (a *archiveImporter) ArchiveImportReady(ctx context.Context) error {
	if a == nil || a.repo == nil {
		return media.ErrUnavailable
	}
	if e := a.repo.Ready(ctx); e != nil {
		return e
	}
	if _, e := a.repo.db.ExecContext(ctx, `SELECT job_id,media_id,source_sha256 FROM owned_livestream_archive_media LIMIT 0`); e != nil {
		return e
	}
	for _, name := range []string{a.cfg.FFmpeg, a.cfg.Limiter} {
		info, e := os.Stat(name)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
			return media.ErrUnavailable
		}
	}
	return nil
}

func archiveRead(ctx context.Context, store live.ArtifactStore, namespace, output, name string, limit int64) ([]byte, error) {
	body, size, _, e := store.OpenStreamingArtifact(ctx, namespace, output, name, limit)
	if e != nil {
		return nil, e
	}
	defer body.Close()
	if size < 1 || size > limit {
		return nil, media.ErrTooLarge
	}
	data, e := io.ReadAll(io.LimitReader(body, limit+1))
	if e != nil || int64(len(data)) != size || int64(len(data)) > limit {
		return nil, media.ErrInvalid
	}
	return data, ctx.Err()
}

func archiveAssets(playlist []byte, allowed map[string]bool) ([]string, error) {
	assets := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(playlist), "\r\n", "\n"), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if !allowed[line] || !live.ValidOutputAsset(line) || !strings.HasSuffix(line, ".ts") {
			return nil, live.ErrPlaylist
		}
		assets = append(assets, line)
	}
	if len(assets) == 0 {
		return nil, live.ErrPlaylist
	}
	return assets, nil
}

func writePrivate(path string, data []byte) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	if x := f.Close(); e == nil {
		e = x
	}
	return e
}

func (a *archiveImporter) transcode(ctx context.Context, source live.ArchiveImportSource) (output, sourceHash string, size int64, e error) {
	playlist, e := archiveRead(ctx, a.artifacts, source.Namespace, source.OutputID, source.Playlist, a.cfg.MaxPlaylistBytes)
	if e != nil {
		return "", "", 0, e
	}
	playlistDigest := sha256.Sum256(playlist)
	if hex.EncodeToString(playlistDigest[:]) != source.PlaylistSHA256 {
		return "", "", 0, live.ErrConflict
	}
	_, allowed, e := live.ProjectHLS(playlist, source.StreamID, source.OutputID, a.cfg.MaxSegments)
	if e != nil {
		return "", "", 0, e
	}
	assets, e := archiveAssets(playlist, allowed)
	if e != nil {
		return "", "", 0, e
	}
	dir, e := os.MkdirTemp("", "solsynth-hls-archive-")
	if e != nil {
		return "", "", 0, e
	}
	defer func() {
		if e != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if e = os.Chmod(dir, 0700); e != nil {
		return "", "", 0, e
	}
	if e = writePrivate(filepath.Join(dir, source.Playlist), playlist); e != nil {
		return "", "", 0, e
	}
	h := sha256.New()
	h.Write(playlistDigest[:])
	total := int64(len(playlist))
	for _, name := range assets {
		data, x := archiveRead(ctx, a.artifacts, source.Namespace, source.OutputID, name, a.cfg.MaxSegmentBytes)
		if x != nil {
			return "", "", 0, x
		}
		total += int64(len(data))
		if total > a.cfg.MaxInputBytes || len(data) < 188 || len(data)%188 != 0 {
			return "", "", 0, media.ErrTooLarge
		}
		for i := 0; i < len(data); i += 188 {
			if data[i] != 0x47 {
				return "", "", 0, live.ErrPlaylist
			}
		}
		digest := sha256.Sum256(data)
		h.Write([]byte{0})
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(digest[:])
		if x = writePrivate(filepath.Join(dir, name), data); x != nil {
			return "", "", 0, x
		}
	}
	sourceHash = hex.EncodeToString(h.Sum(nil))
	output = filepath.Join(dir, "archive.mp4")
	created, e := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", "", 0, e
	}
	if e = created.Close(); e != nil {
		return "", "", 0, e
	}
	seconds := int(a.cfg.Timeout/time.Second) + 1
	args := []string{"--as=1073741824", "--cpu=" + stringInt(seconds), "--fsize=" + stringInt64(a.cfg.MaxOutputBytes), "--", a.cfg.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-max_alloc", "67108864", "-threads", "1", "-protocol_whitelist", "file", "-format_whitelist", "hls,mpegts,mov", "-codec_whitelist", "h264,hevc,aac,mp3", "-f", "hls", "-i", source.Playlist, "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-c", "copy", "-movflags", "+faststart", "-f", "mp4", "archive.mp4"}
	op, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(op, a.cfg.Limiter, args...)
	cmd.Dir = dir
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stdin = bytes.NewReader(nil)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Run(); e != nil {
		return "", "", 0, media.ErrInvalid
	}
	info, e := os.Lstat(output)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > a.cfg.MaxOutputBytes {
		return "", "", 0, media.ErrInvalid
	}
	return output, sourceHash, info.Size(), nil
}

func stringInt(v int) string { return stringInt64(int64(v)) }
func stringInt64(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [32]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func archiveSourceValid(v live.ArchiveImportSource) bool {
	return authority.ValidID(v.JobID) && authority.ValidID(v.LeaseToken) && authority.ValidID(v.ActorID) && authority.ValidID(v.StreamID) && authority.ValidID(v.OutputID) && live.ValidOutputDestination(v.Namespace) && live.ValidOutputAsset(v.Playlist) && strings.HasSuffix(v.Playlist, ".m3u8") && len(v.PlaylistSHA256) == 64
}

func archivePgConflict(e error) bool {
	var pg *pgconn.PgError
	return errors.As(e, &pg) && (pg.Code == "55P03" || pg.Code == "40P01")
}

func (a *archiveImporter) lockArchiveJob(ctx context.Context, tx *sql.Tx, source live.ArchiveImportSource) error {
	var actor, stream, output, state, lease string
	e := tx.QueryRowContext(ctx, `SELECT actor_id::text,stream_id::text,output_id::text,state,COALESCE(lease_token::text,'') FROM owned_livestream_archive_imports WHERE id=$1::uuid FOR UPDATE NOWAIT`, source.JobID).Scan(&actor, &stream, &output, &state, &lease)
	if archivePgConflict(e) {
		return live.ErrConflict
	}
	if e != nil {
		return e
	}
	if actor != source.ActorID || stream != source.StreamID || output != source.OutputID || state != "processing" || lease != source.LeaseToken {
		return live.ErrConflict
	}
	return nil
}

func (a *archiveImporter) prepareMedia(ctx context.Context, source live.ArchiveImportSource, sourceHash, outputHash string, size int64) (media.File, bool, error) {
	var out media.File
	tx, e := a.repo.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return out, false, e
	}
	defer tx.Rollback()
	var active bool
	e = tx.QueryRowContext(ctx, `SELECT NOT a.disabled AND NOT EXISTS(SELECT 1 FROM owned_authority_punishments p WHERE p.account_id=a.id AND p.kind='disable_account' AND(p.starts_at IS NULL OR p.starts_at<=clock_timestamp())AND(p.expires_at IS NULL OR p.expires_at>clock_timestamp())) FROM owned_authority_accounts a WHERE a.id=$1::uuid FOR UPDATE`, source.ActorID).Scan(&active)
	if e != nil || !active {
		if e == nil {
			e = media.ErrForbidden
		}
		return out, false, e
	}
	var limit, used, reserved int64
	var extended bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='owned_media_quotas' AND column_name='base_limit_bytes')`).Scan(&extended); e != nil {
		return out, false, e
	}
	quotaQuery := `SELECT limit_bytes,used_bytes,reserved_bytes FROM owned_media_quotas q WHERE q.account_id=$1::uuid FOR UPDATE`
	if extended {
		quotaQuery = `SELECT COALESCE(q.base_limit_bytes,q.limit_bytes)+COALESCE((SELECT sum(quota_bytes) FROM owned_media_quota_records b WHERE b.account_id=q.account_id AND(b.expired_at IS NULL OR b.expired_at>clock_timestamp())),0),used_bytes,reserved_bytes FROM owned_media_quotas q WHERE q.account_id=$1::uuid FOR UPDATE OF q`
	}
	e = tx.QueryRowContext(ctx, quotaQuery, source.ActorID).Scan(&limit, &used, &reserved)
	if e != nil {
		return out, false, e
	}
	if e = a.lockArchiveJob(ctx, tx, source); e != nil {
		return out, false, e
	}
	var details bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='owned_media_details' AND c.relkind IN('r','p'))`).Scan(&details); e != nil {
		return out, false, e
	}
	var storedSource, charge string
	e = tx.QueryRowContext(ctx, `SELECT `+fields+`,r.source_sha256,f.charge FROM owned_livestream_archive_media r JOIN owned_media_files f ON f.id=r.media_id WHERE r.job_id=$1::uuid FOR UPDATE OF r,f`, source.JobID).Scan(&out.ID, &out.AccountID, &out.Name, &out.MIMEType, &out.Size, &out.SHA256, &out.Public, &out.State, &out.CreatedAt, &out.UpdatedAt, &out.Usage, &storedSource, &charge)
	if e == nil {
		if storedSource != sourceHash || out.AccountID != source.ActorID || out.MIMEType != "video/mp4" || out.Public {
			return media.File{}, false, media.ErrConflict
		}
		if out.State == "pending" && charge == "reserved" {
			if out.Size != size || out.SHA256 != outputHash {
				return media.File{}, false, media.ErrConflict
			}
			if _, e = tx.ExecContext(ctx, `UPDATE owned_media_files SET expires_at=clock_timestamp()+($2::bigint*interval '1 microsecond'),updated_at=clock_timestamp() WHERE id=$1::uuid AND state='pending'`, out.ID, a.repo.cfg.PendingTTL.Microseconds()); e != nil {
				return media.File{}, false, e
			}
			return out, false, tx.Commit()
		}
		if out.State != "deleted" || charge != "none" {
			return media.File{}, false, media.ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_livestream_archive_media WHERE job_id=$1::uuid AND media_id=$2::uuid`, source.JobID, out.ID); e != nil {
			return media.File{}, false, e
		}
		if details {
			if _, e = tx.ExecContext(ctx, `DELETE FROM public.owned_media_details WHERE file_id=$1::uuid`, out.ID); e != nil {
				return media.File{}, false, e
			}
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM owned_media_files WHERE id=$1::uuid AND state='deleted' AND charge='none'`, out.ID); e != nil {
			return media.File{}, false, e
		}
		e = sql.ErrNoRows
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, false, e
	}
	if size > limit-used-reserved {
		return out, false, media.ErrQuota
	}
	id, e := uuid()
	if e != nil {
		return out, false, e
	}
	name := "livestream-" + source.OutputID + ".mp4"
	out, e = scan(tx.QueryRowContext(ctx, `INSERT INTO owned_media_files(id,account_id,idempotency_key,name,mime_type,size,sha256,is_public,state,charge,expires_at,created_at,updated_at,usage) VALUES($1::uuid,$2::uuid,$3::text,$4::text,'video/mp4',$5::bigint,$6::text,false,'pending','reserved',clock_timestamp()+($7::bigint*interval '1 microsecond'),clock_timestamp(),clock_timestamp(),'') RETURNING `+fields, id, source.ActorID, "livestream-archive:"+source.JobID, name, size, outputHash, a.repo.cfg.PendingTTL.Microseconds()))
	if e != nil {
		return out, false, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes+$2::bigint WHERE account_id=$1::uuid`, source.ActorID, size); e != nil {
		return out, false, e
	}
	if details {
		if _, e = tx.ExecContext(ctx, `INSERT INTO public.owned_media_details(file_id,options,options_sha256,object_key)VALUES($1::uuid,'{}'::jsonb,'44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',$1::uuid)`, out.ID); e != nil {
			return out, false, e
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO owned_livestream_archive_media(job_id,media_id,actor_id,source_sha256,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,clock_timestamp())`, source.JobID, out.ID, source.ActorID, sourceHash); e != nil {
		return out, false, e
	}
	return out, false, tx.Commit()
}

func objectMatches(ctx context.Context, store media.ObjectStore, id, digest string, size int64) (bool, error) {
	body, e := store.Open(ctx, id)
	if errors.Is(e, media.ErrNotFound) || errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer body.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(body, size+1))
	if e != nil {
		return false, e
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return false, media.ErrConflict
	}
	return true, nil
}

func (a *archiveImporter) ImportHLSArchive(ctx context.Context, source live.ArchiveImportSource) (live.ArchiveImportReceipt, error) {
	var receipt live.ArchiveImportReceipt
	if a == nil || !archiveSourceValid(source) {
		return receipt, media.ErrInvalid
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		return receipt, media.ErrUnavailable
	}
	output, sourceHash, size, e := a.transcode(ctx, source)
	if e != nil {
		return receipt, e
	}
	defer os.RemoveAll(filepath.Dir(output))
	body, e := os.Open(output)
	if e != nil {
		return receipt, e
	}
	h := sha256.New()
	_, e = io.Copy(h, body)
	body.Close()
	if e != nil {
		return receipt, e
	}
	outputHash := hex.EncodeToString(h.Sum(nil))
	f, _, e := a.prepareMedia(ctx, source, sourceHash, outputHash, size)
	if e != nil {
		return receipt, e
	}
	matched, x := objectMatches(ctx, a.repo.store, f.ID, f.SHA256, f.Size)
	if x != nil {
		return receipt, x
	}
	if !matched {
		body, x = os.Open(output)
		if x != nil {
			return receipt, x
		}
		x = a.repo.store.Put(ctx, f.ID, body, f.Size, f.SHA256)
		body.Close()
		if x != nil {
			return receipt, x
		}
	}
	return live.ArchiveImportReceipt{MediaID: f.ID, SourceSHA256: sourceHash}, nil
}

var _ live.ArchiveImporter = (*archiveImporter)(nil)
