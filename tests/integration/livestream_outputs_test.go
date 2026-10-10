// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/livestreampg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/social"
	live "solsynth.local/go-migration/ports/sphere/livestreams"
)

type streamOutputProvider struct {
	*streamProvider
	outMu                                        sync.Mutex
	outputs                                      map[string]live.ProviderOutput
	starts, stops                                int
	failBefore, failAfter, failStop, pendingStop bool
	policy                                       string
}

func (*streamOutputProvider) OutputContract() string { return live.EgressContract }
func (p *streamOutputProvider) OutputPolicyFingerprint(ids []string, kind string) ([]byte, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	if len(ids) != 1 || kind == "hls" && ids[0] != "synthetic" || kind == "egress" && ids[0] != "relay" && ids[0] != "archive" || kind != "hls" && kind != "egress" {
		return nil, live.ErrInvalid
	}
	h := sha256.Sum256([]byte(kind + ":" + ids[0] + ":" + p.policy))
	return h[:], nil
}
func (p *streamOutputProvider) OutputUsesRelay(ids []string, kind string) (bool, error) {
	if _, e := p.OutputPolicyFingerprint(ids, kind); e != nil {
		return false, e
	}
	return ids[0] == "relay", nil
}
func (p *streamOutputProvider) StartOutput(_ context.Context, v live.OutputStart) (live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.starts++
	if p.failBefore {
		return live.ProviderOutput{}, live.ErrProvider
	}
	out := live.ProviderOutput{ID: fmt.Sprintf("synthetic-output-%d", p.starts), Room: v.Room, Status: 1}
	if v.Kind == "hls" {
		out.Playlist = "streaming/" + v.Destinations[0] + "/" + v.ID + "/" + v.Playlist
	}
	p.outputs[out.ID] = out
	if p.failAfter {
		return live.ProviderOutput{}, live.ErrProvider
	}
	return out, nil
}
func (p *streamOutputProvider) ListOutputs(_ context.Context, room string) ([]live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	out := []live.ProviderOutput{}
	for _, v := range p.outputs {
		if v.Room == room {
			out = append(out, v)
		}
	}
	return out, nil
}
func (p *streamOutputProvider) StopOutput(_ context.Context, room, id string) (live.ProviderOutput, error) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.stops++
	if p.failStop {
		return live.ProviderOutput{}, live.ErrProvider
	}
	v, ok := p.outputs[id]
	if !ok {
		return live.ProviderOutput{ID: id, Room: room, Absent: true}, nil
	}
	if p.pendingStop {
		v.Status = 2
		p.outputs[id] = v
		return v, nil
	}
	delete(p.outputs, id)
	v.Status = 3
	return v, nil
}

type outputFixture struct {
	streamFixture
	out   *streamOutputProvider
	store *mediastore.Filesystem
	root  string
}

type archiveImporterFixture struct {
	mu        sync.Mutex
	db        *sql.DB
	contract  string
	mediaID   string
	jobs      map[string]string
	calls     int
	failAfter bool
	entered   chan struct{}
	release   chan struct{}
}

func (a *archiveImporterFixture) ArchiveImportContract() string {
	if a.contract != "" {
		return a.contract
	}
	return live.ArchiveImportContract
}
func (*archiveImporterFixture) ArchiveImportReady(context.Context) error { return nil }
func (a *archiveImporterFixture) ImportHLSArchive(ctx context.Context, source live.ArchiveImportSource) (live.ArchiveImportReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(source.PlaylistSHA256) != 64 {
		return live.ArchiveImportReceipt{}, live.ErrInvalid
	}
	a.calls++
	sourceHash := strings.Repeat("a", 64)
	if prior := a.jobs[source.JobID]; prior != "" {
		var state string
		e := a.db.QueryRowContext(ctx, `SELECT state FROM owned_media_files WHERE id=$1::uuid`, prior).Scan(&state)
		if e == nil && state == "pending" {
			return live.ArchiveImportReceipt{MediaID: prior, SourceSHA256: sourceHash}, nil
		}
		if e != nil || state != "deleted" {
			return live.ArchiveImportReceipt{}, live.ErrConflict
		}
		tx, x := a.db.BeginTx(ctx, nil)
		if x != nil {
			return live.ArchiveImportReceipt{}, x
		}
		defer tx.Rollback()
		if _, x = tx.ExecContext(ctx, `DELETE FROM owned_livestream_archive_media WHERE job_id=$1::uuid AND media_id=$2::uuid`, source.JobID, prior); x == nil {
			_, x = tx.ExecContext(ctx, `DELETE FROM owned_media_files WHERE id=$1::uuid AND state='deleted' AND charge='none'`, prior)
		}
		if x == nil {
			x = tx.Commit()
		}
		if x != nil {
			return live.ArchiveImportReceipt{}, x
		}
		delete(a.jobs, source.JobID)
	}
	a.jobs[source.JobID] = a.mediaID
	if a.db == nil {
		return live.ArchiveImportReceipt{}, live.ErrUnavailable
	}
	tx, e := a.db.BeginTx(ctx, nil)
	if e != nil {
		return live.ArchiveImportReceipt{}, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE owned_media_quotas SET reserved_bytes=reserved_bytes+1 WHERE account_id=$1::uuid`, source.ActorID); e == nil {
		_, e = tx.Exec(`INSERT INTO owned_media_files(id,account_id,idempotency_key,name,mime_type,size,sha256,is_public,state,charge,expires_at,created_at,updated_at,usage)VALUES($1::uuid,$2::uuid,'livestream-archive:'||$3::text,'synthetic-archive.mp4','video/mp4',1,$4::text,false,'pending','reserved',clock_timestamp()+interval '1 day',clock_timestamp(),clock_timestamp(),'')`, a.mediaID, source.ActorID, source.JobID, sourceHash)
	}
	if e == nil {
		_, e = tx.Exec(`INSERT INTO owned_livestream_archive_media(job_id,media_id,actor_id,source_sha256,created_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4::text,clock_timestamp())`, source.JobID, a.mediaID, source.ActorID, sourceHash)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		return live.ArchiveImportReceipt{}, e
	}
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return live.ArchiveImportReceipt{}, ctx.Err()
		}
	}
	if a.failAfter {
		a.failAfter = false
		return live.ArchiveImportReceipt{}, live.ErrProvider
	}
	return live.ArchiveImportReceipt{MediaID: a.mediaID, SourceSHA256: sourceHash}, nil
}

func outputConfig() livestreampg.OutputConfig {
	return livestreampg.OutputConfig{MaxOutputsPerStream: 20, MaxSegments: 10, MaxPlaylistBytes: 64 << 10, MaxSegmentBytes: 4 << 20, OperationTimeout: time.Second, HLSNamespaces: []string{"synthetic"}}
}
func newOutputFixture(t *testing.T) outputFixture {
	t.Helper()
	f := outputFixture{streamFixture: newStreamFixture(t)}
	query(t, f.db, livestreampg.OutputsSchema)
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.owner)
	p, e := f.verifier.Authenticate(f.ctx, f.token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	f.root = t.TempDir()
	if e = os.Chmod(f.root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(f.root, "streaming", "synthetic"), 0700); e != nil {
		t.Fatal(e)
	}
	f.store, e = mediastore.NewFilesystem(f.root, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.store.Close() })
	f.out = &streamOutputProvider{streamProvider: f.provider, outputs: map[string]live.ProviderOutput{}, policy: "synthetic-v1"}
	f.repo, e = livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.out, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.repo.WithOutputs(outputConfig(), f.store); e != nil {
		t.Fatal(e)
	}
	f.handler = (&live.Service{Repository: f.repo, Authority: f.verifier, Bind: f.guard.Bind}).Handler()
	if e = f.repo.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func outputStream(t *testing.T, f outputFixture, key string) live.Stream {
	t.Helper()
	s := streamCreate(t, f.streamFixture, key)
	streamNative(t, f.streamFixture, s.ID)
	return s
}
func hlsStart(t *testing.T, f outputFixture, id, key string) live.OutputResponse {
	t.Helper()
	v, e := f.repo.StartHLS(f.ownerCtx, f.owner, id, live.HLSRequest{Key: key, DestinationID: "synthetic"})
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func outputPhase(t *testing.T, f outputFixture, id string) string {
	t.Helper()
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_outputs WHERE id=$1::uuid`, id).Scan(&phase); e != nil {
		t.Fatal(e)
	}
	return phase
}
func outputDue(t *testing.T, f outputFixture, sid string) error {
	t.Helper()
	query(t, f.db, `UPDATE owned_livestream_outputs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid AND phase<>'done'`, sid)
	_, e := f.repo.ProcessOutputsDue(f.ctx, 100)
	return e
}
func outputFiles(t *testing.T, f outputFixture, id string) {
	t.Helper()
	dir := filepath.Join(f.root, "streaming", "synthetic", id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:6,\nsegment_0.ts\n"
	if e := os.WriteFile(filepath.Join(dir, "playlist.m3u8"), []byte(playlist), 0600); e != nil {
		t.Fatal(e)
	}
	segment := make([]byte, 188)
	segment[0] = 0x47
	if e := os.WriteFile(filepath.Join(dir, "segment_0.ts"), segment, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "unreferenced.ts"), segment, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestPGLiveStreamHLSHTTPPlaybackAndCurrentPrivacy(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "hls-http")
	base := "/api/livestreams/" + s.ID
	req := fixtureJSONRequest(t, "POST", base+"/hls", f.token, map[string]any{"destination_id": "synthetic"})
	req.Header.Set("Idempotency-Key", "synthetic-hls-http")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := backendDecode[live.OutputResponse](t, w)
	if v.EgressID == "" || v.State != "active" || v.PlaylistURL == nil {
		t.Fatal(v)
	}
	outputFiles(t, f, v.OutputID)
	w = streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.thirdToken, 200)
	if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || strings.Contains(w.Body.String(), "streaming/synthetic") || !strings.Contains(w.Body.String(), v.OutputID+"/segment_0.ts") {
		t.Fatal("unsafe playlist projection")
	}
	segment := base + "/hls/" + v.OutputID + "/segment_0.ts"
	w = streamGetCall(t, f.streamFixture, segment, f.thirdToken, 200)
	if w.Body.Len() != 188 || w.Header().Get("Content-Type") != "video/mp2t" {
		t.Fatal("segment framing")
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, "", 401)
	streamGetCall(t, f.streamFixture, base+"/hls/"+v.OutputID+"/unreferenced.ts", f.token, 404)
	if _, e := f.social.ChangeRelationship(f.viewerCtx, f.third, f.owner, social.RelationshipChange{Action: social.Block}); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, segment, f.thirdToken, 403)
	if _, e := f.repo.OpenHLS(f.ownerCtx, f.owner, s.ID, v.OutputID, "segment_0.ts"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(f.root, "streaming", "synthetic", v.OutputID, "playlist.m3u8"), []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://external.invalid/key\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.token, 503)
	if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.token, 404)
}

func TestPGLiveStreamOutputsRequireFixedDestinationsAndAdminEditor(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-policy")
	if _, e := f.repo.StartHLS(f.editorCtx, f.other, s.ID, live.HLSRequest{DestinationID: "synthetic"}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("non-admin editor started egress", e)
	}
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.third)
	p, e := f.verifier.Authenticate(f.ctx, f.thirdToken)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.StartHLS(f.guard.Bind(f.ctx, p), f.third, s.ID, live.HLSRequest{DestinationID: "synthetic"}); e == nil {
		t.Fatal("non-editor admin started egress")
	}
	path := "/api/livestreams/" + s.ID
	backendJSONCall(t, f.handler, "POST", path+"/egress", f.token, map[string]any{"rtmp_urls": []string{"rtmps://external.invalid/live/key"}}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/egress", f.token, map[string]any{"file_path": "/tmp/out.mp4"}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "playlist_name": "../bad.m3u8"}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "segment_count": 4}, 400)
	backendJSONCall(t, f.handler, "POST", path+"/hls", f.token, map[string]any{"destination_id": "synthetic", "hls_base_url": "https://external.invalid"}, 400)
	if f.out.starts != 0 {
		t.Fatal("rejected output reached provider")
	}
	private := streamCreate(t, f.streamFixture, "private-output")
	visibility := live.Private
	if _, e = f.repo.Update(f.ownerCtx, f.owner, private.ID, live.Patch{Visibility: &visibility}); e != nil {
		t.Fatal(e)
	}
	streamNative(t, f.streamFixture, private.ID)
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, private.ID, live.EgressRequest{DestinationIDs: []string{"relay"}}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("private stream relayed externally", e)
	}
	v := hlsStart(t, f, private.ID, "private-hls")
	outputFiles(t, f, v.OutputID)
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.otherToken, 200)
	streamGetCall(t, f.streamFixture, *v.PlaylistURL, f.thirdToken, 404)
	fixturePublisherShadowban(t, f.db, f.pub.ID, "other", time.Now().UTC())
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, s.ID, live.EgressRequest{DestinationIDs: []string{"relay"}}); !errors.Is(e, live.ErrForbidden) {
		t.Fatal("editor exception bypassed public relay visibility", e)
	}
}

func TestPGLiveStreamOutputsUncertainStartNeverRecreates(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "uncertain-hls")
	f.out.failAfter = true
	q := live.HLSRequest{Key: "uncertain", DestinationID: "synthetic"}
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, q); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, q); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal(e)
	}
	if f.out.starts != 1 {
		t.Fatal("uncertain HLS was reissued")
	}
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	v, e := f.repo.GetOutput(f.ownerCtx, f.owner, s.ID, "hls")
	if e != nil || v.State != "active" {
		t.Fatal(v, e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	r := outputStream(t, f, "uncertain-rtmp")
	request := live.EgressRequest{Key: "rtmp-uncertain", DestinationIDs: []string{"relay"}}
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, r.ID, request); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if e = outputDue(t, f, r.ID); e != nil {
		t.Fatal(e)
	}
	v, e = f.repo.GetOutput(f.ownerCtx, f.owner, r.ID, "egress")
	if e != nil || v.State != "uncertain" || v.EgressID != "" {
		t.Fatal("unproven RTMP association", v, e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, r.ID, "egress"); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal("unknown outcome falsely stopped", e)
	}
	if _, e = f.repo.StartEgress(f.ownerCtx, f.owner, r.ID, request); !errors.Is(e, live.ErrOutputUncertain) {
		t.Fatal(e)
	}
	if f.out.starts != 2 {
		t.Fatal("unknown RTMP was retried")
	}
}

func TestPGLiveStreamOutputsActivationRollbackRetainsCleanup(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-rollback")
	constraint := "synthetic_output_fail_" + strings.ReplaceAll(f.owner, "-", "")
	query(t, f.db, `ALTER TABLE owned_livestream_audit ADD CONSTRAINT `+constraint+` CHECK(NOT(actor_id='`+f.owner+`'::uuid AND action='livestreams.hls.observed'))NOT VALID`)
	t.Cleanup(func() { _, _ = f.db.Exec(`ALTER TABLE owned_livestream_audit DROP CONSTRAINT IF EXISTS ` + constraint) })
	if _, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "rollback", DestinationID: "synthetic"}); e == nil {
		t.Fatal("activation audit failure committed")
	}
	v, e := f.repo.GetOutput(f.ownerCtx, f.owner, s.ID, "hls")
	if e != nil || v.State != "stopping" || v.EgressID == "" {
		t.Fatal("observed provider ID lost on rollback", v, e)
	}
	if f.out.starts != 1 {
		t.Fatal(f.out.starts)
	}
	query(t, f.db, `ALTER TABLE owned_livestream_audit DROP CONSTRAINT `+constraint)
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" || f.out.stops != 1 {
		t.Fatal("retained cleanup did not complete")
	}
}

func TestPGLiveStreamOutputsFreshPolicyAndDurableStopRetry(t *testing.T) {
	f := newOutputFixture(t)
	pair := pgIssue(t, f.issuer, f.owner)
	p, e := f.verifier.Authenticate(f.ctx, pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	s := outputStream(t, f, "output-revocation")
	v := hlsStart(t, f, s.ID, "fresh-policy")
	rotated, e := f.issuer.Refresh(f.ctx, pair.RefreshToken, 0, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = outputDue(t, f, s.ID); e != nil || outputPhase(t, f, v.OutputID) != "active" {
		t.Fatal("ordinary refresh revoked output", e)
	}
	p, e = f.verifier.Authenticate(f.ctx, rotated.Token)
	if e != nil {
		t.Fatal(e)
	}
	f.ownerCtx = f.guard.Bind(f.ctx, p)
	f.out.failStop = true
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=false WHERE id=$1::uuid`, f.owner)
	if e := outputDue(t, f, s.ID); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "stopping" {
		t.Fatal("revoked output was retained as active")
	}
	// Restoring authority cannot revive an admission already pending removal.
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.owner)
	f.out.failStop = false
	f.out.pendingStop = true
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "stopping" {
		t.Fatal("provider ENDING was falsely completed")
	}
	f.out.pendingStop = false
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" || f.out.starts != 1 {
		t.Fatal("retry restarted recording")
	}
	var phase string
	if e := f.db.QueryRow(`SELECT phase FROM owned_livestream_runs WHERE stream_id=$1::uuid`, s.ID).Scan(&phase); e != nil || phase != "active" {
		t.Fatal("output revocation ended unrelated valid broadcast", phase, e)
	}
	v = hlsStart(t, f, s.ID, "rotated-policy")
	f.out.policy = "synthetic-v2"
	if e := outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("old destination policy remained admitted")
	}
	relay, e := f.repo.StartEgress(f.ownerCtx, f.owner, s.ID, live.EgressRequest{Key: "shadowban", DestinationIDs: []string{"relay"}})
	if e != nil {
		t.Fatal(e)
	}
	fixturePublisherShadowban(t, f.db, f.pub.ID, "other", time.Now().UTC())
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, relay.OutputID) != "done" {
		t.Fatal("relay ignored canonical public publisher visibility")
	}
}

func TestPGLiveStreamOutputsConcurrentIdempotencyAndCapacity(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-concurrent")
	const n = 6
	results := make(chan live.OutputResponse, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"})
			results <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, live.ErrOutputUncertain) {
			t.Fatal(e)
		}
	}
	if f.out.starts != 1 {
		t.Fatal("duplicate remote start", f.out.starts)
	}
	v, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"})
	if e != nil || v.State != "active" {
		t.Fatal(v, e)
	}
	for result := range results {
		if result.OutputID != "" && result.OutputID != v.OutputID {
			t.Fatal("duplicate intent")
		}
	}
	if _, e = f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "different-key", DestinationID: "synthetic"}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("new key silently reused active intent", e)
	}
	if e = f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	if replay, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "concurrent", DestinationID: "synthetic"}); e != nil || replay.OutputID != v.OutputID || replay.State != "done" {
		t.Fatal("completed key created output", replay, e)
	}
	cfg := outputConfig()
	cfg.MaxOutputsPerStream = 1
	if e = f.repo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	if _, e = f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{Key: "capacity", DestinationID: "synthetic"}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("capacity ignored", e)
	}
	opposing := outputStream(t, f, "opposing-editors")
	query(t, f.db, `UPDATE owned_authority_accounts SET is_superuser=true WHERE id=$1::uuid`, f.other)
	p, e := f.verifier.Authenticate(f.ctx, f.otherToken)
	if e != nil {
		t.Fatal(e)
	}
	editor := f.guard.Bind(f.ctx, p)
	race := make(chan error, 2)
	go func() {
		_, e := f.repo.StartHLS(f.ownerCtx, f.owner, opposing.ID, live.HLSRequest{Key: "owner", DestinationID: "synthetic"})
		race <- e
	}()
	go func() {
		_, e := f.repo.StartHLS(editor, f.other, opposing.ID, live.HLSRequest{Key: "editor", DestinationID: "synthetic"})
		race <- e
	}()
	for i := 0; i < 2; i++ {
		e := <-race
		if e != nil && !errors.Is(e, live.ErrConflict) && !errors.Is(e, live.ErrOutputUncertain) {
			t.Fatal("opposing editor lock order", e)
		}
	}
	if f.out.starts != 2 {
		t.Fatal("opposing editors duplicated remote start", f.out.starts)
	}
}

func TestPGLiveStreamOutputsDisableDuringAuthorityWait(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-disabled-wait")
	base := atomicAuthorityFixture{db: f.db, ctx: f.ctx, actor: f.owner}
	blocker, pid := atomicBlocker(t, base, "owned_authority_accounts", f.owner)
	if _, e := blocker.ExecContext(f.ctx, `UPDATE owned_authority_accounts SET disabled=true,version=version+1 WHERE id=$1::uuid`, f.owner); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := f.repo.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{DestinationID: "synthetic"})
		done <- e
	}()
	waitAtomicBlocked(t, base, pid, "owned_authority_accounts")
	if e := blocker.Commit(); e != nil {
		t.Fatal(e)
	}
	if e := <-done; !errors.Is(e, authority.ErrDisabled) && !errors.Is(e, authority.ErrStale) {
		t.Fatal(e)
	}
	if f.out.starts != 0 {
		t.Fatal("disabled actor reached provider")
	}
	if countCalls(t, f.callFixture, `SELECT count(*) FROM owned_livestream_outputs WHERE stream_id=$1::uuid`, s.ID) != 0 {
		t.Fatal("disabled actor left an intent")
	}
}

func TestPGLiveStreamOutputsEndCleanupIndependentAndUnconfigured(t *testing.T) {
	f := newOutputFixture(t)
	s := outputStream(t, f, "output-independent")
	v := hlsStart(t, f, s.ID, "independent")
	f.provider.failDelete = true
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_livestream_runs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE stream_id=$1::uuid`, s.ID)
	query(t, f.db, `UPDATE owned_livestream_outputs SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1::uuid`, v.OutputID)
	if _, e := f.repo.ProcessDue(f.ctx, 100); !errors.Is(e, live.ErrProvider) {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("room cleanup failure hid output stop")
	}
	f.provider.failDelete = false
	streamDue(t, f.streamFixture, s.ID)
	streamNative(t, f.streamFixture, s.ID)
	f.out.failStop = true
	v = hlsStart(t, f, s.ID, "pending-unconfigured")
	if e := f.repo.End(f.ownerCtx, f.owner, s.ID, false); e != nil {
		t.Fatal(e)
	}
	plain, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.provider, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = plain.StartHLS(f.ownerCtx, f.owner, s.ID, live.HLSRequest{DestinationID: "synthetic"}); !errors.Is(e, live.ErrOutputUnavailable) {
		t.Fatal("unconfigured HLS accepted", e)
	}
	yes := true
	if _, e = plain.Start(f.ownerCtx, f.owner, s.ID, live.StartRequest{NoIngress: &yes}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("disabled output module bypassed prior cleanup obligation", e)
	}
	privacy := live.Private
	if _, e = plain.Update(f.ownerCtx, f.owner, s.ID, live.Patch{Visibility: &privacy}); !errors.Is(e, live.ErrConflict) {
		t.Fatal("privacy changed before prior output was closed", e)
	}
	f.out.failStop = false
	if e = outputDue(t, f, s.ID); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, v.OutputID) != "done" {
		t.Fatal("pending output not discharged")
	}
}

func TestPGLiveStreamOutputsRestartPersistence(t *testing.T) {
	phase := fixtureRestartPhase(t)
	database(t)
	path := filepath.Join(os.Getenv("SOLSYNTH_DISPOSABLE_PG"), "synthetic-livestream-output-restart.json")
	type state struct {
		Stream, Output, Endpoint string
		Provider                 live.ProviderOutput
	}
	var saved state
	if phase == "before" {
		f := newOutputFixture(t)
		s := outputStream(t, f, "outputs-restart")
		v := hlsStart(t, f, s.ID, "restart")
		f.out.failStop = true
		if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); !errors.Is(e, live.ErrProvider) {
			t.Fatal(e)
		}
		saved = state{Stream: s.ID, Output: v.OutputID, Endpoint: f.provider.Endpoint(), Provider: f.out.outputs[v.EgressID]}
		raw, e := json.Marshal(saved)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
		return
	}
	if phase != "after" {
		t.Fatal("PG_TEST_PHASE must be before or after")
	}
	raw, e := os.ReadFile(path)
	if e != nil || json.Unmarshal(raw, &saved) != nil {
		t.Fatal("missing synthetic output restart state", e)
	}
	f := newOutputFixture(t)
	f.provider.endpoint = saved.Endpoint
	f.out.outputs[saved.Provider.ID] = saved.Provider
	if outputPhase(t, f, saved.Output) != "stopping" {
		t.Fatal("cleanup intent did not survive restart")
	}
	if e = outputDue(t, f, saved.Stream); e != nil {
		t.Fatal(e)
	}
	if outputPhase(t, f, saved.Output) != "done" || f.out.starts != 0 || f.out.stops != 1 {
		t.Fatal("restart lost cleanup or recreated output")
	}
	// Existing recorded admissions use the split durable generation after restart.
	if e = authoritypg.New(f.db).ContinuityReady(f.ctx); e != nil {
		t.Fatal(e)
	}
}

func TestPGLiveStreamStoppedArchiveImportUncertainReplay(t *testing.T) {
	f := newOutputFixture(t)
	invalid := outputConfig()
	invalid.ArchiveImporter = &archiveImporterFixture{contract: "unreviewed-import-adapter"}
	if e := f.repo.WithOutputs(invalid, f.store); !errors.Is(e, live.ErrInvalid) {
		t.Fatal("unreviewed archive adapter accepted", e)
	}
	importer := &archiveImporterFixture{db: f.db, mediaID: authorityUUID(t), jobs: map[string]string{}, failAfter: true}
	cfg := outputConfig()
	cfg.ArchiveImporter = importer
	if e := f.repo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	s := outputStream(t, f, "archive-import")
	v := hlsStart(t, f, s.ID, "archive-source")
	outputFiles(t, f, v.OutputID)
	if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	job, e := f.repo.RequestArchiveImport(f.ownerCtx, f.owner, s.ID, v.OutputID, "archive-job")
	if e != nil || job.State != "pending" {
		t.Fatal(job, e)
	}
	if _, e = f.repo.RequestArchiveImport(f.ownerCtx, f.owner, s.ID, v.OutputID, "archive-job"); e != nil {
		t.Fatal("exact request did not replay", e)
	}
	if _, e = f.repo.ProcessArchiveImportsDue(f.ctx, 10); e == nil {
		t.Fatal("uncertain adapter result was accepted")
	}
	operations, total, e := f.repo.InspectOperations(f.ownerCtx, f.owner, "archive", 10, 0)
	if e != nil || total != 1 || len(operations) != 1 || operations[0].ID != job.ID || operations[0].Attempts != 1 {
		t.Fatal(operations, total, e)
	}
	redriven, e := f.repo.RedriveOperation(f.ownerCtx, f.owner, "archive", job.ID)
	if e != nil || redriven.ID != job.ID || redriven.Attempts != 0 || redriven.State != "pending" {
		t.Fatal(redriven, e)
	}
	// Simulate a process dying after its claim commits. A separately constructed
	// repository must reclaim only the expired fenced lease and replay the stable
	// job identity into the importer.
	query(t, f.db, `UPDATE owned_livestream_archive_imports SET state='processing',lease_token=$2::uuid,lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, job.ID, authorityUUID(t))
	secondRepo, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.out, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = secondRepo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	if n, e := secondRepo.ProcessArchiveImportsDue(f.ctx, 10); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	complete, e := f.repo.GetArchiveImport(f.ownerCtx, f.owner, s.ID, v.OutputID, job.ID)
	if e != nil || complete.State != "complete" || complete.MediaID == nil || *complete.MediaID != importer.mediaID || importer.calls != 2 || len(importer.jobs) != 1 {
		t.Fatal(complete, importer.calls, importer.jobs, e)
	}
	// Status remains inspectable after a restart where the external conversion
	// adapter is intentionally unavailable.
	if e = f.repo.WithOutputs(outputConfig(), f.store); e != nil {
		t.Fatal(e)
	}
	if complete, e = f.repo.GetArchiveImport(f.ownerCtx, f.owner, s.ID, v.OutputID, job.ID); e != nil || complete.State != "complete" {
		t.Fatal(complete, e)
	}
	playback, e := f.repo.OpenHLS(f.viewerCtx, f.third, s.ID, v.OutputID, "playlist.m3u8")
	if e != nil || !strings.Contains(string(playback.Body), "segment_0.ts") {
		t.Fatal("stopped archive unavailable", e)
	}
}

func TestPGLiveStreamArchiveStagingDoesNotPublishAfterProofRevocation(t *testing.T) {
	f := newOutputFixture(t)
	importer := &archiveImporterFixture{db: f.db, mediaID: authorityUUID(t), jobs: map[string]string{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := outputConfig()
	cfg.ArchiveImporter = importer
	if e := f.repo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	s := outputStream(t, f, "archive-revoked-after-io")
	v := hlsStart(t, f, s.ID, "archive-revoked-source")
	outputFiles(t, f, v.OutputID)
	if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	job, e := f.repo.RequestArchiveImport(f.ownerCtx, f.owner, s.ID, v.OutputID, "archive-revoked-job")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, x := f.repo.ProcessArchiveImportsDue(f.ctx, 1)
		done <- x
	}()
	select {
	case <-importer.entered:
	case <-f.ctx.Done():
		t.Fatal("archive adapter did not reach staged external completion")
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET authorization_epoch=authorization_epoch+1 WHERE id=(SELECT session_id FROM owned_livestream_archive_imports WHERE id=$1::uuid)`, job.ID)
	query(t, f.db, `UPDATE owned_publishers SET account_id=$2::uuid,updated_at=clock_timestamp() WHERE id=$1::uuid`, f.pub.ID, f.other)
	close(importer.release)
	if e = <-done; e == nil {
		t.Fatal("revoked archive proof was finalized")
	}
	var mediaState, charge, jobState string
	var used, reserved int64
	if e = f.db.QueryRow(`SELECT f.state,f.charge,q.used_bytes,q.reserved_bytes,j.state FROM owned_media_files f JOIN owned_media_quotas q ON q.account_id=f.account_id JOIN owned_livestream_archive_imports j ON j.media_id IS NULL WHERE f.id=$1::uuid AND j.id=$2::uuid`, importer.mediaID, job.ID).Scan(&mediaState, &charge, &used, &reserved, &jobState); e != nil {
		t.Fatal(e)
	}
	if mediaState != "pending" || charge != "reserved" || used != 0 || reserved != 1 || jobState == "complete" {
		t.Fatal("revoked archive became readable or permanently charged", mediaState, charge, used, reserved, jobState)
	}
	query(t, f.db, `UPDATE owned_livestream_archive_imports SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1::uuid`, job.ID)
	secondRepo, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.out, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = secondRepo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	if _, e = secondRepo.ProcessArchiveImportsDue(f.ctx, 1); e == nil {
		t.Fatal("permanently revoked archive claim did not report authority loss")
	}
	var attempts int
	if e = f.db.QueryRow(`SELECT state,attempts FROM owned_livestream_archive_imports WHERE id=$1::uuid`, job.ID).Scan(&jobState, &attempts); e != nil || jobState != "failed" || attempts < 2 {
		t.Fatal("revoked archive remained in the due hot loop", jobState, attempts, e)
	}
	if n, e := secondRepo.ProcessArchiveImportsDue(f.ctx, 1); e != nil || n != 0 {
		t.Fatal("terminal archive was selected again", n, e)
	}
}

func TestPGLiveStreamArchiveEditorRechecksPunishedOwnerAfterIO(t *testing.T) {
	f := newOutputFixture(t)
	importer := &archiveImporterFixture{db: f.db, mediaID: authorityUUID(t), jobs: map[string]string{}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	cfg := outputConfig()
	cfg.ArchiveImporter = importer
	if e := f.repo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	s := outputStream(t, f, "archive-editor-owner-punishment")
	v := hlsStart(t, f, s.ID, "archive-editor-source")
	outputFiles(t, f, v.OutputID)
	if e := f.repo.StopOutput(f.ownerCtx, f.owner, s.ID, "hls"); e != nil {
		t.Fatal(e)
	}
	job, e := f.repo.RequestArchiveImport(f.editorCtx, f.other, s.ID, v.OutputID, "archive-editor-job")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, x := f.repo.ProcessArchiveImportsDue(f.ctx, 1)
		done <- x
	}()
	select {
	case <-importer.entered:
	case <-f.ctx.Done():
		t.Fatal("archive adapter did not reach staged external completion")
	}
	var starts time.Time
	if e = f.db.QueryRowContext(f.ctx, `INSERT INTO owned_authority_punishments(account_id,kind,starts_at)VALUES($1::uuid,'disable_account',clock_timestamp()+interval '1 second')RETURNING starts_at`, f.owner).Scan(&starts); e != nil {
		t.Fatal(e)
	}
	for {
		var active bool
		if e = f.db.QueryRowContext(f.ctx, `SELECT clock_timestamp()>=$1::timestamptz`, starts).Scan(&active); e != nil {
			t.Fatal(e)
		}
		if active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(importer.release)
	if e = <-done; e == nil {
		t.Fatal("editor archive ignored current publisher-owner punishment")
	}
	var mediaState, charge, jobState string
	var used, reserved int64
	if e = f.db.QueryRow(`SELECT f.state,f.charge,q.used_bytes,q.reserved_bytes,j.state FROM owned_media_files f JOIN owned_media_quotas q ON q.account_id=f.account_id JOIN owned_livestream_archive_imports j ON j.id=$2::uuid AND j.media_id IS NULL WHERE f.id=$1::uuid`, importer.mediaID, job.ID).Scan(&mediaState, &charge, &used, &reserved, &jobState); e != nil {
		t.Fatal(e)
	}
	if mediaState != "pending" || charge != "reserved" || used != 0 || reserved != 1 || jobState == "complete" {
		t.Fatal("punished owner archive became readable or permanently charged", mediaState, charge, used, reserved, jobState)
	}
	query(t, f.db, `UPDATE owned_livestream_archive_imports SET next_attempt_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1::uuid`, job.ID)
	secondRepo, e := livestreampg.New(f.db, f.guard, f.accounts, f.media, f.social, f.out, streamConfig())
	if e != nil {
		t.Fatal(e)
	}
	if e = secondRepo.WithOutputs(cfg, f.store); e != nil {
		t.Fatal(e)
	}
	if _, e = secondRepo.ProcessArchiveImportsDue(f.ctx, 1); e == nil {
		t.Fatal("punished owner archive claim did not become terminal")
	}
	var attempts int
	if e = f.db.QueryRow(`SELECT state,attempts FROM owned_livestream_archive_imports WHERE id=$1::uuid`, job.ID).Scan(&jobState, &attempts); e != nil || jobState != "failed" || attempts < 2 {
		t.Fatal("punished owner archive remained in the due hot loop", jobState, attempts, e)
	}
	query(t, f.db, `UPDATE owned_media_files SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid AND state='pending'`, importer.mediaID)
	if n, x := f.media.Cleanup(f.ctx, 100); x != nil || n != 1 {
		t.Fatal("staged archive cleanup did not release quota", n, x)
	}
	if e = f.db.QueryRow(`SELECT state,charge FROM owned_media_files WHERE id=$1::uuid`, importer.mediaID).Scan(&mediaState, &charge); e != nil || mediaState != "deleted" || charge != "none" {
		t.Fatal(mediaState, charge, e)
	}
	query(t, f.db, `DELETE FROM owned_authority_punishments WHERE account_id=$1::uuid AND kind='disable_account'`, f.owner)
	if _, e = f.repo.RedriveOperation(f.ownerCtx, f.owner, "archive", job.ID); e != nil {
		t.Fatal(e)
	}
	if n, x := secondRepo.ProcessArchiveImportsDue(f.ctx, 1); x != nil || n != 1 {
		t.Fatal("redrive did not rebuild cleaned archive staging", n, x)
	}
	if e = f.db.QueryRow(`SELECT f.state,f.charge,q.used_bytes,q.reserved_bytes,j.state FROM owned_media_files f JOIN owned_media_quotas q ON q.account_id=f.account_id JOIN owned_livestream_archive_imports j ON j.media_id=f.id WHERE f.id=$1::uuid AND j.id=$2::uuid`, importer.mediaID, job.ID).Scan(&mediaState, &charge, &used, &reserved, &jobState); e != nil || mediaState != "ready" || charge != "used" || used != 1 || reserved != 0 || jobState != "complete" {
		t.Fatal("cleaned archive redrive did not restore one conserved ready media", mediaState, charge, used, reserved, jobState, e)
	}
}
