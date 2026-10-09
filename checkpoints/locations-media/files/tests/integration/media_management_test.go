// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"strings"
	"testing"
	"time"
)

func TestPGMediaFoldersMetadataAtomicBatchAndDedup(t *testing.T) {
	f := newMediaFixture(t)
	folder, e := f.repo.CreateFolder(f.ctx, f.actor, "private folder", nil)
	if e != nil {
		t.Fatal(e)
	}
	child, e := f.repo.CreateFolder(f.ctx, f.actor, "child", &folder.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.PatchFile(f.ctx, f.actor, folder.ID, media.FilePatch{ParentPresent: true, ParentID: &child.ID}); !errors.Is(e, media.ErrConflict) {
		t.Fatal("hierarchy cycle", e)
	}
	u := mediaUpload("first", "hello", false)
	u.Options.ParentID = child.ID
	first, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello"))
	if e != nil {
		t.Fatal(e)
	}
	u.IdempotencyKey = "second"
	second, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("hello"))
	if e != nil {
		t.Fatal(e)
	}
	refs, e := f.repo.FileReferences(f.ctx, f.actor, first.ID)
	if e != nil || len(refs) != 2 {
		t.Fatal("physical dedup refs", refs, e)
	}
	mediaUsage(t, f, 10, 0)
	meta := map[string]json.RawMessage{"caption": json.RawMessage(`"synthetic"`)}
	updated, e := f.repo.PatchFile(f.ctx, f.actor, first.ID, media.FilePatch{Name: postString("renamed.txt"), Meta: meta, Sensitive: []string{"spoiler"}})
	if e != nil || updated.Name != "renamed.txt" || string(updated.Meta["caption"]) != `"synthetic"` || updated.ParentID != child.ID {
		t.Fatal(updated, e)
	}
	listed, n, e := f.repo.ListFiles(f.ctx, f.actor, media.FileQuery{ParentID: child.ID, Take: 50, Order: "date"})
	if e != nil || n != 2 || len(listed) != 2 {
		t.Fatal(listed, n, e)
	}
	listed, n, e = f.repo.ListFiles(f.ctx, f.actor, media.FileQuery{All: true, Take: 50, Order: "date"})
	if e != nil || n != 4 || len(listed) != 4 {
		t.Fatal("owned listing omitted nested files", listed, n, e)
	}
	if _, e = f.repo.FileRecord(f.otherCtx, f.other, first.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("foreign metadata", e)
	}
	if _, e = f.repo.DeleteFiles(f.ctx, f.actor, []string{child.ID}); !errors.Is(e, media.ErrConflict) {
		t.Fatal("nonempty folder deletion", e)
	}
	foreign, _, e := f.repo.Upload(f.otherCtx, f.other, mediaUpload("foreign", "foreign", false), strings.NewReader("foreign"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.DeleteFiles(f.ctx, f.actor, []string{second.ID, foreign.ID}); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("mixed-owner batch", e)
	}
	if _, e = f.repo.Info(f.ctx, f.actor, second.ID); e != nil {
		t.Fatal("partially committed forbidden batch", e)
	}
	if _, e = f.repo.DeleteFiles(f.ctx, f.actor, []string{first.ID}); e != nil {
		t.Fatal(e)
	}
	_, body, e := f.repo.Open(f.ctx, f.actor, second.ID)
	if e != nil {
		t.Fatal("shared bytes deleted with first reference", e)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != "hello" {
		t.Fatal(string(got))
	}
	mediaUsage(t, f, 5, 0)
	if _, e = f.repo.DeleteFiles(f.ctx, f.actor, []string{second.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.DeleteFiles(f.ctx, f.actor, []string{child.ID}); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f, 0, 0)
}

func TestPGMediaOwnerScopedDeletionRetryIsolation(t *testing.T) {
	f := newMediaFixture(t)
	store := &mediaFailingStore{ObjectStore: f.store, failDelete: true}
	repo, e := mediapg.New(f.db, store, f.guard, mediapg.Config{MaxFileBytes: 1 << 20, PendingTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	own, _, e := repo.Upload(f.ctx, f.actor, mediaUpload("own-retry", "own", false), strings.NewReader("own"))
	if e != nil {
		t.Fatal(e)
	}
	other, _, e := repo.Upload(f.otherCtx, f.other, mediaUpload("other-retry", "other", false), strings.NewReader("other"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.Delete(f.ctx, f.actor, own.ID); e == nil {
		t.Fatal("own durable delete failure hidden")
	}
	if _, e = repo.Delete(f.otherCtx, f.other, other.ID); e == nil {
		t.Fatal("other durable delete failure hidden")
	}
	store.failDelete = false
	if n, e := repo.CleanupOwnedDeletions(f.ctx, f.actor, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	mediaUsage(t, f, 0, 0)
	var state string
	var used int64
	if e = f.db.QueryRow(`SELECT f.state,q.used_bytes FROM owned_media_files f JOIN owned_media_quotas q ON q.account_id=f.account_id WHERE f.id=$1`, other.ID).Scan(&state, &used); e != nil || state != "deleting" || used != 5 {
		t.Fatal("foreign queue drained", state, used, e)
	}
	if n, e := repo.CleanupOwnedDeletions(f.otherCtx, f.other, 100); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

type syntheticRemoteMedia struct{ body string }

type syntheticRasterRenderer struct{ png []byte }

func (r syntheticRasterRenderer) RenderPNG(_ context.Context, mime string, b []byte) ([]byte, error) {
	if mime != "video/mp4" || string(b) != "synthetic video provider contract" {
		return nil, media.ErrInvalid
	}
	return r.png, nil
}

func TestPGMediaLivePhotoCompositionOwnershipAudienceAndCleanup(t *testing.T) {
	f := newMediaFixture(t)
	var pixels bytes.Buffer
	if e := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 1, 1))); e != nil {
		t.Fatal(e)
	}
	u := mediaUpload("composition-image", pixels.String(), false)
	u.MIMEType = "image/png"
	picture, _, e := f.repo.Upload(f.ctx, f.actor, u, bytes.NewReader(pixels.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	u = mediaUpload("composition-video", "synthetic video provider contract", false)
	u.MIMEType = "video/mp4"
	video, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("synthetic video provider contract"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.ComposeLivePhoto(f.ctx, f.actor, media.LivePhotoRequest{ImageID: picture.ID, VideoID: video.ID}); !errors.Is(e, media.ErrUnavailable) {
		t.Fatal("unconfigured processor claimed valid video", e)
	}
	f.repo.WithRenderer(syntheticRasterRenderer{pixels.Bytes()})
	pair, e := f.repo.ComposeLivePhoto(f.ctx, f.actor, media.LivePhotoRequest{ImageID: picture.ID, VideoID: video.ID})
	if e != nil || pair.ApplicationType != "live_photo" {
		t.Fatal(pair, e)
	}
	duplicate, e := f.repo.ComposeLivePhoto(f.ctx, f.actor, media.LivePhotoRequest{ImageID: picture.ID, VideoID: video.ID})
	if e != nil || duplicate.ID != pair.ID {
		t.Fatal("non-idempotent composition", duplicate, e)
	}
	if _, e = f.repo.LivePhoto(f.otherCtx, f.other, pair.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("private component granted by composition", e)
	}
	if _, e = f.repo.ComposeLivePhoto(f.otherCtx, f.other, media.LivePhotoRequest{ImageID: picture.ID, VideoID: video.ID}); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("foreign attachment ownership", e)
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, picture.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.LivePhoto(f.ctx, f.actor, pair.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("deleted component retained pair visibility", e)
	}
	if _, body, e := f.repo.Open(f.ctx, f.actor, video.ID); e != nil {
		t.Fatal("composition cleanup deleted independent video", e)
	} else {
		body.Close()
	}
	if e = f.repo.DeleteLivePhoto(f.ctx, f.actor, pair.ID); e != nil {
		t.Fatal(e)
	}
	mediaUsage(t, f, video.Size, 0)
}

func (s syntheticRemoteMedia) Fetch(context.Context, string, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.body)), nil
}
func TestPGMediaEncryptedExpiryPoolsRemoteAndThumbnail(t *testing.T) {
	f := newMediaFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.ctx = mediaProof(t, f, f.token)
	poolID := authorityUUID(t)
	if e := f.repo.WithPools(map[string]media.ObjectStore{poolID: f.store}); e != nil {
		t.Fatal(e)
	}
	pool, e := f.repo.CreatePool(f.ctx, f.actor, media.Pool{ID: poolID, Name: "synthetic configured local pool", AllowEncryption: true, MaxFileBytes: 1 << 20})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Pools(f.otherCtx, f.other); e != nil {
		t.Fatal(e)
	}
	opts := media.Options{PoolID: pool.ID, EncryptionScheme: "file.aesgcm.v1", EncryptionHeader: base64.StdEncoding.EncodeToString(make([]byte, 16)), EncryptionSignature: base64.StdEncoding.EncodeToString(make([]byte, 64))}
	u := mediaUpload("cipher", "opaque ciphertext", false)
	u.MIMEType = "application/octet-stream"
	u.Options = opts
	cipher, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("opaque ciphertext"))
	if e != nil {
		t.Fatal(e)
	}
	record, e := f.repo.FileRecord(f.ctx, f.actor, cipher.ID)
	if e != nil || record.EncryptionHeader != opts.EncryptionHeader || record.PoolID != pool.ID {
		t.Fatal(record, e)
	}
	if _, _, e = f.repo.Thumbnail(f.ctx, f.actor, cipher.ID); !errors.Is(e, media.ErrUnsupported) {
		t.Fatal("ciphertext processed as image", e)
	}
	f.repo.WithRemoteFetcher(syntheticRemoteMedia{"remote bytes"})
	remote, _, e := f.repo.UploadRemote(f.ctx, f.actor, media.RemoteUploadRequest{URL: "https://synthetic.invalid/media", Name: "remote.txt", ContentType: "text/plain", Size: 12, SHA256: mediaDigest("remote bytes"), IdempotencyKey: "remote"})
	if e != nil || remote.Size != 12 {
		t.Fatal(remote, e)
	}
	var pngBytes bytes.Buffer
	if e = png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 4, 512))); e != nil {
		t.Fatal(e)
	}
	imageBody := pngBytes.String()
	u = mediaUpload("thumbnail", imageBody, false)
	u.Name = "image.png"
	u.MIMEType = "image/png"
	pic, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader(imageBody))
	if e != nil {
		t.Fatal(e)
	}
	thumb, body, e := f.repo.Thumbnail(f.ctx, f.actor, pic.ID)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _, e := image.DecodeConfig(body)
	body.Close()
	if e != nil || thumb.MIMEType != "image/png" || cfg.Width != 2 || cfg.Height != 256 {
		t.Fatal(thumb, cfg, e)
	}
	if _, _, e = f.repo.Thumbnail(f.otherCtx, f.other, pic.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("foreign private thumbnail", e)
	}
	expiry := time.Now().Add(time.Minute)
	u = mediaUpload("expiry", "expires", false)
	u.Options.ExpiredAt = &expiry
	exp, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("expires"))
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_details SET expired_at=clock_timestamp()-interval '1 second' WHERE file_id=$1`, exp.ID)
	if _, e = f.repo.Info(f.ctx, f.actor, exp.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("expired bytes exposed", e)
	}
	if n, e := f.repo.Cleanup(context.Background(), 100); e != nil || n != 1 {
		t.Fatal("ready expiry cleanup", n, e)
	}
}
func TestPGMediaResumableStatsDetailsCleanupAndBundleProtection(t *testing.T) {
	f := newMediaFixture(t)
	task := createResumable(t, f, "hello", "statistics", 3)
	uploadResumableChunk(t, f, task.TaskID, 0, "hel")
	stats, e := f.repo.UploadStatistics(f.ctx, f.actor)
	if e != nil || stats.TotalTasks != 1 || stats.InProgressTasks != 1 || stats.AverageProgress != 50 {
		t.Fatal(stats, e)
	}
	details, e := f.repo.UploadTaskDetails(f.ctx, f.actor, task.TaskID)
	if e != nil || details.UploadedBytes != 3 || details.Task.ChunksUploaded != 1 {
		t.Fatal(details, e)
	}
	if _, e = f.repo.UploadTaskDetails(f.otherCtx, f.other, task.TaskID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("foreign upload details", e)
	}
	query(t, f.db, `UPDATE owned_media_files SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, task.TaskID)
	if n, e := f.repo.CleanupUploadTasks(f.ctx, f.actor); e != nil || n != 1 {
		t.Fatal("failed/expired cleanup", n, e)
	}
	mediaUsage(t, f, 0, 0)
	pass := "synthetic passcode"
	bundle, e := f.repo.SaveBundle(f.ctx, f.actor, "", media.BundleRequest{Name: "private by default", Passcode: &pass, Public: true})
	if e != nil {
		t.Fatal(e)
	}
	u := mediaUpload("bundle", "protected", true)
	u.Options.BundleID = bundle.ID
	file, _, e := f.repo.Upload(f.ctx, f.actor, u, strings.NewReader("protected"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Info(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrForbidden) {
		t.Fatal("public flag bypassed bundle passcode", e)
	}
	shared := media.WithBundlePasscode(f.otherCtx, pass)
	if _, e = f.repo.Info(shared, f.other, file.ID); e != nil {
		t.Fatal("valid protected share", e)
	}
	for i := 0; i < 8; i++ {
		if _, e = f.repo.Info(f.otherCtx, f.other, file.ID); !errors.Is(e, media.ErrForbidden) {
			t.Fatal("invalid share secret accepted", i, e)
		}
	}
	if _, e = f.repo.Info(shared, f.other, file.ID); !errors.Is(e, media.ErrRateLimit) {
		t.Fatal("failed password attempts were rolled back", e)
	}
	query(t, f.db, `UPDATE owned_media_bundle_attempts SET window_start=clock_timestamp()-interval '2 minutes' WHERE account_id=$1 AND bundle_id=$2`, f.other, bundle.ID)
	if _, e = f.repo.Info(shared, f.other, file.ID); e != nil {
		t.Fatal("password attempt window did not reset", e)
	}
	if e = f.repo.DeleteBundle(f.ctx, f.actor, bundle.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.Info(shared, f.other, file.ID); !errors.Is(e, media.ErrNotFound) {
		t.Fatal("deleted bundle retained read grant", e)
	}
}

func TestPGMediaQuotaRecordsExpiryAndIdempotentGrant(t *testing.T) {
	f := newMediaFixture(t)
	if e := authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "*"}}, nil); e != nil {
		t.Fatal(e)
	}
	f.ctx = mediaProof(t, f, f.token)
	query(t, f.db, `UPDATE owned_media_quotas SET limit_bytes=5 WHERE account_id=$1`, f.actor)
	expiry := time.Now().Add(time.Minute)
	grant := media.QuotaRecord{AccountID: f.actor, SourceKey: "synthetic-grant", Name: "explicit synthetic quota", QuotaBytes: 10, ExpiredAt: &expiry}
	first, e := f.repo.AddQuotaRecord(f.ctx, f.actor, grant)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.repo.AddQuotaRecord(f.ctx, f.actor, grant)
	if e != nil || second.ID != first.ID {
		t.Fatal("non-idempotent quota grant", second, e)
	}
	grant.QuotaBytes = 11
	if _, e = f.repo.AddQuotaRecord(f.ctx, f.actor, grant); !errors.Is(e, media.ErrConflict) {
		t.Fatal("changed source replay", e)
	}
	q, e := f.repo.Quota(f.ctx, f.actor)
	if e != nil || q.LimitBytes != 15 {
		t.Fatal(q, e)
	}
	file, _, e := f.repo.Upload(f.ctx, f.actor, mediaUpload("bonus-bytes", "0123456789", false), strings.NewReader("0123456789"))
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_media_quota_records SET expired_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID)
	if _, _, e = f.repo.Upload(f.ctx, f.actor, mediaUpload("past-bonus", "x", false), strings.NewReader("x")); !errors.Is(e, media.ErrQuota) {
		t.Fatal("expired bonus reused", e)
	}
	records, n, e := f.repo.QuotaRecords(f.ctx, f.actor, false, 20, 0)
	if e != nil || n != 0 || len(records) != 0 {
		t.Fatal(records, n, e)
	}
	records, n, e = f.repo.QuotaRecords(f.ctx, f.actor, true, 20, 0)
	if e != nil || n != 1 || len(records) != 1 {
		t.Fatal(records, n, e)
	}
	if _, e = f.repo.Delete(f.ctx, f.actor, file.ID); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.repo.Upload(f.ctx, f.actor, mediaUpload("base-after-cleanup", "x", false), strings.NewReader("x")); e != nil {
		t.Fatal("base quota not released", e)
	}
	usage, e := f.repo.StorageUsage(f.ctx, f.actor, "")
	if e != nil || usage.TotalUsageBytes != 1 || usage.TotalQuota != 5 || usage.ReservedQuota != 0 {
		t.Fatal(usage, e)
	}
}
